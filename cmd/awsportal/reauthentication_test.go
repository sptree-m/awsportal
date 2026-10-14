package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sptree-m/awsportal/internal/auth"
	"github.com/sptree-m/awsportal/internal/store"
)

func stepUpTestCookie(a *app, u store.User) *http.Cookie {
	r := requestAs(a, u, "GET", testPasskeyOrigin+"/passkeys", nil)
	return r.Cookies()[0]
}
func performPasskeyStepUp(t *testing.T, a *app, cookie *http.Cookie, k testAuthenticator, action string, count uint32) string {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"action": action})
	w := passkeyRequest(a, a.handler().ServeHTTP, "/passkeys/reauth/begin", b, cookie)
	if w.Code != 200 {
		t.Fatalf("reauth begin: %d %s", w.Code, w.Body.String())
	}
	var options struct{ PublicKey struct{ Challenge string } }
	if err := json.Unmarshal(w.Body.Bytes(), &options); err != nil {
		t.Fatal(err)
	}
	w = passkeyRequest(a, a.handler().ServeHTTP, "/passkeys/reauth/finish", k.response(t, options.PublicKey.Challenge, testPasskeyOrigin, "portal.example.com", false, 5, count), cookie, w.Result().Cookies()[0])
	if w.Code != 200 {
		t.Fatalf("reauth finish: %d %s", w.Code, w.Body.String())
	}
	var result struct{ Stepup string }
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Stepup == "" {
		t.Fatal("missing proof")
	}
	return result.Stepup
}

func TestFreshAdminCookieCannotRegisterNewPasskey(t *testing.T) {
	a := testPasskeyApp(t)
	u := createPasskeyUser(t, a, "portal_admin")
	cookie := stepUpTestCookie(a, u)
	b, _ := json.Marshal(map[string]string{"name": "Unauthorized PC"})
	w := passkeyRequest(a, a.handler().ServeHTTP, "/passkeys/register/begin", b, cookie)
	if w.Code != 403 || w.Header().Get("X-Reauthenticate") == "" {
		t.Fatalf("cookie-only registration accepted: %d", w.Code)
	}
	var n int
	a.db.DB.QueryRow("SELECT COUNT(*) FROM passkeys").Scan(&n)
	if n != 0 {
		t.Fatal("new key was registered")
	}
}

func TestPasskeyStepUpIsPurposeSessionBoundAndSingleUse(t *testing.T) {
	a := testPasskeyApp(t)
	u := createPasskeyUser(t, a, "portal_admin")
	k := newTestAuthenticator(t)
	enrollTestPasskey(t, a, u, k)
	cookie := stepUpTestCookie(a, u)
	token := performPasskeyStepUp(t, a, cookie, k, "/passkeys/register", 1)
	request := func(c *http.Cookie, action string) *http.Request {
		r := httptest.NewRequest("POST", testPasskeyOrigin+action, nil)
		r.AddCookie(c)
		r.Header.Set("X-Step-Up", token)
		return r
	}
	// A different operation cannot use or consume this registration proof.
	if a.consumeStepUp(httptest.NewRecorder(), request(cookie, "/change-password"), "/change-password") {
		t.Fatal("cross-operation proof accepted")
	}
	a.sessions["other"] = session{User: u, Expires: time.Now().Add(time.Hour), AuthenticatedAt: time.Now(), LastSeen: time.Now()}
	if a.consumeStepUp(httptest.NewRecorder(), request(&http.Cookie{Name: "awsportal_session", Value: "other"}, "/passkeys/register"), "/passkeys/register") {
		t.Fatal("cross-session proof accepted")
	}
	// Simultaneous consumption succeeds exactly once.
	var success atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if a.consumeStepUp(httptest.NewRecorder(), request(cookie, "/passkeys/register"), "/passkeys/register") {
				success.Add(1)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 1 {
		t.Fatalf("proof uses=%d", success.Load())
	}
}

func TestReauthenticatedPasskeyCanRegisterAdditionalKey(t *testing.T) {
	a := testPasskeyApp(t)
	u := createPasskeyUser(t, a, "user")
	old := newTestAuthenticator(t)
	enrollTestPasskey(t, a, u, old)
	cookie := stepUpTestCookie(a, u)
	proof := performPasskeyStepUp(t, a, cookie, old, "/passkeys/register", 1)
	b, _ := json.Marshal(map[string]string{"name": "New PC"})
	r := httptest.NewRequest("POST", testPasskeyOrigin+"/passkeys/register/begin", strings.NewReader(string(b)))
	r.AddCookie(cookie)
	r.Header.Set("Origin", testPasskeyOrigin)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", passkeyCSRF(r))
	r.Header.Set("X-Step-Up", proof)
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("registration begin=%d %s", w.Code, w.Body.String())
	}
	var options struct{ PublicKey struct{ Challenge string } }
	json.Unmarshal(w.Body.Bytes(), &options)
	key := newTestAuthenticator(t)
	w = passkeyRequest(a, a.handler().ServeHTTP, "/passkeys/register/finish", key.response(t, options.PublicKey.Challenge, testPasskeyOrigin, "portal.example.com", true, 5, 0), cookie, w.Result().Cookies()[0])
	if w.Code != 200 {
		t.Fatalf("registration finish=%d %s", w.Code, w.Body.String())
	}
	keys, err := a.db.Passkeys(context.Background(), u.ID)
	if err != nil || len(keys) != 2 {
		t.Fatal("additional key missing")
	}
}

func TestPasswordStepUpConsumesTOTPAndExpiredProofIsRejected(t *testing.T) {
	a := testPasskeyApp(t)
	u := createPasskeyUser(t, a, "portal_admin")
	cookie := stepUpTestCookie(a, u)
	code, _ := auth.TOTPAt(u.TOTPSecret, time.Now(), 6)
	send := func() *httptest.ResponseRecorder {
		form := url.Values{"action": {"/change-password"}, "password": {"Correct-Password-42"}, "totp": {code}}
		r := httptest.NewRequest("POST", testPasskeyOrigin+"/reauthenticate", strings.NewReader(form.Encode()))
		r.AddCookie(cookie)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("X-CSRF-Token", passkeyCSRF(r))
		w := httptest.NewRecorder()
		a.handler().ServeHTTP(w, r)
		return w
	}
	w := send()
	if w.Code != 200 {
		t.Fatalf("password step-up=%d %s", w.Code, w.Body.String())
	}
	var result struct{ Stepup string }
	json.Unmarshal(w.Body.Bytes(), &result)
	if w = send(); w.Code != 401 {
		t.Fatalf("OTP replay=%d", w.Code)
	}
	s := a.sessions[cookie.Value]
	s.StepUpExpires = time.Now().Add(-time.Second)
	a.sessions[cookie.Value] = s
	r := httptest.NewRequest("POST", testPasskeyOrigin+"/change-password", nil)
	r.AddCookie(cookie)
	r.Header.Set("X-Step-Up", result.Stepup)
	if a.consumeStepUp(httptest.NewRecorder(), r, "/change-password") {
		t.Fatal("expired proof accepted")
	}
}

func TestMirrorAdministrationNeedsProofButUserSyncDoesNot(t *testing.T) {
	a := testPasskeyApp(t)
	u := createPasskeyUser(t, a, "portal_admin")
	form := url.Values{"operation": {"issue"}, "user_id": {strconv.FormatInt(u.ID, 10)}, "label": {"Unsafe"}, "days": {"90"}}
	r := requestAs(a, u, "POST", testPasskeyOrigin+"/mirrors", strings.NewReader(form.Encode()))
	r.Header.Del("X-Step-Up")
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, r)
	tokens, err := a.db.MirrorTokens(context.Background())
	if err != nil || len(tokens) != 0 || w.Header().Get("X-Reauthenticate") == "" {
		t.Fatal("cookie-only token issue accepted")
	}
	r = requestAs(a, u, "POST", testPasskeyOrigin+"/mirrors", strings.NewReader(form.Encode()))
	s := a.sessions["test-session-"+u.Username]
	s.AuthenticatedAt = time.Now().Add(-time.Hour)
	a.sessions["test-session-"+u.Username] = s
	w = httptest.NewRecorder()
	a.handler().ServeHTTP(w, r)
	tokens, _ = a.db.MirrorTokens(context.Background())
	if len(tokens) != 0 {
		t.Fatal("old session issued a token")
	}
}

func TestAuditFailureBlocksPasswordMutationAtProductionRoute(t *testing.T) {
	a := testPasskeyApp(t)
	u := createPasskeyUser(t, a, "portal_admin")
	if _, err := a.db.DB.Exec("CREATE TRIGGER reject_audit BEFORE INSERT ON audit_log BEGIN SELECT RAISE(FAIL,'audit unavailable'); END;"); err != nil {
		t.Fatal(err)
	}
	r := requestAs(a, u, "POST", testPasskeyOrigin+"/change-password", strings.NewReader("password=Changed-Password-123&confirm=Changed-Password-123"))
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, r)
	fresh, _ := a.db.UserByName(context.Background(), u.Username)
	if w.Code != 503 || !auth.CheckPassword(fresh.PasswordHash, "Correct-Password-42") {
		t.Fatalf("unaudited mutation: status=%d", w.Code)
	}
}

func TestInterruptedMutationRetainsPendingIntent(t *testing.T) {
	a := testPasskeyApp(t)
	u := createPasskeyUser(t, a, "portal_admin")
	r := requestAs(a, u, "POST", testPasskeyOrigin+"/test-interrupted", strings.NewReader(""))
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected handler panic")
			}
		}()
		a.require(func(http.ResponseWriter, *http.Request) { panic("test interruption") })(httptest.NewRecorder(), r)
	}()
	var pending int
	if err := a.db.DB.QueryRow("SELECT COUNT(*) FROM audit_operations WHERE completed IS NULL").Scan(&pending); err != nil || pending != 1 {
		t.Fatal("interrupted effect was marked complete")
	}
}
