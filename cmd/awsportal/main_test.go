package main

import (
	"context"
	"fmt"
	"github.com/sptree-m/awsportal/internal/auth"
	"github.com/sptree-m/awsportal/internal/store"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLabDebugMFABypass(t *testing.T) {
	old, had := os.LookupEnv("AWSPORTAL_LAB_DEBUG_AUTH")
	defer func() {
		if had {
			_ = os.Setenv("AWSPORTAL_LAB_DEBUG_AUTH", old)
		} else {
			_ = os.Unsetenv("AWSPORTAL_LAB_DEBUG_AUTH")
		}
	}()
	cases := []struct {
		name, flag, user string
		want             bool
	}{
		{"lab flag plus labdebug", "1", "labdebug", true},
		{"flag off", "0", "labdebug", false},
		{"other admin never bypasses", "1", "labadmin", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.Setenv("AWSPORTAL_LAB_DEBUG_AUTH", tc.flag)
			got := labDebugMFABypass(store.User{Username: tc.user, Role: "portal_admin"})
			if got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestStaticAssetsServed(t *testing.T) {
	h := staticHandler(http.StripPrefix("/static/", http.FileServer(http.FS(web))))
	cases := []struct {
		path        string
		contentType string
		prefix      string
	}{
		{"/static/web/app.css", "text/css", "@font-face"},
		{"/static/web/dashboard.js", "text/javascript", "(()=>"},
		{"/static/web/htmx.min.js", "text/javascript", "var htmx="},
		{"/static/web/fonts/rounded-mplus-1mn-regular.ttf", "font/ttf", ""},
		{"/static/web/fonts/rounded-mplus-1mn-regular.woff2", "font/woff2", ""},
		{"/static/web/fonts/rounded-mplus-1mn-bold.woff2", "font/woff2", ""},
		{"/static/web/fonts/rounded-mplus-1mn-bold.ttf", "font/ttf", ""},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("%s returned %d", tc.path, w.Code)
			}
			if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, tc.contentType) {
				t.Fatalf("%s content-type=%q want prefix %q", tc.path, got, tc.contentType)
			}
			if got := w.Header().Get("Cache-Control"); !strings.Contains(got, "max-age=86400") {
				t.Fatalf("%s cache-control=%q", tc.path, got)
			}
			if tc.prefix != "" && !strings.HasPrefix(w.Body.String(), tc.prefix) {
				t.Fatalf("%s unexpected body prefix", tc.path)
			}
			if strings.HasSuffix(tc.path, ".ttf") && w.Body.Len() < 10000 {
				t.Fatalf("%s font response too small: %d", tc.path, w.Body.Len())
			}
		})
	}
}

type fakeEC2 struct {
	states  map[string]string
	started []string
	stopped []string
}

func (f *fakeEC2) Start(_ context.Context, id string) error {
	f.started = append(f.started, id)
	f.states[id] = "pending"
	return nil
}
func (f *fakeEC2) Stop(_ context.Context, id string) error {
	f.stopped = append(f.stopped, id)
	f.states[id] = "stopping"
	return nil
}
func (f *fakeEC2) State(_ context.Context, id string) (string, error) {
	return f.states[id], nil
}
func (f *fakeEC2) States(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		out[id] = f.states[id]
	}
	return out, nil
}

func newHandlerTestApp(t *testing.T) (*app, *fakeEC2) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "portal.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ec2 := &fakeEC2{states: map[string]string{}}
	return &app{db: s, ec2: ec2, tpl: template.Must(template.ParseFS(web, "web/*.html")), sessions: map[string]session{}}, ec2
}

func requestAs(a *app, u store.User, method, target string, body *strings.Reader) *http.Request {
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, body)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	token := "test-session-" + u.Username
	a.sessions[token] = session{User: u, Expires: time.Now().Add(time.Hour)}
	r.AddCookie(&http.Cookie{Name: "awsportal_session", Value: token})
	return r
}

func TestPortalAdminLoginRequiresTOTP(t *testing.T) {
	a, _ := newHandlerTestApp(t)
	secret := "JBSWY3DPEHPK3PXP"
	hash, err := auth.HashPassword("Correct-Horse-42")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.db.CreateUser(context.Background(), "admin", hash, "portal_admin", secret); err != nil {
		t.Fatal(err)
	}

	bad := httptest.NewRecorder()
	badReq := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(url.Values{
		"username": {"admin"}, "password": {"Correct-Horse-42"},
	}.Encode()))
	badReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	a.login(bad, badReq)
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("admin login without TOTP=%d", bad.Code)
	}

	code, err := auth.TOTPAt(secret, time.Now(), 6)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(url.Values{
		"username": {"admin"}, "password": {"Correct-Horse-42"}, "totp": {code},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ok := httptest.NewRecorder()
	a.login(ok, req)
	if ok.Code != http.StatusSeeOther {
		t.Fatalf("admin login with TOTP=%d body=%s", ok.Code, ok.Body.String())
	}
}

func TestInstanceActionEnforcesAssignment(t *testing.T) {
	a, ec2 := newHandlerTestApp(t)
	ctx := context.Background()
	if err := a.db.CreateUser(ctx, "alice", "x", "user", ""); err != nil {
		t.Fatal(err)
	}
	u, _ := a.db.UserByName(ctx, "alice")
	_, err := a.db.DB.ExecContext(ctx, `INSERT INTO instances(id,instance_id,name,dcv_host) VALUES(1,'i-ok','ok','ok.local'),(2,'i-no','no','no.local'); INSERT INTO instance_users(instance_id,user_id,can_control) VALUES(1,?,1);`, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	ec2.states["i-ok"] = "stopped"
	ec2.states["i-no"] = "stopped"

	allowed := requestAs(a, u, http.MethodPost, "/instance/i-ok/start", nil)
	allowed.SetPathValue("id", "i-ok")
	allowed.SetPathValue("action", "start")
	w := httptest.NewRecorder()
	a.require(a.instanceAction)(w, allowed)
	if w.Code != http.StatusSeeOther || len(ec2.started) != 1 || ec2.started[0] != "i-ok" {
		t.Fatalf("allowed action code=%d started=%v", w.Code, ec2.started)
	}

	denied := requestAs(a, u, http.MethodPost, "/instance/i-no/start", nil)
	denied.SetPathValue("id", "i-no")
	denied.SetPathValue("action", "start")
	w = httptest.NewRecorder()
	a.require(a.instanceAction)(w, denied)
	if w.Code != http.StatusForbidden {
		t.Fatalf("unassigned instance action=%d", w.Code)
	}
	if len(ec2.started) != 1 {
		t.Fatalf("AWS action called for denied instance: %v", ec2.started)
	}
}

func TestDCVHTTPTokenIsOneTimeAndBoundToAssignedInstance(t *testing.T) {
	a, _ := newHandlerTestApp(t)
	ctx := context.Background()
	if err := a.db.CreateUser(ctx, "alice", "x", "user", ""); err != nil {
		t.Fatal(err)
	}
	u, _ := a.db.UserByName(ctx, "alice")
	_, err := a.db.DB.ExecContext(ctx, `INSERT INTO instances(id,instance_id,name,dcv_host,dcv_session_id) VALUES(1,'i-ok','dev','dev.local','console'); INSERT INTO instance_users(instance_id,user_id,can_control) VALUES(1,?,1);`, u.ID)
	if err != nil {
		t.Fatal(err)
	}

	r := requestAs(a, u, http.MethodGet, "/dcv/i-ok", nil)
	r.SetPathValue("id", "i-ok")
	w := httptest.NewRecorder()
	a.require(a.dcv)(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("dcv redirect=%d body=%s", w.Code, w.Body.String())
	}
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	token := loc.Query().Get("authToken")
	if token == "" || loc.Fragment != "console" {
		t.Fatalf("bad DCV redirect: %s", loc.String())
	}

	form := url.Values{"authenticationToken": {token}, "sessionId": {"console"}}.Encode()
	first := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/dcv-auth", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	a.dcvAuth(first, req)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `<auth result="yes"><username>alice</username></auth>`) {
		t.Fatalf("first dcv auth code=%d body=%q", first.Code, first.Body.String())
	}

	second := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/dcv-auth", strings.NewReader(form))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	a.dcvAuth(second, req2)
	if second.Code != http.StatusUnauthorized {
		t.Fatalf("reused DCV token code=%d", second.Code)
	}
}

func TestHTMXInstanceActionReturnsPollingRow(t *testing.T) {
	a, ec2 := newHandlerTestApp(t)
	ctx := context.Background()
	if err := a.db.CreateUser(ctx, "alice", "x", "user", ""); err != nil {
		t.Fatal(err)
	}
	u, _ := a.db.UserByName(ctx, "alice")
	_, err := a.db.DB.ExecContext(ctx, `INSERT INTO instances(id,instance_id,name,dcv_host) VALUES(1,'i-ok','dev','dev.local'); INSERT INTO instance_users(instance_id,user_id,can_control) VALUES(1,?,1);`, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	ec2.states["i-ok"] = "stopped"

	r := requestAs(a, u, http.MethodPost, "/instance/i-ok/start", nil)
	r.SetPathValue("id", "i-ok")
	r.SetPathValue("action", "start")
	r.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	a.require(a.instanceAction)(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("htmx action code=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{`data-state="pending"`, `hx-get="/instances/i-ok/row"`, "AWSの状態を確認中"} {
		if !strings.Contains(body, want) {
			t.Fatalf("HTMX fragment missing %q: %s", want, body)
		}
	}
	if len(ec2.started) != 1 || ec2.started[0] != "i-ok" {
		t.Fatalf("start calls=%v", ec2.started)
	}
}

func TestHTMXInstanceRowStopsPollingAtTerminalState(t *testing.T) {
	a, ec2 := newHandlerTestApp(t)
	ctx := context.Background()
	if err := a.db.CreateUser(ctx, "alice", "x", "user", ""); err != nil {
		t.Fatal(err)
	}
	u, _ := a.db.UserByName(ctx, "alice")
	_, err := a.db.DB.ExecContext(ctx, `INSERT INTO instances(id,instance_id,name,dcv_host) VALUES(1,'i-ok','dev','dev.local'); INSERT INTO instance_users(instance_id,user_id,can_control) VALUES(1,?,1);`, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	ec2.states["i-ok"] = "running"

	r := requestAs(a, u, http.MethodGet, "/instances/i-ok/row", nil)
	r.SetPathValue("id", "i-ok")
	w := httptest.NewRecorder()
	a.require(a.instanceRow)(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("row code=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, `hx-trigger="load delay:1500ms`) {
		t.Fatalf("terminal row must stop polling: %s", body)
	}
	if !strings.Contains(body, `data-state="running"`) || !strings.Contains(body, ">接続</a>") {
		t.Fatalf("running row invalid: %s", body)
	}
}

func TestHTMXAdminDisableReturnsUpdatedUserRow(t *testing.T) {
	a, _ := newHandlerTestApp(t)
	ctx := context.Background()
	if err := a.db.CreateUser(ctx, "admin", "x", "portal_admin", "SECRET"); err != nil {
		t.Fatal(err)
	}
	if err := a.db.CreateUser(ctx, "alice", "x", "user", ""); err != nil {
		t.Fatal(err)
	}
	admin, _ := a.db.UserByName(ctx, "admin")

	r := requestAs(a, admin, http.MethodPost, "/admin/users/alice/disable", nil)
	r.SetPathValue("username", "alice")
	r.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	a.require(a.adminDisableUser)(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("disable code=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{`data-user="alice"`, "無効", `hx-post="/admin/users/alice/reactivate"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("user fragment missing %q: %s", want, body)
		}
	}
	u, _ := a.db.UserByName(ctx, "alice")
	if u.Enabled {
		t.Fatal("alice should be disabled")
	}
}

func TestHTMXMFADeleteRemovesDeviceWithoutRedirect(t *testing.T) {
	a, _ := newHandlerTestApp(t)
	ctx := context.Background()
	if err := a.db.CreateUser(ctx, "alice", "x", "user", ""); err != nil {
		t.Fatal(err)
	}
	u, _ := a.db.UserByName(ctx, "alice")
	id, err := a.db.AddMFADevice(ctx, u.ID, "phone", "SECRET")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.db.VerifyMFADevice(ctx, u.ID, id); err != nil {
		t.Fatal(err)
	}

	r := requestAs(a, u, http.MethodPost, "/mfa/device/delete", nil)
	r.SetPathValue("id", fmt.Sprint(id))
	r.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	a.require(a.mfaDelete)(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("mfa delete code=%d body=%s", w.Code, w.Body.String())
	}
	if w.Header().Get("Location") != "" {
		t.Fatalf("htmx delete must not redirect: %q", w.Header().Get("Location"))
	}
	if ds, _ := a.db.MFADevices(ctx, u.ID); len(ds) != 0 {
		t.Fatalf("device not deleted: %#v", ds)
	}
}

func TestHTMXRefreshFragmentsAndHistory(t *testing.T) {
	a, _ := newHandlerTestApp(t)
	if err := a.db.CreateUser(context.Background(), "admin", "x", "portal_admin", ""); err != nil {
		t.Fatal(err)
	}
	u, _ := a.db.UserByName(context.Background(), "admin")
	for _, tc := range []struct {
		path, target string
		handler      http.HandlerFunc
	}{
		{"/", "dashboard-live", a.dashboard},
		{"/instances", "instances-live", a.instancesPage},
	} {
		for _, history := range []bool{false, true} {
			r := requestAs(a, u, "GET", tc.path, nil)
			r.Header.Set("HX-Request", "true")
			r.Header.Set("HX-Target", tc.target)
			if history {
				r.Header.Set("HX-History-Restore-Request", "true")
			}
			w := httptest.NewRecorder()
			a.require(tc.handler)(w, r)
			if w.Code != 200 {
				t.Fatalf("%s status %d", tc.path, w.Code)
			}
			full := strings.Contains(w.Body.String(), "<!doctype html>")
			if full != history {
				t.Fatalf("%s full=%v history=%v", tc.path, full, history)
			}
			if !strings.Contains(w.Body.String(), `id="`+tc.target+`"`) {
				t.Fatalf("missing region %s", tc.target)
			}
		}
	}
}

func TestHTMXExpiredSessionRedirectsWholeWindow(t *testing.T) {
	a, _ := newHandlerTestApp(t)
	r := httptest.NewRequest("GET", "/instances", nil)
	r.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	a.require(a.instancesPage)(w, r)
	if w.Code != 401 || w.Header().Get("HX-Redirect") != "/login" {
		t.Fatalf("code=%d redirect=%q", w.Code, w.Header().Get("HX-Redirect"))
	}
}

func TestHTMXDetailActionAndTerminalRefresh(t *testing.T) {
	a, ec2 := newHandlerTestApp(t)
	ctx := context.Background()
	if err := a.db.CreateUser(ctx, "alice", "x", "user", ""); err != nil {
		t.Fatal(err)
	}
	u, _ := a.db.UserByName(ctx, "alice")
	_, err := a.db.DB.ExecContext(ctx, `INSERT INTO instances(id,instance_id,name,dcv_host) VALUES(1,'i-ok','dev','dev.local'); INSERT INTO instance_users(instance_id,user_id,can_control) VALUES(1,?,1);`, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	r := requestAs(a, u, "POST", "/instance/i-ok/start", nil)
	r.SetPathValue("id", "i-ok")
	r.SetPathValue("action", "start")
	r.Header.Set("HX-Request", "true")
	r.Header.Set("HX-Target", "instance-live")
	w := httptest.NewRecorder()
	a.require(a.instanceAction)(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `id="instance-live"`) || !strings.Contains(w.Body.String(), "data-transition") || strings.Contains(w.Body.String(), "<tr") {
		t.Fatalf("invalid detail fragment: %s", w.Body.String())
	}
	ec2.states["i-ok"] = "running"
	r = requestAs(a, u, "GET", "/instances/i-ok", nil)
	r.SetPathValue("id", "i-ok")
	r.Header.Set("HX-Request", "true")
	r.Header.Set("HX-Target", "instance-live")
	w = httptest.NewRecorder()
	a.require(a.instanceDetail)(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), "data-transition") || strings.Contains(w.Body.String(), "<!doctype") || !strings.Contains(w.Body.String(), "DCV 接続") {
		t.Fatalf("invalid terminal detail: %s", w.Body.String())
	}
}

func TestDCVAuthResponseEscapesUsername(t *testing.T) {
	w := httptest.NewRecorder()
	dcvAuthReply(w, 200, "yes", "user<&", "")
	if w.Header().Get("Content-Type") != "application/xml; charset=utf-8" || !strings.Contains(w.Body.String(), "user&lt;&amp;") {
		t.Fatalf("invalid DCV XML: %s", w.Body.String())
	}
}
