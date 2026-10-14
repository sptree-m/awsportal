package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/sptree-m/awsportal/internal/auth"
	"github.com/sptree-m/awsportal/internal/store"
)

const testPasskeyOrigin = "https://portal.example.com"

type testAuthenticator struct {
	key           *ecdsa.PrivateKey
	id, publicKey []byte
}

func newTestAuthenticator(t *testing.T) testAuthenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 32)
	rand.Read(id)
	x, y := make([]byte, 32), make([]byte, 32)
	key.X.FillBytes(x)
	key.Y.FillBytes(y)
	publicKey, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	if err != nil {
		t.Fatal(err)
	}
	return testAuthenticator{key, id, publicKey}
}
func testB64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
func (k testAuthenticator) response(t *testing.T, challenge, origin, rp string, register bool, flags byte, count uint32) []byte {
	t.Helper()
	kind := "webauthn.get"
	if register {
		kind = "webauthn.create"
		flags |= 0x40
	}
	client, _ := json.Marshal(map[string]any{"type": kind, "challenge": challenge, "origin": origin, "crossOrigin": false})
	hash := sha256.Sum256([]byte(rp))
	data := append(hash[:], flags)
	data = binary.BigEndian.AppendUint32(data, count)
	response := map[string]any{"clientDataJSON": testB64(client)}
	if register {
		data = append(data, make([]byte, 16)...)
		data = binary.BigEndian.AppendUint16(data, uint16(len(k.id)))
		data = append(data, k.id...)
		data = append(data, k.publicKey...)
		att, err := cbor.Marshal(map[string]any{"fmt": "none", "authData": data, "attStmt": map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		response["attestationObject"] = testB64(att)
		response["transports"] = []string{"internal"}
	} else {
		ch := sha256.Sum256(client)
		digest := sha256.Sum256(append(data, ch[:]...))
		signature, err := ecdsa.SignASN1(rand.Reader, k.key, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		response["authenticatorData"] = testB64(data)
		response["signature"] = testB64(signature)
	}
	b, _ := json.Marshal(map[string]any{"id": testB64(k.id), "rawId": testB64(k.id), "type": "public-key", "authenticatorAttachment": "platform", "response": response, "clientExtensionResults": map[string]any{}})
	return b
}
func testPasskeyApp(t *testing.T) *app {
	t.Helper()
	a, _ := newHandlerTestApp(t)
	var err error
	a.webauthn, err = auth.NewWebAuthn(testPasskeyOrigin)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func passkeyRequest(a *app, handler http.HandlerFunc, path string, body []byte, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", testPasskeyOrigin+path, bytes.NewReader(body))
	r.Header.Set("Origin", testPasskeyOrigin)
	r.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	r.Header.Set("X-CSRF-Token", passkeyCSRF(r))
	if strings.HasPrefix(path, "/admin/") || strings.HasSuffix(path, "/delete") {
		authorizeTestStepUp(a, r, path)
	}
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}
func beginTestPasskey(t *testing.T, a *app, path string, input any) (string, *http.Cookie) {
	t.Helper()
	body, _ := json.Marshal(input)
	handler := a.passkeyLoginBegin
	if strings.Contains(path, "register") {
		handler = a.passkeyRegisterBegin
	}
	w := passkeyRequest(a, handler, path, body)
	if w.Code != 200 {
		t.Fatalf("begin %s: %d %s", path, w.Code, w.Body.String())
	}
	var options struct{ PublicKey struct{ Challenge string } }
	if err := json.Unmarshal(w.Body.Bytes(), &options); err != nil {
		t.Fatal(err)
	}
	if options.PublicKey.Challenge == "" {
		t.Fatal("empty challenge")
	}
	return options.PublicKey.Challenge, w.Result().Cookies()[0]
}
func createPasskeyUser(t *testing.T, a *app, role string) store.User {
	t.Helper()
	ctx := context.Background()
	hash, err := auth.HashPassword("Correct-Password-42")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.db.CreateUser(ctx, role, hash, role, "JBSWY3DPEHPK3PXP"); err != nil {
		t.Fatal(err)
	}
	u, err := a.db.UserByName(ctx, role)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
func enrollTestPasskey(t *testing.T, a *app, u store.User, k testAuthenticator) {
	t.Helper()
	code, err := auth.NewPasskeyToken()
	if err == nil {
		err = a.db.IssuePasskeyInviteCLI(context.Background(), u.Username, "initial registration", code, false, time.Now())
	}
	if err != nil {
		t.Fatal(err)
	}
	challenge, cookie := beginTestPasskey(t, a, "/passkeys/register/begin", map[string]string{"username": u.Username, "code": code, "name": "PC-01"})
	w := passkeyRequest(a, a.passkeyRegisterFinish, "/passkeys/register/finish", k.response(t, challenge, testPasskeyOrigin, "portal.example.com", true, 5, 0), cookie)
	if w.Code != 200 {
		t.Fatalf("registration: %d %s", w.Code, w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == "awsportal_session" {
			t.Fatal("registration created a Portal session")
		}
	}
}

func TestPasskeyRolePolicyAndLogin(t *testing.T) {
	for _, role := range []string{"user", "group_admin", "portal_admin"} {
		t.Run(role, func(t *testing.T) {
			a := testPasskeyApp(t)
			u := createPasskeyUser(t, a, role)
			k := newTestAuthenticator(t)
			enrollTestPasskey(t, a, u, k)
			code, _ := auth.TOTPAt(u.TOTPSecret, time.Now(), 6)
			legacy := securityLogin(a, u.Username, "Correct-Password-42", code)
			if role == "portal_admin" {
				if legacy.Code != 303 {
					t.Fatalf("admin legacy path: %d", legacy.Code)
				}
			} else if legacy.Code != 401 || len(legacy.Result().Cookies()) != 0 {
				t.Fatal("password+TOTP authenticated non-admin")
			}
			challenge, cookie := beginTestPasskey(t, a, "/passkeys/login/begin", map[string]string{"username": u.Username})
			body := k.response(t, challenge, testPasskeyOrigin, "portal.example.com", false, 5, 1)
			w := passkeyRequest(a, a.passkeyLoginFinish, "/passkeys/login/finish", body, cookie)
			if w.Code != 200 {
				t.Fatalf("passkey login: %d %s", w.Code, w.Body.String())
			}
			var sessionCookie *http.Cookie
			for _, c := range w.Result().Cookies() {
				if c.Name == "awsportal_session" {
					sessionCookie = c
				}
			}
			if sessionCookie == nil || !sessionCookie.Secure || !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteStrictMode {
				t.Fatal("missing protected session")
			}
			r := httptest.NewRequest("GET", "/", nil)
			r.AddCookie(sessionCookie)
			ok := httptest.NewRecorder()
			a.require(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })(ok, r)
			if ok.Code != 204 {
				t.Fatal("new session denied")
			}
			w = passkeyRequest(a, a.passkeyLoginFinish, "/passkeys/login/finish", body, cookie)
			if w.Code == 200 {
				t.Fatal("challenge replay accepted")
			}
		})
	}
}

func TestPasskeyRegistrationRequiresUVAndRejectsCloudSync(t *testing.T) {
	for _, flags := range []byte{1, 9, 13, 29} {
		t.Run(string(rune('a'+flags)), func(t *testing.T) {
			a := testPasskeyApp(t)
			u := createPasskeyUser(t, a, "user")
			k := newTestAuthenticator(t)
			code, _ := auth.NewPasskeyToken()
			if err := a.db.IssuePasskeyInviteCLI(context.Background(), u.Username, "verified", code, false, time.Now()); err != nil {
				t.Fatal(err)
			}
			challenge, cookie := beginTestPasskey(t, a, "/passkeys/register/begin", map[string]string{"username": u.Username, "code": code, "name": "PC"})
			w := passkeyRequest(a, a.passkeyRegisterFinish, "/passkeys/register/finish", k.response(t, challenge, testPasskeyOrigin, "portal.example.com", true, flags, 0), cookie)
			if w.Code == 200 {
				t.Fatalf("flags %x accepted", flags)
			}
			keys, _ := a.db.Passkeys(context.Background(), u.ID)
			if len(keys) != 0 {
				t.Fatal("rejected key persisted")
			}
			if err := a.db.PasskeyInvite(context.Background(), u.ID, store.DCVTokenHash(code), u.AuthGeneration, time.Now()); err != nil {
				t.Fatal("failed registration consumed invite")
			}
		})
	}
}

func TestPasskeyLoginRejectsInvalidProofs(t *testing.T) {
	for _, mode := range []string{"uv", "backup", "origin", "rp", "challenge", "signature", "counter"} {
		t.Run(mode, func(t *testing.T) {
			a := testPasskeyApp(t)
			u := createPasskeyUser(t, a, "user")
			k := newTestAuthenticator(t)
			enrollTestPasskey(t, a, u, k)
			flags := byte(5)
			origin, rp := testPasskeyOrigin, "portal.example.com"
			challenge, cookie := beginTestPasskey(t, a, "/passkeys/login/begin", map[string]string{"username": u.Username})
			count := uint32(1)
			switch mode {
			case "uv":
				flags = 1
			case "backup":
				flags = 13
			case "origin":
				origin = "https://evil.example.com"
			case "rp":
				rp = "evil.example.com"
			case "challenge":
				challenge = testB64([]byte("wrong-challenge"))
			case "signature":
				k.key, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			case "counter":
				keys, _ := a.db.Passkeys(context.Background(), u.ID)
				keys[0].Credential.Authenticator.SignCount = 2
				b, _ := json.Marshal(keys[0].Credential)
				a.db.DB.Exec(`UPDATE passkeys SET credential=?`, string(b))
			}
			w := passkeyRequest(a, a.passkeyLoginFinish, "/passkeys/login/finish", k.response(t, challenge, origin, rp, false, flags, count), cookie)
			if w.Code == 200 || len(a.sessions) > 0 {
				t.Fatal("invalid proof accepted")
			}
			fresh, _ := a.db.UserByName(context.Background(), u.Username)
			if fresh.FailedLoginAttempts != 1 {
				t.Fatalf("failure not counted: %d", fresh.FailedLoginAttempts)
			}
		})
	}
}

func TestPasskeyRecoveryInvalidatesOldKeysSessionsAndChallenges(t *testing.T) {
	a := testPasskeyApp(t)
	ctx := context.Background()
	u := createPasskeyUser(t, a, "user")
	k := newTestAuthenticator(t)
	enrollTestPasskey(t, a, u, k)
	challenge, cookie := beginTestPasskey(t, a, "/passkeys/login/begin", map[string]string{"username": u.Username})
	old := requestAs(a, u, "GET", "/", nil)
	code, _ := auth.NewPasskeyToken()
	admin := createPasskeyUser(t, a, "portal_admin")
	if err := a.db.IssuePasskeyInvite(ctx, admin, u.Username, "identity verified after lost PC", code, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.require(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })(w, old)
	if w.Code == 200 {
		t.Fatal("old session survived reset")
	}
	w = passkeyRequest(a, a.passkeyLoginFinish, "/passkeys/login/finish", k.response(t, challenge, testPasskeyOrigin, "portal.example.com", false, 5, 1), cookie)
	if w.Code == 200 {
		t.Fatal("outstanding challenge survived reset")
	}
	keys, _ := a.db.Passkeys(ctx, u.ID)
	if len(keys) != 0 {
		t.Fatal("old keys survived reset")
	}
	newKey := newTestAuthenticator(t)
	challenge, cookie = beginTestPasskey(t, a, "/passkeys/register/begin", map[string]string{"username": u.Username, "code": code, "name": "Replacement PC"})
	w = passkeyRequest(a, a.passkeyRegisterFinish, "/passkeys/register/finish", newKey.response(t, challenge, testPasskeyOrigin, "portal.example.com", true, 5, 0), cookie)
	if w.Code != 200 {
		t.Fatalf("recovery registration failed: %s", w.Body.String())
	}
	input, _ := json.Marshal(map[string]string{"username": u.Username, "code": code, "name": "Replay PC"})
	w = passkeyRequest(a, a.passkeyRegisterBegin, "/passkeys/register/begin", input)
	if w.Code == 200 {
		t.Fatal("single-use code replay accepted")
	}
	entries, _ := a.db.AuditEntries(ctx, 200)
	for _, e := range entries {
		if strings.Contains(e.Detail, code) || strings.Contains(e.Detail, testB64(k.id)) {
			t.Fatal("sensitive data in audit")
		}
	}
}

func TestPasskeyAccountStateExpiryAndOrigin(t *testing.T) {
	for _, state := range []string{"locked", "disabled", "expired", "throttled", "challenge_expired", "reset_during_finish"} {
		t.Run(state, func(t *testing.T) {
			a := testPasskeyApp(t)
			u := createPasskeyUser(t, a, "user")
			k := newTestAuthenticator(t)
			enrollTestPasskey(t, a, u, k)
			challenge, cookie := beginTestPasskey(t, a, "/passkeys/login/begin", map[string]string{"username": u.Username})
			switch state {
			case "locked":
				a.db.DB.Exec(`UPDATE users SET locked_at=1`)
			case "disabled":
				a.db.DB.Exec(`UPDATE users SET enabled=0`)
			case "expired":
				a.db.DB.Exec(`UPDATE users SET expires_at=1`)
			case "throttled":
				a.db.DB.Exec(`UPDATE users SET login_retry_at=?`, time.Now().Add(time.Minute).Unix())
			case "challenge_expired":
				a.db.DB.Exec(`UPDATE passkey_challenges SET expires_at=1`)
			case "reset_during_finish":
				a.db.DB.Exec(`UPDATE users SET auth_generation=auth_generation+1`)
			}
			w := passkeyRequest(a, a.passkeyLoginFinish, "/passkeys/login/finish", k.response(t, challenge, testPasskeyOrigin, "portal.example.com", false, 5, 1), cookie)
			if w.Code == 200 || len(a.sessions) > 0 {
				t.Fatal("account state bypassed")
			}
		})
	}
	a := testPasskeyApp(t)
	r := httptest.NewRequest("POST", "/passkeys/login/begin", strings.NewReader(`{"username":"user"}`))
	r.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	a.passkeyLoginBegin(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin begin accepted")
	}
}

func TestAdminPasskeyInviteFormRequiresRoleRecentLoginAndCSRF(t *testing.T) {
	a := testPasskeyApp(t)
	admin := createPasskeyUser(t, a, "portal_admin")
	user := createPasskeyUser(t, a, "user")
	for _, mode := range []string{"valid", "missing_csrf", "old_session", "user", "wrong_origin"} {
		t.Run(mode, func(t *testing.T) {
			actor := admin
			if mode == "user" {
				actor = user
			}
			r := requestAs(a, actor, "GET", "/admin/users", nil)
			cookie, _ := r.Cookie("awsportal_session")
			s := a.sessions[cookie.Value]
			s.AuthenticatedAt = time.Now()
			s.Method = "passkey"
			if mode == "old_session" {
				s.AuthenticatedAt = time.Now().Add(-11 * time.Minute)
			}
			a.sessions[cookie.Value] = s
			page := httptest.NewRecorder()
			a.require(a.adminUsers)(page, r)
			csrf := ""
			if match := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(page.Body.String()); len(match) > 1 {
				csrf = match[1]
			}
			if mode == "missing_csrf" {
				csrf = ""
			}
			form := url.Values{"username": {user.Username}, "reason": {"identity verified"}, "csrf": {csrf}}
			post := httptest.NewRequest("POST", "/admin/passkeys/invite", strings.NewReader(form.Encode()))
			post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			post.Header.Set("Origin", "null")
			if mode == "wrong_origin" {
				post.Header.Set("Origin", "https://evil.example.com")
			}
			post.AddCookie(cookie)
			authorizeTestStepUp(a, post, "/admin/passkeys/invite")
			w := httptest.NewRecorder()
			a.require(a.adminPasskeyInvite)(w, post)
			if mode == "valid" {
				if w.Code != 200 || !strings.Contains(w.Body.String(), "パスキー登録コード") {
					t.Fatalf("valid form %d %s", w.Code, w.Body.String())
				}
			} else if w.Code != 403 {
				t.Fatalf("%s accepted: %d", mode, w.Code)
			}
		})
	}
}

func TestPasskeyInitialAccountAndExpiredInvite(t *testing.T) {
	a := testPasskeyApp(t)
	ctx := context.Background()
	code, _ := auth.NewPasskeyToken()
	if err := a.db.CreatePasskeyUserCLI(ctx, "new-user", "user", code, time.Now()); err != nil {
		t.Fatal(err)
	}
	u, err := a.db.UserByName(ctx, "new-user")
	if err != nil || !u.MustChangePassword {
		t.Fatal("initial account not gated")
	}
	if a.db.PasskeyInvite(ctx, u.ID, store.DCVTokenHash(code), u.AuthGeneration, time.Now().Add(31*time.Minute)) == nil {
		t.Fatal("expired code valid")
	}
	k := newTestAuthenticator(t)
	challenge, cookie := beginTestPasskey(t, a, "/passkeys/register/begin", map[string]string{"username": u.Username, "code": code, "name": "PC"})
	w := passkeyRequest(a, a.passkeyRegisterFinish, "/passkeys/register/finish", k.response(t, challenge, testPasskeyOrigin, "portal.example.com", true, 5, 0), cookie)
	if w.Code != 200 {
		t.Fatalf("initial enrollment failed %s", w.Body.String())
	}
	u, _ = a.db.UserByName(ctx, u.Username)
	if u.MustChangePassword {
		t.Fatal("successful enrollment did not clear initial gate")
	}
	keys, _ := a.db.Passkeys(ctx, u.ID)
	newCode, _ := auth.NewPasskeyToken()
	if err = a.db.IssuePasskeyInviteCLI(ctx, u.Username, "replacement after loss", newCode, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = a.db.CompletePasskeyRegistration(ctx, u, "stale completion", &keys[0].Credential, store.DCVTokenHash(newCode), time.Now()); err == nil {
		t.Fatal("stale generation completed registration")
	}
}
