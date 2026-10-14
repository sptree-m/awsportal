package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sptree-m/awsportal/internal/auth"
)

func TestBrowserMutationAndRecentAdminAtProductionRoutes(t *testing.T) {
	for _, mode := range []string{"valid", "missing_csrf", "foreign_origin", "same_site_origin", "old_session"} {
		t.Run(mode, func(t *testing.T) {
			a := testPasskeyApp(t)
			u := createPasskeyUser(t, a, "portal_admin")
			form := url.Values{"password": {"New-Password-42"}, "confirm": {"New-Password-42"}}
			r := requestAs(a, u, "POST", testPasskeyOrigin+"/change-password", strings.NewReader(form.Encode()))
			r.Header.Set("Origin", testPasskeyOrigin)
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			if mode == "missing_csrf" {
				r.Header.Del("X-CSRF-Token")
			}
			if mode == "foreign_origin" || mode == "same_site_origin" {
				r.Header.Set("Origin", "https://untrusted.example.com")
				r.Header.Set("Sec-Fetch-Site", "same-site")
			}
			if mode == "old_session" {
				s := a.sessions["test-session-"+u.Username]
				s.AuthenticatedAt = time.Now().Add(-time.Hour)
				a.sessions["test-session-"+u.Username] = s
			}
			w := httptest.NewRecorder()
			a.handler().ServeHTTP(w, r)
			fresh, err := a.db.UserByName(context.Background(), u.Username)
			if err != nil {
				t.Fatal(err)
			}
			changed := auth.CheckPassword(fresh.PasswordHash, "New-Password-42")
			if changed != (mode == "valid") {
				t.Fatalf("changed=%v status=%d body=%s", changed, w.Code, w.Body.String())
			}
		})
	}
}

func TestMFAChangeRequiresRecentAuthentication(t *testing.T) {
	a := testPasskeyApp(t)
	u := createPasskeyUser(t, a, "portal_admin")
	r := requestAs(a, u, "POST", testPasskeyOrigin+"/mfa/add", strings.NewReader("name=stolen-session-device"))
	s := a.sessions["test-session-"+u.Username]
	s.AuthenticatedAt = time.Now().Add(-time.Hour)
	a.sessions["test-session-"+u.Username] = s
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, r)
	ds, err := a.db.MFADevices(context.Background(), u.ID)
	if err != nil || len(ds) != 0 || w.Header().Get("Location") != "/login" {
		t.Fatalf("devices=%d status=%d error=%v", len(ds), w.Code, err)
	}
}

func TestTOTPLoginRejectsReplay(t *testing.T) {
	a := testPasskeyApp(t)
	u := createPasskeyUser(t, a, "portal_admin")
	code, err := auth.TOTPAt(u.TOTPSecret, time.Now(), 6)
	if err != nil {
		t.Fatal(err)
	}
	if w := securityLogin(a, u.Username, "Correct-Password-42", code); w.Code != 303 {
		t.Fatalf("first login=%d", w.Code)
	}
	if w := securityLogin(a, u.Username, "Correct-Password-42", code); w.Code != 401 {
		t.Fatalf("replay login=%d", w.Code)
	}
	if len(a.sessions) != 1 {
		t.Fatalf("sessions=%d", len(a.sessions))
	}
}

func TestAnonymousAuthenticationRateLimitAndProxyTrust(t *testing.T) {
	a := testPasskeyApp(t)
	h := a.handler()
	for i := 0; i < 13; i++ {
		r := httptest.NewRequest("POST", testPasskeyOrigin+"/login", strings.NewReader("username=missing"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.RemoteAddr = "192.0.2.10:1234"
		r.Header.Set("X-Real-IP", "198.51.100.99")
		r.Header.Set("X-Forwarded-For", "198.51.100.1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if i < 12 && w.Code != 401 {
			t.Fatalf("attempt %d=%d", i, w.Code)
		}
		if i == 12 && (w.Code != 429 || w.Header().Get("Retry-After") == "") {
			t.Fatalf("no throttling: %d", w.Code)
		}
	}
	r := httptest.NewRequest("POST", testPasskeyOrigin+"/login", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("X-Real-IP", "198.51.100.7")
	if got := a.limiter.peer(r); got != "198.51.100.7" {
		t.Fatal(got)
	}
	r.RemoteAddr = "192.0.2.1:1234"
	if got := a.limiter.peer(r); got != "192.0.2.1" {
		t.Fatal("untrusted proxy header accepted", got)
	}
}

func TestChallengeRotationDoesNotBlockLegitimateLogin(t *testing.T) {
	a := testPasskeyApp(t)
	u := createPasskeyUser(t, a, "user")
	k := newTestAuthenticator(t)
	enrollTestPasskey(t, a, u, k)
	for i := 0; i < 16; i++ {
		beginTestPasskey(t, a, "/passkeys/login/begin", map[string]string{"username": u.Username})
	}
	challenge, cookie := beginTestPasskey(t, a, "/passkeys/login/begin", map[string]string{"username": u.Username})
	w := passkeyRequest(a, a.passkeyLoginFinish, "/passkeys/login/finish", k.response(t, challenge, testPasskeyOrigin, "portal.example.com", false, 5, 1), cookie)
	if w.Code != 200 {
		t.Fatalf("legitimate proof blocked: %d %s", w.Code, w.Body.String())
	}
	var count int
	a.db.DB.QueryRow(`SELECT COUNT(*) FROM passkey_challenges WHERE user_id=?`, u.ID).Scan(&count)
	if count > 16 {
		t.Fatalf("unbounded challenges=%d", count)
	}
}

func TestExpiredSessionsAreDeletedAndPerUserBounded(t *testing.T) {
	a := testPasskeyApp(t)
	u := createPasskeyUser(t, a, "user")
	now := time.Now()
	a.sessions["expired"] = session{User: u, Expires: now.Add(-time.Second), LastSeen: now.Add(-time.Hour)}
	a.mu.Lock()
	for i := 0; i < 8; i++ {
		if !a.putSessionLocked(string(rune('a'+i)), session{User: u, Expires: now.Add(time.Hour), AuthenticatedAt: now.Add(time.Duration(i) * time.Second)}) {
			t.Fatal("session rejected")
		}
	}
	a.mu.Unlock()
	if len(a.sessions) != 5 {
		t.Fatalf("sessions=%d", len(a.sessions))
	}
	a.sessions["idle"] = session{User: u, Expires: now.Add(time.Hour), LastSeen: now.Add(-31 * time.Minute)}
	r := httptest.NewRequest("GET", testPasskeyOrigin+"/passkeys", nil)
	r.AddCookie(&http.Cookie{Name: "awsportal_session", Value: "idle"})
	w := httptest.NewRecorder()
	a.require(a.passkeyPage)(w, r)
	if _, ok := a.sessions["idle"]; ok || w.Header().Get("Location") != "/login" {
		t.Fatal("idle session survived")
	}
}
