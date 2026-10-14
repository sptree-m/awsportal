package main

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/sptree-m/awsportal/internal/auth"
	"github.com/sptree-m/awsportal/internal/store"
)

type passkeyUser struct {
	store.User
	keys []webauthn.Credential
}

func (u passkeyUser) WebAuthnID() []byte                         { return []byte(fmt.Sprintf("awsportal-user-%d", u.ID)) }
func (u passkeyUser) WebAuthnName() string                       { return u.Username }
func (u passkeyUser) WebAuthnDisplayName() string                { return u.Username }
func (u passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.keys }

type passkeyChallenge struct {
	Action                        string
	Username                      string
	Generation                    int64
	Session                       webauthn.SessionData
	Name, InviteHash, SessionHash string
}

func (a *app) passkeyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /passkeys/enroll", a.passkeyEnrollPage)
	mux.HandleFunc("GET /passkeys", a.require(a.passkeyPage))
	mux.HandleFunc("POST /passkeys/login/begin", a.passkeyLoginBegin)
	mux.HandleFunc("POST /passkeys/login/finish", a.passkeyLoginFinish)
	mux.HandleFunc("POST /passkeys/register/begin", a.passkeyRegisterBegin)
	mux.HandleFunc("POST /passkeys/register/finish", a.passkeyRegisterFinish)
	mux.HandleFunc("POST /passkeys/{id}/delete", a.require(a.passkeyDelete))
	mux.HandleFunc("POST /admin/passkeys/invite", a.require(a.adminPasskeyInvite))
	mux.HandleFunc("POST /admin/passkeys/reset", a.require(a.adminPasskeyInvite))
}

func (a *app) passkeyOrigin(w http.ResponseWriter, r *http.Request) bool {
	if a.webauthn == nil {
		http.Error(w, "パスキーの接続先設定が必要です", 503)
		return false
	}
	if r.Header.Get("Origin") != a.webauthn.Config.RPOrigins[0] {
		http.Error(w, "接続先を確認してください", 403)
		return false
	}
	return true
}

func passkeyCSRF(r *http.Request) string {
	cookie, err := r.Cookie("awsportal_session")
	if err != nil {
		return ""
	}
	return store.DCVTokenHash("awsportal-passkey-form:" + cookie.Value)
}

// Referrer-Policy:no-referrer can make native form Origin opaque ("null").
// Session-bound CSRF tokens protect these forms without weakening that policy.
func (a *app) passkeyForm(w http.ResponseWriter, r *http.Request) bool {
	if a.webauthn == nil {
		http.Error(w, "パスキーの接続先設定が必要です", 503)
		return false
	}
	origin := r.Header.Get("Origin")
	if origin != "" && origin != "null" && origin != a.webauthn.Config.RPOrigins[0] {
		http.Error(w, "接続先を確認してください", 403)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	expected := passkeyCSRF(r)
	if r.ParseForm() != nil || expected == "" || !hmac.Equal([]byte(expected), []byte(r.PostFormValue("csrf"))) {
		http.Error(w, "操作権限を確認できません。画面を再表示してください", 403)
		return false
	}
	return true
}

func readPasskeyJSON(w http.ResponseWriter, r *http.Request, v any) error {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		return fmt.Errorf("JSON required")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	return json.NewDecoder(r.Body).Decode(v)
}
func passkeyJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (a *app) passkeyAccount(ctx context.Context, username string, enroll bool) (passkeyUser, string) {
	u, err := a.db.UserByName(ctx, username)
	if err == sql.ErrNoRows {
		return passkeyUser{}, "unknown_user"
	}
	if err != nil {
		return passkeyUser{}, "authentication_error"
	}
	now := time.Now()
	switch {
	case u.LockedAt != 0:
		return passkeyUser{User: u}, "account_locked"
	case u.LoginRetryAt > now.Unix():
		return passkeyUser{User: u}, "login_throttled"
	case !u.Enabled:
		return passkeyUser{User: u}, "account_disabled"
	case a.db.AccountExpired(u, now):
		return passkeyUser{User: u}, "account_expired"
	case !enroll && u.Role != "portal_admin" && u.MustChangePassword:
		return passkeyUser{User: u}, "enrollment_required"
	case !enroll && u.Role == "portal_admin" && u.TempPasswordExpires > 0 && u.TempPasswordExpires < now.Unix():
		return passkeyUser{User: u}, "temporary_password_expired"
	}
	keys, err := a.db.Passkeys(ctx, u.ID)
	if err != nil {
		return passkeyUser{User: u}, "authentication_error"
	}
	result := passkeyUser{User: u}
	for _, k := range keys {
		result.keys = append(result.keys, k.Credential)
	}
	return result, ""
}

func (a *app) beginPasskey(w http.ResponseWriter, r *http.Request, u passkeyUser, purpose string, c passkeyChallenge, options any) {
	token, err := auth.NewPasskeyToken()
	if err != nil {
		http.Error(w, "認証処理を開始できません", 503)
		return
	}
	c.Username = u.Username
	c.Generation = u.AuthGeneration
	b, err := json.Marshal(c)
	if err == nil {
		err = a.db.SavePasskeyChallenge(r.Context(), token, u.ID, purpose, string(b), time.Now())
	}
	if err != nil {
		http.Error(w, "認証処理を開始できません。再試行してください", 503)
		return
	}
	name := "awsportal_passkey_" + purpose
	http.SetCookie(w, &http.Cookie{Name: name, Value: token, Path: "/passkeys/", HttpOnly: true, Secure: env("AWSPORTAL_COOKIE_SECURE", "1") == "1", SameSite: http.SameSiteStrictMode, MaxAge: 120})
	passkeyJSON(w, options)
}

func (a *app) consumePasskey(w http.ResponseWriter, r *http.Request, purpose string) (passkeyChallenge, error) {
	var c passkeyChallenge
	name := "awsportal_passkey_" + purpose
	cookie, err := r.Cookie(name)
	if err != nil {
		return c, err
	}
	http.SetCookie(w, &http.Cookie{Name: name, Path: "/passkeys/", HttpOnly: true, Secure: env("AWSPORTAL_COOKIE_SECURE", "1") == "1", SameSite: http.SameSiteStrictMode, MaxAge: -1})
	payload, err := a.db.ConsumePasskeyChallenge(r.Context(), cookie.Value, purpose, time.Now())
	if err != nil {
		return c, err
	}
	err = json.Unmarshal([]byte(payload), &c)
	return c, err
}

func (a *app) passkeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	if !a.passkeyOrigin(w, r) {
		return
	}
	var input struct {
		Username string `json:"username"`
	}
	if readPasskeyJSON(w, r, &input) != nil {
		http.Error(w, "入力を確認してください", 400)
		return
	}
	u, reason := a.passkeyAccount(r.Context(), input.Username, false)
	if reason != "" {
		a.loginFailure(w, r, input.Username, reason)
		return
	}
	options, data, err := a.webauthn.BeginLogin(u, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		a.loginFailure(w, r, input.Username, "passkey_unavailable")
		return
	}
	a.beginPasskey(w, r, u, "login", passkeyChallenge{Session: *data}, options)
}

func (a *app) passkeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	if !a.passkeyOrigin(w, r) {
		return
	}
	c, err := a.consumePasskey(w, r, "login")
	if err != nil {
		http.Error(w, "認証の有効期限が切れました。再試行してください", 401)
		return
	}
	u, reason := a.passkeyAccount(r.Context(), c.Username, false)
	if reason != "" {
		a.loginFailure(w, r, c.Username, reason)
		return
	}
	if u.AuthGeneration != c.Generation {
		a.loginFailure(w, r, c.Username, "account_state_changed")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	parsed, err := protocol.ParseCredentialRequestResponse(r)
	if err != nil {
		a.loginFailure(w, r, c.Username, "invalid_passkey")
		return
	}
	credential, err := a.webauthn.ValidateLogin(u, c.Session, parsed)
	if err == nil {
		err = auth.CheckDevicePasskey(credential)
	}
	if err != nil {
		a.loginFailure(w, r, c.Username, "invalid_passkey")
		return
	}
	token, err := auth.NewPasskeyToken()
	if err != nil {
		http.Error(w, "認証処理を完了できません", 503)
		return
	}
	a.mu.Lock()
	err = a.db.RecordPasskeyLogin(r.Context(), u.User, credential, time.Now())
	if err == nil {
		if !a.putSessionLocked(token, session{User: u.User, Expires: time.Now().Add(8 * time.Hour), AuthenticatedAt: time.Now(), Method: "passkey"}) {
			err = fmt.Errorf("session capacity reached")
		}
	}
	a.mu.Unlock()
	if err != nil {
		a.loginFailure(w, r, c.Username, "account_state_changed")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "awsportal_session", Value: token, Path: "/", HttpOnly: true, Secure: env("AWSPORTAL_COOKIE_SECURE", "1") == "1", SameSite: http.SameSiteStrictMode, MaxAge: 28800})
	redirect := "/"
	if u.Role == "portal_admin" && u.MustChangePassword {
		redirect = "/change-password"
	}
	passkeyJSON(w, map[string]string{"redirect": redirect})
}

func (a *app) recentPasskeySession(w http.ResponseWriter, r *http.Request, next func(store.User, string)) {
	a.require(func(w http.ResponseWriter, r *http.Request) {
		cookie, _ := r.Cookie("awsportal_session")
		a.mu.Lock()
		s := a.sessions[cookie.Value]
		a.mu.Unlock()
		if s.AuthenticatedAt.IsZero() || time.Since(s.AuthenticatedAt) > 10*time.Minute {
			http.Error(w, "ログインし直してから操作してください", 403)
			return
		}
		next(r.Context().Value("user").(store.User), store.DCVTokenHash(cookie.Value))
	})(w, r)
}

func (a *app) passkeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	if !a.passkeyOrigin(w, r) {
		return
	}
	var input struct{ Username, Code, Name string }
	if readPasskeyJSON(w, r, &input) != nil || strings.TrimSpace(input.Name) == "" || len([]rune(input.Name)) > 80 {
		http.Error(w, "PC名を確認してください", 400)
		return
	}
	start := func(username, invite, sessionHash string) {
		u, reason := a.passkeyAccount(r.Context(), username, true)
		if reason != "" {
			a.loginFailure(w, r, username, reason)
			return
		}
		if invite != "" && a.db.PasskeyInvite(r.Context(), u.ID, invite, u.AuthGeneration, time.Now()) != nil {
			a.loginFailure(w, r, username, "invalid_enrollment_code")
			return
		}
		options, data, err := a.webauthn.BeginRegistration(u, webauthn.WithExclusions(webauthn.Credentials(u.keys).CredentialDescriptors()))
		if err != nil {
			http.Error(w, "登録を開始できません", 503)
			return
		}
		a.beginPasskey(w, r, u, "register", passkeyChallenge{Session: *data, Name: input.Name, InviteHash: invite, SessionHash: sessionHash}, options)
	}
	if input.Code != "" {
		start(input.Username, store.DCVTokenHash(input.Code), "")
		return
	}
	a.recentPasskeySession(w, r, func(u store.User, hash string) {
		if !a.consumeStepUp(w, r, "/passkeys/register") {
			return
		}
		start(u.Username, "", hash)
	})
}

func (a *app) passkeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	if !a.passkeyOrigin(w, r) {
		return
	}
	c, err := a.consumePasskey(w, r, "register")
	if err != nil {
		http.Error(w, "登録の有効期限が切れました。再試行してください", 401)
		return
	}
	finish := func() {
		u, reason := a.passkeyAccount(r.Context(), c.Username, true)
		if reason != "" {
			a.loginFailure(w, r, c.Username, reason)
			return
		}
		if u.AuthGeneration != c.Generation {
			http.Error(w, "登録権限が失効しました", 401)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
		parsed, err := protocol.ParseCredentialCreationResponse(r)
		if err != nil {
			http.Error(w, "パスキーを確認できません", 400)
			return
		}
		credential, err := a.webauthn.CreateCredential(u, c.Session, parsed)
		if err == nil {
			err = auth.CheckDevicePasskey(credential)
		}
		if err == nil {
			err = a.db.CompletePasskeyRegistration(r.Context(), u.User, c.Name, credential, c.InviteHash, time.Now())
		}
		if err != nil {
			a.db.Audit(r.Context(), u.Username, "passkey.register", u.Username, "deny", "verification or account state rejected")
			http.Error(w, "登録できません。同期しないPCのパスキーを使用し、コード・PC名を確認してください", 400)
			return
		}
		// Registration never creates a Portal session. Login must prove the new key.
		passkeyJSON(w, map[string]string{"redirect": "/login"})
	}
	if c.InviteHash != "" {
		finish()
		return
	}
	a.recentPasskeySession(w, r, func(u store.User, hash string) {
		if hash != c.SessionHash || u.Username != c.Username {
			http.Error(w, "登録権限が一致しません", 403)
			return
		}
		finish()
	})
}

func (a *app) passkeyEnrollPage(w http.ResponseWriter, r *http.Request) {
	a.renderPage(w, r, "passkey-enroll.html", nil)
}
func (a *app) passkeyPage(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value("user").(store.User)
	keys, err := a.db.Passkeys(r.Context(), u.ID)
	if err != nil {
		http.Error(w, "一覧を取得できません", 503)
		return
	}
	a.renderPage(w, r, "passkeys.html", map[string]any{"User": u, "Keys": keys, "CSRF": passkeyCSRF(r)})
}
func (a *app) passkeyDelete(w http.ResponseWriter, r *http.Request) {
	if !a.passkeyForm(w, r) {
		return
	}
	a.recentPasskeySession(w, r, func(u store.User, _ string) {
		if !a.consumeStepUp(w, r, r.URL.Path) {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err == nil {
			err = a.db.DeletePasskey(r.Context(), u, id)
		}
		if err != nil {
			http.Error(w, "削除できません。最後のパスキーのリセットは管理者へ依頼してください", 400)
			return
		}
		http.Redirect(w, r, "/login", 303)
	})
}

func (a *app) adminPasskeyInvite(w http.ResponseWriter, r *http.Request) {
	if !a.passkeyForm(w, r) {
		return
	}
	a.recentPasskeySession(w, r, func(admin store.User, _ string) {
		if admin.Role != "portal_admin" {
			http.Error(w, "管理者のみ", 403)
			return
		}
		if !a.consumeStepUp(w, r, r.URL.Path) {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if r.ParseForm() != nil {
			http.Error(w, "入力を確認してください", 400)
			return
		}
		token, err := auth.NewPasskeyToken()
		username := r.PostFormValue("username")
		reset := r.URL.Path == "/admin/passkeys/reset"
		if err == nil {
			err = a.db.IssuePasskeyInvite(r.Context(), admin, username, r.PostFormValue("reason"), token, reset, time.Now())
		}
		if err != nil {
			a.db.Audit(r.Context(), admin.Username, "passkey.invite", username, "deny", "account state or reason rejected")
			http.Error(w, "発行できません。対象と理由を確認してください", 400)
			return
		}
		a.renderPage(w, r, "passkey-code.html", map[string]any{"Username": username, "Code": token})
	})
}
