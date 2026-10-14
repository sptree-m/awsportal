package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sptree-m/awsportal/internal/auth"
	"github.com/sptree-m/awsportal/internal/store"
)

func loginRequestDetail(r *http.Request, reason string) store.LoginAuditDetail {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	bounded := func(s string, limit int) string {
		rs := []rune(s)
		return string(rs[:min(len(rs), limit)])
	}
	return store.LoginAuditDetail{Reason: reason, RemoteIP: bounded(peer, 128), UserAgent: bounded(r.UserAgent(), 512), ForwardedFor: bounded(r.Header.Get("X-Forwarded-For"), 512)}
}

func (a *app) loginFailure(w http.ResponseWriter, r *http.Request, username, reason string) {
	detail, err := a.db.RecordLoginFailure(r.Context(), username, loginRequestDetail(r, reason), time.Now())
	if err != nil {
		log.Printf("login security record failed: %v", err)
		http.Error(w, "認証処理を完了できません", http.StatusServiceUnavailable)
		return
	}
	if detail.LockedAt != 0 {
		// Revoke existing sessions so they cannot become valid again after unlock.
		a.mu.Lock()
		for token, s := range a.sessions {
			if s.User.Username == username {
				delete(a.sessions, token)
			}
		}
		a.mu.Unlock()
	} else if remaining := detail.RetryAt - time.Now().Unix(); remaining > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt(remaining, 10))
		http.Error(w, "ログインの試行回数が上限に達しました。"+strconv.FormatInt(remaining, 10)+"秒後に再試行してください", http.StatusTooManyRequests)
		return
	}
	// Keep all credential failures indistinguishable to unauthenticated callers.
	http.Error(w, "認証に失敗しました。ログインできない場合はPortal Adminに確認してください", http.StatusUnauthorized)
}

func (a *app) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	if err := r.ParseForm(); err != nil {
		a.loginFailure(w, r, "", "invalid_request")
		return
	}
	username := r.PostFormValue("username")
	u, err := a.db.UserByName(r.Context(), username)
	if err == sql.ErrNoRows {
		a.loginFailure(w, r, username, "unknown_user")
		return
	}
	if err != nil {
		a.loginFailure(w, r, username, "authentication_error")
		return
	}
	now := time.Now()
	reason := ""
	switch {
	case u.LockedAt != 0:
		reason = "account_locked"
	case u.LoginRetryAt > now.Unix():
		reason = "login_throttled"
	case !u.Enabled:
		reason = "account_disabled"
	case a.db.AccountExpired(u, now):
		if err = a.db.DisableInactive(r.Context(), u.ID, now); err != nil {
			reason = "authentication_error"
		} else {
			a.db.Audit(r.Context(), u.Username, "account.auto_disable", u.Username, "ok", "inactive_30_days")
			reason = "account_expired"
		}
	case u.TempPasswordExpires > 0 && now.Unix() > u.TempPasswordExpires:
		reason = "temporary_password_expired"
	case u.Role != "portal_admin":
		reason = "passkey_required"
	case !auth.CheckPassword(u.PasswordHash, r.PostFormValue("password")):
		reason = "invalid_password"
	default:
		valid, mfaErr := a.checkLoginMFA(r.Context(), u, r.PostFormValue("totp"))
		if mfaErr != nil {
			reason = "authentication_error"
		} else if !valid {
			reason = "invalid_mfa"
		}
	}
	if reason != "" {
		a.loginFailure(w, r, username, reason)
		return
	}
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		a.loginFailure(w, r, username, "authentication_error")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	// Coordinate session creation with lock-triggered session revocation.
	a.mu.Lock()
	err = a.db.RecordLogin(r.Context(), u.ID, time.Now())
	if err == nil {
		a.sessions[token] = session{User: u, Expires: time.Now().Add(8 * time.Hour), AuthenticatedAt: time.Now(), Method: "password_totp"}
	}
	a.mu.Unlock()
	if err != nil {
		a.loginFailure(w, r, username, "account_state_changed")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "awsportal_session", Value: token, Path: "/", HttpOnly: true, Secure: env("AWSPORTAL_COOKIE_SECURE", "1") == "1", SameSite: http.SameSiteStrictMode, MaxAge: 28800})
	a.db.Audit(r.Context(), u.Username, "login", "", "ok", "")
	if u.MustChangePassword {
		http.Redirect(w, r, "/change-password", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *app) adminUnlockUser(w http.ResponseWriter, r *http.Request) {
	admin := r.Context().Value("user").(store.User)
	if admin.Role != "portal_admin" {
		http.Error(w, "管理者のみ", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "入力を確認してください", http.StatusBadRequest)
		return
	}
	target := r.PathValue("username")
	reason := strings.TrimSpace(r.PostFormValue("reason"))
	if err := a.db.UnlockUser(r.Context(), admin, target, reason, loginRequestDetail(r, ""), time.Now()); err != nil {
		a.db.Audit(r.Context(), admin.Username, "account.unlock", target, "deny", "invalid reason, account state or administrator")
		http.Error(w, "解除理由とユーザーのロック状態を確認してください", http.StatusBadRequest)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		a.adminUserFragment(w, r, target)
		return
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}
