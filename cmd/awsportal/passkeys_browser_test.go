package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/sptree-m/awsportal/internal/auth"
)

func TestBrowserPasskeys(t *testing.T) {
	if os.Getenv("AWSPORTAL_BROWSER_TEST") != "1" {
		t.Skip("set AWSPORTAL_BROWSER_TEST=1 with Playwright")
	}
	t.Setenv("AWSPORTAL_COOKIE_SECURE", "0")
	a := testPasskeyApp(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.require(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/passkeys", 303) }))
	mux.Handle("/", a.handler())
	srv := httptest.NewServer(headers(mux))
	defer srv.Close()
	origin := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	var err error
	a.webauthn, err = auth.NewWebAuthn(origin)
	if err != nil {
		t.Fatal(err)
	}
	u := createPasskeyUser(t, a, "user")
	admin := createPasskeyUser(t, a, "portal_admin")
	userCode, _ := auth.NewPasskeyToken()
	adminCode, _ := auth.NewPasskeyToken()
	for _, entry := range []struct{ username, code string }{{u.Username, userCode}, {admin.Username, adminCode}} {
		if err = a.db.IssuePasskeyInviteCLI(context.Background(), entry.username, "browser enrollment", entry.code, false, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	code, _ := auth.TOTPAt(admin.TOTPSecret, time.Now(), 6)
	cmd := exec.Command("node", "../../tests/browser-passkeys.cjs")
	cmd.Env = append(os.Environ(), "AWSPORTAL_TEST_URL="+origin, "AWSPORTAL_TEST_USER_CODE="+userCode, "AWSPORTAL_TEST_ADMIN_CODE="+adminCode, "AWSPORTAL_TEST_TOTP="+code)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser passkeys: %v\n%s", err, output)
	}
	t.Log(string(output))
}
