package main

import (
	"crypto/hmac"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/sptree-m/awsportal/internal/auth"
	"github.com/sptree-m/awsportal/internal/store"
)

// Only these local operations can receive a single-use proof. Never accept a
// caller-provided return URL, account name, or credential as proof of ownership.
func stepUpDestination(action string) (string, string, bool) {
	destinations := map[string][2]string{
		"/passkeys/register":     {"パスキーの追加", "/passkeys"},
		"/change-password":       {"パスワードの変更", "/change-password"},
		"/mfa/add":               {"TOTP端末の追加", "/mfa"},
		"/admin/passkeys/invite": {"登録コードの発行", "/admin/users"},
		"/admin/passkeys/reset":  {"パスキーのリセット", "/admin/users"},
		"/admin/users":           {"ユーザーの作成", "/admin/users"},
		"/admin/instances":       {"インスタンス管理", "/admin/instances"},
		"/admin/site":            {"表示設定の変更", "/admin/site"},
		"/admin/egress":          {"外向き通信の変更", "/admin/egress"},
		"/admin/proxy":           {"Proxy管理", "/admin/proxy"},
		"/admin/environments":    {"環境管理", "/admin/environments"},
		"/admin/two-stage":       {"取り込み管理", "/admin/two-stage"},
		"/mirrors":               {"Gitミラー管理", "/mirrors"},
	}
	if d, ok := destinations[action]; ok {
		return d[0], d[1], true
	}
	parts := strings.Split(action, "/")
	if len(parts) == 4 && (parts[1] == "passkeys" || parts[1] == "mfa") {
		id, err := strconv.ParseInt(parts[2], 10, 64)
		if err == nil && id > 0 && strconv.FormatInt(id, 10) == parts[2] {
			if parts[1] == "passkeys" && parts[3] == "delete" {
				return "パスキーの削除", "/passkeys", true
			}
			if parts[1] == "mfa" && (parts[3] == "verify" || parts[3] == "delete") {
				return "TOTP端末の確認・削除", "/mfa?setup=" + parts[2], true
			}
		}
	}
	if len(parts) == 5 && parts[1] == "admin" && parts[2] == "users" && parts[3] != "" {
		switch parts[4] {
		case "reactivate", "disable", "unlock":
			return "ユーザー状態の変更", "/admin/users", true
		}
	}
	return "", "", false
}

func stepUpAllowed(u store.User, action string) bool {
	_, _, ok := stepUpDestination(action)
	if !ok {
		return false
	}
	if u.MustChangePassword {
		return action == "/change-password"
	}
	return u.Role == "portal_admin" || action == "/passkeys/register" || strings.HasPrefix(action, "/passkeys/")
}

func (a *app) issueStepUp(w http.ResponseWriter, r *http.Request, u store.User, action string) {
	token, err := auth.NewPasskeyToken()
	cookie, _ := r.Cookie("awsportal_session")
	if err != nil || cookie == nil {
		http.Error(w, "再認証を完了できません", 503)
		return
	}
	a.mu.Lock()
	s, ok := a.sessions[cookie.Value]
	if ok && !sessionExpired(s, time.Now()) && s.User.AuthGeneration == u.AuthGeneration {
		s.AuthenticatedAt = time.Now()
		s.LastSeen = time.Now()
		s.StepUpHash = store.DCVTokenHash(token)
		s.StepUpAction = action
		s.StepUpExpires = time.Now().Add(2 * time.Minute)
		a.sessions[cookie.Value] = s
	} else {
		ok = false
	}
	a.mu.Unlock()
	if !ok {
		http.Error(w, "セッションが失効しました", 401)
		return
	}
	_, destination, _ := stepUpDestination(action)
	passkeyJSON(w, map[string]string{"stepup": token, "action": action, "redirect": destination})
}

func (a *app) consumeStepUp(w http.ResponseWriter, r *http.Request, action string) bool {
	token := r.Header.Get("X-Step-Up")
	if token == "" {
		token = r.PostFormValue("stepup")
	}
	cookie, _ := r.Cookie("awsportal_session")
	valid := false
	if cookie != nil && token != "" {
		a.mu.Lock()
		s, ok := a.sessions[cookie.Value]
		if ok && !sessionExpired(s, time.Now()) && s.StepUpAction == action && time.Now().Before(s.StepUpExpires) && hmac.Equal([]byte(s.StepUpHash), []byte(store.DCVTokenHash(token))) {
			valid = true
			s.StepUpHash = ""
			s.StepUpAction = ""
			s.StepUpExpires = time.Time{}
			a.sessions[cookie.Value] = s
		}
		a.mu.Unlock()
	}
	if valid {
		return true
	}
	destination := "/reauthenticate?action=" + url.QueryEscape(action)
	w.Header().Set("X-Reauthenticate", destination)
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", destination)
		http.Error(w, "既存の認証要素で確認してください", 401)
	} else if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "既存の認証要素で確認してください", 403)
	} else {
		http.Redirect(w, r, destination, 303)
	}
	return false
}

func (a *app) reauthenticationPage(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value("user").(store.User)
	action := r.URL.Query().Get("action")
	if !stepUpAllowed(u, action) {
		http.Error(w, "操作を確認してください", 400)
		return
	}
	label, _, _ := stepUpDestination(action)
	a.renderPage(w, r, "reauthenticate.html", map[string]any{"User": u, "Action": action, "Operation": label})
}

func (a *app) reauthenticatePassword(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value("user").(store.User)
	if r.ParseForm() != nil || u.Role != "portal_admin" || !stepUpAllowed(u, r.PostFormValue("action")) {
		http.Error(w, "操作を確認してください", 403)
		return
	}
	if !auth.CheckPassword(u.PasswordHash, r.PostFormValue("password")) {
		a.loginFailure(w, r, u.Username, "invalid_password")
		return
	}
	valid, err := a.db.RecordPasswordReauthentication(r.Context(), u, r.PostFormValue("totp"), time.Now())
	if err != nil {
		http.Error(w, "再認証を完了できません", 503)
		return
	}
	if !valid {
		a.loginFailure(w, r, u.Username, "invalid_mfa")
		return
	}
	a.issueStepUp(w, r, u, r.PostFormValue("action"))
}

func (a *app) reauthenticatePasskeyBegin(w http.ResponseWriter, r *http.Request) {
	if !a.passkeyOrigin(w, r) {
		return
	}
	var input struct{ Action string }
	u := r.Context().Value("user").(store.User)
	if readPasskeyJSON(w, r, &input) != nil || !stepUpAllowed(u, input.Action) {
		http.Error(w, "操作を確認してください", 400)
		return
	}
	user, reason := a.passkeyAccount(r.Context(), u.Username, false)
	if reason != "" {
		http.Error(w, "アカウントを確認できません", 403)
		return
	}
	options, data, err := a.webauthn.BeginLogin(user, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		http.Error(w, "登録済みパスキーで確認してください", 400)
		return
	}
	cookie, _ := r.Cookie("awsportal_session")
	a.beginPasskey(w, r, user, "reauth", passkeyChallenge{Session: *data, SessionHash: store.DCVTokenHash(cookie.Value), Action: input.Action}, options)
}

func (a *app) reauthenticatePasskeyFinish(w http.ResponseWriter, r *http.Request) {
	if !a.passkeyOrigin(w, r) {
		return
	}
	c, err := a.consumePasskey(w, r, "reauth")
	if err != nil {
		http.Error(w, "再認証の有効期限が切れました", 401)
		return
	}
	u := r.Context().Value("user").(store.User)
	cookie, _ := r.Cookie("awsportal_session")
	if c.Username != u.Username || c.Generation != u.AuthGeneration || c.SessionHash != store.DCVTokenHash(cookie.Value) || !stepUpAllowed(u, c.Action) {
		http.Error(w, "再認証の権限が一致しません", 403)
		return
	}
	user, reason := a.passkeyAccount(r.Context(), u.Username, false)
	if reason != "" {
		http.Error(w, "アカウントを確認できません", 403)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	parsed, err := protocol.ParseCredentialRequestResponse(r)
	var credential *webauthn.Credential
	if err == nil {
		credential, err = a.webauthn.ValidateLogin(user, c.Session, parsed)
	}
	if err == nil {
		err = auth.CheckDevicePasskey(credential)
	}
	if err != nil {
		a.loginFailure(w, r, u.Username, "invalid_passkey")
		return
	}
	if err = a.db.RecordPasskeyReauthentication(r.Context(), u, credential, time.Now()); err != nil {
		http.Error(w, "再認証を完了できません", 503)
		return
	}
	a.issueStepUp(w, r, u, c.Action)
}
