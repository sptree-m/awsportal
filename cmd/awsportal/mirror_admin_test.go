package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/sptree-m/awsportal/internal/mirror"
	"github.com/sptree-m/awsportal/internal/store"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMirrorAPIAuthenticationScopeAndNoUpstreamOptions(t *testing.T) {
	a, _ := newHandlerTestApp(t)
	ctx := context.Background()
	a.db.CreateUser(ctx, "alice", "x", "user", "")
	a.db.CreateUser(ctx, "bob", "x", "user", "")
	u, _ := a.db.UserByName(ctx, "alice")
	repo := store.MirrorRepo{Name: "code", Upstream: "https://gitlab.example/team/repo.git", Branch: "main", Enabled: true}
	a.db.SaveMirror(ctx, repo)
	a.db.SetMirrorGrant(ctx, 1, "user", u.ID, true, false)
	token, _ := a.db.IssueMirrorToken(ctx, u.ID, "build", 1, true)
	readToken, _ := a.db.IssueMirrorToken(ctx, u.ID, "read", 1, false)
	var e error
	a.mirrors, e = mirror.New(a.db, filepath.Join(t.TempDir(), "mirrors"), "", "")
	if e != nil {
		t.Fatal(e)
	}
	request := func(tok, target, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", target, strings.NewReader(body))
		r.SetPathValue("id", "1")
		if tok != "" {
			r.Header.Set("Authorization", "Bearer "+tok)
		}
		w := httptest.NewRecorder()
		a.mirrorSyncAPI(w, r)
		return w
	}
	if w := request("", "/api/mirrors/1/sync", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request(readToken, "/api/mirrors/1/sync", ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
	for _, target := range []string{"/api/mirrors/1/sync?url=https://evil.example/repo.git", "/api/mirrors/1/sync?push=1"} {
		if w := request(token, target, ""); w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	if w := request(token, "/api/mirrors/1/sync", "arbitrary data"); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := request(token, "/api/mirrors/1/sync", ""); w.Code != 202 || !strings.Contains(w.Body.String(), "job_id") {
		t.Fatal(w.Code, w.Body.String())
	}
	var n int
	a.db.DB.QueryRow("SELECT COUNT(*) FROM mirror_jobs").Scan(&n)
	request(token, "/api/mirrors/1/sync", "")
	a.db.DB.QueryRow("SELECT COUNT(*) FROM mirror_jobs").Scan(&n)
	if n != 1 {
		t.Fatal("duplicate job")
	}
	a.db.RevokeMirrorToken(ctx, 1)
	if w := request(token, "/api/mirrors/1/sync", ""); w.Code != 401 {
		t.Fatal("revoked token")
	}
	bob, _ := a.db.UserByName(ctx, "bob")
	bt, _ := a.db.IssueMirrorToken(ctx, bob.ID, "build", 1, true)
	if w := request(bt, "/api/mirrors/1/sync", ""); w.Code != 403 {
		t.Fatal("unassigned")
	}
	a.db.DB.Exec("UPDATE users SET enabled=0 WHERE id=?", u.ID)
	if w := request(readToken, "/api/mirrors/1/sync", ""); w.Code != 401 {
		t.Fatal("disabled user")
	}
}
func TestMirrorAdminAndReadOnlyEndpoints(t *testing.T) {
	a, _ := newHandlerTestApp(t)
	ctx := context.Background()
	a.db.CreateUser(ctx, "admin", "x", "portal_admin", "")
	a.db.CreateUser(ctx, "alice", "x", "user", "")
	admin, _ := a.db.UserByName(ctx, "admin")
	alice, _ := a.db.UserByName(ctx, "alice")
	form := url.Values{"operation": {"save"}, "name": {"Team Code"}, "upstream": {"https://gitlab.example/team/code.git"}, "branch": {"main"}, "interval": {"60"}, "enabled": {"1"}}
	post := func(u store.User) *httptest.ResponseRecorder {
		r := requestAs(a, u, "POST", "/mirrors", strings.NewReader(form.Encode()))
		r.Header.Set("HX-Request", "true")
		r.Header.Set("HX-Target", "mirrors-live")
		w := httptest.NewRecorder()
		a.require(a.mirrorChange)(w, r)
		return w
	}
	if w := post(alice); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := post(admin); w.Code != 200 || !strings.Contains(w.Body.String(), "Team Code") {
		t.Fatal(w.Code, w.Body.String())
	}
	repo, _ := a.db.Mirror(ctx, 1)
	repo.LastSuccess = 1
	a.db.DB.Exec("UPDATE mirror_repos SET last_success=1")
	a.db.SetMirrorGrant(ctx, 1, "user", alice.ID, false, false)
	tok, _ := a.db.IssueMirrorToken(ctx, alice.ID, "read", 1, false)
	a.mirrors, _ = mirror.New(a.db, filepath.Join(t.TempDir(), "mirror"), "", "")
	for _, suffix := range []string{"git-receive-pack", "config", "objects/info/packs"} {
		r := httptest.NewRequest(http.MethodGet, "/git/mirrors/1/"+suffix, nil)
		r.SetPathValue("id", "1")
		r.SetPathValue("suffix", suffix)
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		a.mirrorGit(w, r)
		if w.Code != 403 {
			t.Fatal(fmt.Sprint(suffix, w.Code))
		}
	}
}

func TestAutomationCLISyncWaitOverTLS(t *testing.T) {
	a, _ := newHandlerTestApp(t)
	ctx := context.Background()
	a.db.CreateUser(ctx, "alice", "x", "user", "")
	u, _ := a.db.UserByName(ctx, "alice")
	a.db.SaveMirror(ctx, store.MirrorRepo{Name: "code", Upstream: "https://gitlab.example/team/code.git", Branch: "main", Enabled: true})
	a.db.SetMirrorGrant(ctx, 1, "user", u.ID, true, false)
	token, _ := a.db.IssueMirrorToken(ctx, u.ID, "automation", 1, true)
	a.mirrors, _ = mirror.New(a.db, filepath.Join(t.TempDir(), "mirrors"), "", "")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/mirrors/{id}/sync", func(w http.ResponseWriter, r *http.Request) {
		a.mirrorSyncAPI(w, r)
		a.db.DB.Exec("UPDATE mirror_jobs SET state='success' WHERE state='queued'")
	})
	mux.HandleFunc("GET /api/mirror-jobs/{id}", a.mirrorJobAPI)
	server := httptest.NewTLSServer(mux)
	defer server.Close()
	root := t.TempDir()
	ca := filepath.Join(root, "ca.pem")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.TLS.Certificates[0].Certificate[0]}), 0600)
	cfg := filepath.Join(root, "config.json")
	b, _ := json.Marshal(map[string]string{"portal_url": server.URL, "token": token, "ca_file": ca})
	os.WriteFile(cfg, b, 0600)
	command := exec.Command("python3", "../../scripts/awsportal-mirror", "--config", cfg, "sync", "1", "--wait", "--timeout", "10")
	out, e := command.CombinedOutput()
	if e != nil || !strings.Contains(string(out), `"state": "success"`) || strings.Contains(string(out), token) {
		t.Fatalf("CLI %v %s", e, out)
	}
	os.Chmod(cfg, 0644)
	if e = exec.Command("python3", "../../scripts/awsportal-mirror", "--config", cfg, "status", "1").Run(); e == nil {
		t.Fatal("unsafe credential permissions")
	}
}

func TestLFSEndpointsRequireCurrentMirrorGrantAndReadToken(t *testing.T) {
	a, _ := newHandlerTestApp(t)
	ctx := context.Background()
	if err := a.db.CreateUser(ctx, "alice", "x", "user", ""); err != nil {
		t.Fatal(err)
	}
	user, _ := a.db.UserByName(ctx, "alice")
	if err := a.db.SaveMirror(ctx, store.MirrorRepo{Name: "code", Upstream: "https://gitlab.example/team/repo.git", Branch: "main", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	a.db.DB.Exec("UPDATE mirror_repos SET last_success=1 WHERE id=1")
	a.db.SetMirrorGrant(ctx, 1, "user", user.ID, false, false)
	token, _ := a.db.IssueMirrorToken(ctx, user.ID, "read", 1, false)
	var err error
	a.mirrors, err = mirror.New(a.db, filepath.Join(t.TempDir(), "mirrors"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	settings := store.DefaultSiteSettings()
	settings.PortalURL = "https://portal.company.example"
	a.db.SaveSiteSettings(ctx, store.User{Role: "portal_admin"}, settings)
	request := func(secret, operation string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/git/mirrors/1/info/lfs/objects/batch", strings.NewReader(`{"operation":"`+operation+`","objects":[]}`))
		r.SetPathValue("id", "1")
		r.SetPathValue("suffix", "info/lfs/objects/batch")
		r.Header.Set("Content-Type", "application/vnd.git-lfs+json")
		if secret != "" {
			r.Header.Set("Authorization", "Bearer "+secret)
		}
		w := httptest.NewRecorder()
		a.mirrorGit(w, r)
		return w
	}
	if w := request("", "download"); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request(token, "download"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(token, "upload"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	a.db.SetMirrorGrant(ctx, 1, "user", user.ID, false, true)
	if w := request(token, "download"); w.Code != 403 {
		t.Fatal("revoked grant", w.Code)
	}
}
