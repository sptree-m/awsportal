package mirror

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"github.com/sptree-m/awsportal/internal/store"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitTest(t *testing.T, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0")
	out, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %v %s", args, e, out)
	}
	return strings.TrimSpace(string(out))
}
func TestHTTPSMirrorSyncCloneFetchAndPushDenied(t *testing.T) {
	root := t.TempDir()
	working := filepath.Join(root, "work")
	uproot := filepath.Join(root, "upstream")
	os.MkdirAll(uproot, 0700)
	gitTest(t, "init", "-b", "main", working)
	os.WriteFile(filepath.Join(working, "code.txt"), []byte("first"), 0600)
	gitTest(t, "-C", working, "add", ".")
	gitTest(t, "-C", working, "commit", "-m", "first")
	origin := filepath.Join(uproot, "origin.git")
	gitTest(t, "init", "--bare", origin)
	gitTest(t, "-C", working, "push", origin, "main")
	gitTest(t, "-C", origin, "symbolic-ref", "HEAD", "refs/heads/main")
	db, e := store.Open(filepath.Join(root, "db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.Migrate(context.Background())
	credentials := filepath.Join(root, "creds")
	os.Mkdir(credentials, 0700)
	os.WriteFile(filepath.Join(credentials, "lab.json"), []byte(`{"Username":"reader","Token":"upstream-secret"}`), 0600)
	manager, e := New(db, filepath.Join(root, "mirrors"), credentials, "")
	if e != nil {
		t.Fatal(e)
	}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "reader" || p != "upstream-secret" {
			t.Error("missing upstream credential")
			http.Error(w, "auth", 401)
			return
		}
		if strings.Contains(r.URL.String(), "receive-pack") {
			t.Error("upstream push attempted")
		}
		(&cgi.Handler{Path: manager.Backend, Dir: uproot, Env: []string{"GIT_PROJECT_ROOT=" + uproot, "GIT_HTTP_EXPORT_ALL=1"}}).ServeHTTP(w, r)
	}))
	defer upstream.Close()
	ca := filepath.Join(root, "ca.pem")
	cert, _ := x509.ParseCertificate(upstream.TLS.Certificates[0].Certificate[0])
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0600)
	manager.CAFile = ca
	repo := store.MirrorRepo{ID: 1, Name: "lab", Upstream: upstream.URL + "/origin.git", CredentialRef: "lab", Branch: "main", Enabled: true}
	repo.ID = 0
	if e = db.SaveMirror(context.Background(), repo); e != nil {
		t.Fatal(e)
	}
	repo, _ = db.Mirror(context.Background(), 1)
	if e = manager.Sync(context.Background(), repo); e != nil {
		t.Fatal("sync", e)
	}
	repo.LastSuccess = 1
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		suffix := strings.TrimPrefix(r.URL.Path, "/1/")
		manager.Serve(w, r, repo, suffix)
	}))
	defer downstream.Close()
	packet := func(value string) string { return fmt.Sprintf("%04x%s", len(value)+4, value) }
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	gzipWriter.Write([]byte(packet("command=ls-refs\n") + "0001" + packet("symrefs\n") + "0000"))
	gzipWriter.Close()
	request, _ := http.NewRequest("POST", downstream.URL+"/1/git-upload-pack", &compressed)
	request.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	request.Header.Set("Content-Encoding", "gzip")
	request.Header.Set("Git-Protocol", "version=2")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	responseBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !bytes.Contains(responseBody, []byte("refs/heads/main")) {
		t.Fatalf("gzip Git protocol: %d %s", response.StatusCode, responseBody)
	}
	clone := filepath.Join(root, "clone")
	gitTest(t, "clone", downstream.URL+"/1", clone)
	b, _ := os.ReadFile(filepath.Join(clone, "code.txt"))
	if string(b) != "first" {
		t.Fatal("wrong clone")
	}
	os.WriteFile(filepath.Join(working, "code.txt"), []byte("second"), 0600)
	gitTest(t, "-C", working, "commit", "-am", "second")
	gitTest(t, "-C", working, "push", origin, "main")
	if e = manager.Sync(context.Background(), repo); e != nil {
		t.Fatal(e)
	}
	gitTest(t, "-C", clone, "pull", "--ff-only")
	b, _ = os.ReadFile(filepath.Join(clone, "code.txt"))
	if string(b) != "second" {
		t.Fatal("not refreshed")
	}
	os.WriteFile(filepath.Join(clone, "code.txt"), []byte("must not upload"), 0600)
	gitTest(t, "-C", clone, "commit", "-am", "attempt write")
	if e = exec.Command("git", "-C", clone, "push", "origin", "main").Run(); e == nil {
		t.Fatal("push succeeded")
	}
	if gitTest(t, "-C", origin, "rev-parse", "main") == gitTest(t, "-C", clone, "rev-parse", "main") {
		t.Fatal("source mutated")
	}
	for _, target := range []string{"/1/info/refs?service=git-receive-pack", "/1/objects/info/packs", "/1/config", "/1/info/refs?service=git-upload-pack&service=git-receive-pack"} {
		response, e := http.Get(downstream.URL + target)
		if e != nil {
			t.Fatal(e)
		}
		response.Body.Close()
		if response.StatusCode != 403 {
			t.Fatal(target, response.StatusCode)
		}
	}
	j, e := db.EnqueueMirror(context.Background(), repo.ID, 0)
	if e != nil {
		t.Fatal(e)
	}
	manager.Tick(context.Background())
	j, _ = db.MirrorJob(context.Background(), j.ID)
	if j.State != "success" {
		t.Fatal(j)
	}
	repo.IntervalMinutes = 1
	if e = db.SaveMirror(context.Background(), repo); e != nil {
		t.Fatal(e)
	}
	db.DB.Exec("UPDATE mirror_jobs SET created=1")
	manager.Tick(context.Background())
	var scheduled int
	db.DB.QueryRow("SELECT COUNT(*) FROM mirror_jobs WHERE requested_by=0 AND state='success'").Scan(&scheduled)
	if scheduled != 2 {
		t.Fatal("scheduled sync did not run", scheduled)
	}
}
func TestSyncRejectsCredentialSymlinkAndUnsafeURL(t *testing.T) {
	db, _ := store.Open(filepath.Join(t.TempDir(), "db"))
	defer db.Close()
	db.Migrate(context.Background())
	root := t.TempDir()
	m, e := New(db, filepath.Join(root, "mirror"), root, "")
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(root, "real.json"), []byte(`{"Username":"x","Token":"x"}`), 0600)
	os.Symlink(filepath.Join(root, "real.json"), filepath.Join(root, "link.json"))
	r := store.MirrorRepo{ID: 1, Name: "x", Upstream: "https://example.com/x.git", CredentialRef: "link", Branch: "main", Enabled: true}
	if m.Sync(context.Background(), r) == nil {
		t.Fatal("symlink credential")
	}
	for _, url := range []string{"file:///tmp/x.git", "https://secret@example.com/x.git", "ssh://example.com/x.git", "https://example.com/x.git?token=secret"} {
		r.Upstream = url
		if m.Sync(context.Background(), r) == nil {
			t.Fatal(url)
		}
	}
}
