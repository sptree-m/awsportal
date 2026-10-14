package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sptree-m/awsportal/internal/auth"
	"github.com/sptree-m/awsportal/internal/store"
)

func securityLogin(a *app, username, password, code string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/login", strings.NewReader(url.Values{"username": {username}, "password": {password}, "totp": {code}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("User-Agent", "security-test-browser")
	r.Header.Set("X-Forwarded-For", "198.51.100.9")
	r.RemoteAddr = "192.0.2.3:45678"
	w := httptest.NewRecorder()
	a.login(w, r)
	return w
}

func TestLoginDelayRejectsCorrectCredentialsAndLogsDetails(t *testing.T) {
	a, _ := newHandlerTestApp(t)
	ctx := context.Background()
	hash, err := auth.HashPassword("Good-Password-42")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.db.CreateUser(ctx, "alice", hash, "portal_admin", "JBSWY3DPEHPK3PXP"); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		w := securityLogin(a, "alice", "Sensitive-Wrong-Password", "123456")
		want := http.StatusUnauthorized
		if i == 4 {
			want = http.StatusTooManyRequests
		}
		if w.Code != want || len(w.Result().Cookies()) != 0 {
			t.Fatalf("attempt %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	w := securityLogin(a, "alice", "Good-Password-42", validSecurityTOTP(t))
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" || len(a.sessions) != 0 {
		t.Fatalf("correct credentials bypassed cooldown: %d", w.Code)
	}
	u, _ := a.db.UserByName(ctx, "alice")
	if u.FailedLoginAttempts != 4 {
		t.Fatalf("throttled request increased counter: %+v", u)
	}
	entries, _ := a.db.AuditEntries(ctx, 200)
	for _, e := range entries {
		if strings.Contains(e.Detail, "Sensitive-Wrong-Password") || strings.Contains(e.Detail, "123456") {
			t.Fatal("credentials leaked to audit")
		}
		var d store.LoginAuditDetail
		if err = json.Unmarshal([]byte(e.Detail), &d); err != nil || d.RemoteIP != "192.0.2.3" || d.ForwardedFor != "198.51.100.9" || d.UserAgent != "security-test-browser" || e.At == "" {
			t.Fatalf("missing audit metadata: %+v %+v %v", e, d, err)
		}
	}
	// Advance the persisted deadline to simulate the full wait without sleeping.
	if _, err = a.db.DB.ExecContext(ctx, `UPDATE users SET login_retry_at=? WHERE id=?`, time.Now().Unix(), u.ID); err != nil {
		t.Fatal(err)
	}
	w = securityLogin(a, "alice", "Good-Password-42", validSecurityTOTP(t))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("login after cooldown failed: %d %s", w.Code, w.Body.String())
	}
	u, _ = a.db.UserByName(ctx, "alice")
	if u.FailedLoginAttempts != 0 || u.LoginRetryAt != 0 {
		t.Fatalf("successful login did not reset failures: %+v", u)
	}
}

func TestFinalLockRevokesSessionsAndAdminUnlocks(t *testing.T) {
	a, _ := newHandlerTestApp(t)
	ctx := context.Background()
	hash, _ := auth.HashPassword("Good-Password-42")
	for _, pair := range [][2]string{{"alice", "portal_admin"}, {"admin", "portal_admin"}, {"group", "group_admin"}} {
		if err := a.db.CreateUser(ctx, pair[0], hash, pair[1], "JBSWY3DPEHPK3PXP"); err != nil {
			t.Fatal(err)
		}
	}
	u, _ := a.db.UserByName(ctx, "alice")
	old := requestAs(a, u, "GET", "/", nil)
	if _, err := a.db.DB.ExecContext(ctx, `UPDATE users SET failed_login_attempts=9 WHERE id=?`, u.ID); err != nil {
		t.Fatal(err)
	}
	w := securityLogin(a, "alice", "bad", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("final failure: %d", w.Code)
	}
	if len(a.sessions) != 0 {
		t.Fatal("existing sessions retained on final lock")
	}
	u, _ = a.db.UserByName(ctx, "alice")
	if u.LockedAt == 0 || u.FailedLoginAttempts != 10 {
		t.Fatalf("not permanently locked: %+v", u)
	}
	if w = securityLogin(a, "alice", "Good-Password-42", validSecurityTOTP(t)); w.Code != http.StatusUnauthorized {
		t.Fatal("locked account authenticated")
	}
	group, _ := a.db.UserByName(ctx, "group")
	admin, _ := a.db.UserByName(ctx, "admin")
	for _, actor := range []store.User{u, group} {
		r := requestAs(a, actor, "POST", "/admin/users/alice/unlock", strings.NewReader("reason=verified"))
		r.SetPathValue("username", "alice")
		w = httptest.NewRecorder()
		a.require(a.adminUnlockUser)(w, r)
		if w.Code == http.StatusOK || w.Code == http.StatusSeeOther && w.Header().Get("Location") != "/login" {
			t.Fatalf("non-admin unlocked account: %d", w.Code)
		}
	}
	r := requestAs(a, admin, "POST", "/admin/users/alice/unlock", strings.NewReader(url.Values{"reason": {"本人確認済み"}}.Encode()))
	r.SetPathValue("username", "alice")
	r.Header.Set("HX-Request", "true")
	w = httptest.NewRecorder()
	a.require(a.adminUnlockUser)(w, r)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "ロック解除</button>") || !strings.Contains(w.Body.String(), `data-user="alice"`) {
		t.Fatalf("invalid unlocked row: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	a.require(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })(w, old)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
		t.Fatal("old session revived after unlock")
	}
	if w = securityLogin(a, "alice", "Good-Password-42", validSecurityTOTP(t)); w.Code != http.StatusSeeOther {
		t.Fatalf("fresh login after unlock failed: %d", w.Code)
	}
}

func TestLoginMFAErrorsAndInactiveAccountsAreAudited(t *testing.T) {
	a, _ := newHandlerTestApp(t)
	ctx := context.Background()
	hash, _ := auth.HashPassword("Good-Password-42")
	if err := a.db.CreateUser(ctx, "admin", hash, "portal_admin", "JBSWY3DPEHPK3PXP"); err != nil {
		t.Fatal(err)
	}
	if err := a.db.CreateUser(ctx, "disabled", hash, "user", ""); err != nil {
		t.Fatal(err)
	}
	if err := a.db.SetUserEnabled(ctx, "disabled", false); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ user, reason string }{{"missing", "unknown_user"}, {"disabled", "account_disabled"}, {"admin", "invalid_mfa"}} {
		w := securityLogin(a, tc.user, "Good-Password-42", "not-a-code")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s returned %d", tc.user, w.Code)
		}
		entries, _ := a.db.AuditEntries(ctx, 200)
		var d store.LoginAuditDetail
		if err := json.Unmarshal([]byte(entries[0].Detail), &d); err != nil || d.Reason != tc.reason {
			t.Fatalf("reason missing: %+v %v", d, err)
		}
	}
	u, _ := a.db.UserByName(ctx, "admin")
	if u.FailedLoginAttempts != 1 {
		t.Fatal("MFA failure was not counted")
	}
	if _, err := a.db.DB.ExecContext(ctx, `DROP TABLE mfa_devices`); err != nil {
		t.Fatal(err)
	}
	securityLogin(a, "admin", "Good-Password-42", "000000")
	u, _ = a.db.UserByName(ctx, "admin")
	if u.FailedLoginAttempts != 1 {
		t.Fatal("MFA storage error counted as credential failure")
	}
	entries, _ := a.db.AuditEntries(ctx, 200)
	var d store.LoginAuditDetail
	if err := json.Unmarshal([]byte(entries[0].Detail), &d); err != nil || d.Reason != "authentication_error" {
		t.Fatalf("storage error misreported: %+v %v", d, err)
	}
}

func validSecurityTOTP(t *testing.T) string {
	t.Helper()
	code, err := auth.TOTPAt("JBSWY3DPEHPK3PXP", time.Now(), 6)
	if err != nil {
		t.Fatal(err)
	}
	return code
}
