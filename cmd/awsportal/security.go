package main

import (
	"context"
	"crypto/hmac"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/sptree-m/awsportal/internal/store"
)

// All cookie-authenticated mutations pass through this gate, including htmx.
// Agent/Git APIs use their own bearer credentials and never accept Portal cookies.
func (a *app) browserMutation(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == "GET" || r.Method == "HEAD" || r.Method == "OPTIONS" {
		return true
	}
	origin := r.Header.Get("Origin")
	if a.webauthn != nil && origin != "" && origin != "null" && origin != a.webauthn.Config.RPOrigins[0] {
		http.Error(w, "接続先を確認してください", http.StatusForbidden)
		return false
	}
	if r.Header.Get("Sec-Fetch-Site") != "" || (origin != "" && origin != "null") {
		if http.NewCrossOriginProtection().Check(r) != nil {
			http.Error(w, "別の接続先からの操作はできません", http.StatusForbidden)
			return false
		}
	}
	got := r.Header.Get("X-CSRF-Token")
	if got == "" {
		// Browser forms have a small common limit. Larger machine API payloads
		// are deliberately outside the cookie-authentication middleware.
		r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
		if r.ParseForm() != nil {
			http.Error(w, "入力を確認してください", http.StatusBadRequest)
			return false
		}
		got = r.PostFormValue("csrf")
	}
	want := passkeyCSRF(r)
	if want == "" || !hmac.Equal([]byte(want), []byte(got)) {
		http.Error(w, "操作権限を確認できません。画面を再表示してください", http.StatusForbidden)
		return false
	}
	return true
}

func (a *app) requireRecentAdmin(next http.HandlerFunc) http.HandlerFunc {
	return a.require(func(w http.ResponseWriter, r *http.Request) {
		u := r.Context().Value("user").(store.User)
		if u.Role != "portal_admin" {
			http.Error(w, "管理者のみ", http.StatusForbidden)
			return
		}
		cookie, _ := r.Cookie("awsportal_session")
		a.mu.Lock()
		s := a.sessions[cookie.Value]
		a.mu.Unlock()
		if s.AuthenticatedAt.IsZero() || time.Since(s.AuthenticatedAt) > 10*time.Minute {
			loginRedirect(w, r)
			return
		}
		next(w, r)
	})
}

type authBucket struct {
	tokens float64
	last   time.Time
}
type authLimiter struct {
	mu      sync.Mutex
	peers   map[string]authBucket
	trusted []netip.Prefix
	slots   chan struct{}
}

func newAuthLimiter() (*authLimiter, error) {
	l := &authLimiter{peers: map[string]authBucket{}, slots: make(chan struct{}, 8)}
	for _, value := range strings.Split(env("AWSPORTAL_TRUSTED_PROXY_CIDRS", "127.0.0.1/32,::1/128"), ",") {
		p, err := netip.ParsePrefix(strings.TrimSpace(value))
		if err != nil {
			return nil, err
		}
		l.trusted = append(l.trusted, p)
	}
	return l, nil
}

func (l *authLimiter) peer(r *http.Request) string {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return "unknown"
	}
	ip = ip.Unmap()
	for _, p := range l.trusted {
		if p.Contains(ip) {
			// nginx overwrites this single-valued header. Never trust arbitrary
			// X-Forwarded-For lists or headers from an untrusted network peer.
			if client, err := netip.ParseAddr(r.Header.Get("X-Real-IP")); err == nil {
				return client.Unmap().String()
			}
		}
	}
	return ip.String()
}

func (l *authLimiter) allow(peer string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, b := range l.peers {
		if now.Sub(b.last) > 10*time.Minute {
			delete(l.peers, k)
		}
	}
	b, ok := l.peers[peer]
	if !ok {
		if len(l.peers) >= 4096 {
			return false
		}
		b = authBucket{tokens: 12, last: now}
	}
	b.tokens = min(12, b.tokens+now.Sub(b.last).Seconds())
	b.last = now
	allowed := b.tokens >= 1
	if allowed {
		b.tokens--
	}
	l.peers[peer] = b
	return allowed
}

func (a *app) limitAuthentication(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/login" {
			if http.NewCrossOriginProtection().Check(r) != nil {
				http.Error(w, "別の接続先からのログインはできません", http.StatusForbidden)
				return
			}
		}
		if r.Method == "POST" && (r.URL.Path == "/login" || strings.HasPrefix(r.URL.Path, "/passkeys/login/") || strings.HasPrefix(r.URL.Path, "/passkeys/register/")) {
			if !a.limiter.allow(a.limiter.peer(r), time.Now()) {
				w.Header().Set("Retry-After", "1")
				http.Error(w, "試行が多すぎます。少し待って再試行してください", http.StatusTooManyRequests)
				return
			}
			select {
			case a.limiter.slots <- struct{}{}:
				defer func() { <-a.limiter.slots }()
			default:
				w.Header().Set("Retry-After", "1")
				http.Error(w, "認証処理が混み合っています", http.StatusTooManyRequests)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func sessionExpired(s session, now time.Time) bool {
	return !now.Before(s.Expires) || (!s.LastSeen.IsZero() && now.Sub(s.LastSeen) > 30*time.Minute)
}

func (a *app) pruneSessionsLocked(now time.Time) {
	for token, s := range a.sessions {
		if sessionExpired(s, now) {
			delete(a.sessions, token)
		}
	}
}

// Caller holds a.mu. Bound authenticated state as well as anonymous state.
func (a *app) putSessionLocked(token string, s session) bool {
	a.pruneSessionsLocked(time.Now())
	count, oldestToken := 0, ""
	var oldest time.Time
	for key, existing := range a.sessions {
		if existing.User.ID == s.User.ID {
			count++
			if oldestToken == "" || existing.AuthenticatedAt.Before(oldest) {
				oldestToken, oldest = key, existing.AuthenticatedAt
			}
		}
	}
	if count >= 5 {
		delete(a.sessions, oldestToken)
	}
	if len(a.sessions) >= 4096 {
		return false
	}
	s.LastSeen = time.Now()
	a.sessions[token] = s
	return true
}

func (a *app) sessionJanitor(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			a.mu.Lock()
			a.pruneSessionsLocked(now)
			a.mu.Unlock()
		}
	}
}
