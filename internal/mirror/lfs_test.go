package mirror

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	"sync/atomic"
	"testing"
)

func TestLFSRealClientLocalDownloadsAndUpstreamWriteDenied(t *testing.T) {
	if _, err := exec.LookPath("git-lfs"); err != nil {
		t.Fatal("git-lfs required for LFS integration tests")
	}
	ctx := context.Background()
	root := t.TempDir()
	work := filepath.Join(root, "work")
	uproot := filepath.Join(root, "upstream")
	os.MkdirAll(uproot, 0700)
	gitTest(t, "init", "-b", "main", work)
	contents := []byte(strings.Repeat("large model payload\n", 65536))
	hash := sha256.Sum256(contents)
	oid := hex.EncodeToString(hash[:])
	pointer := fmt.Sprintf("version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize %d\n", oid, len(contents))
	os.WriteFile(filepath.Join(work, "model.bin"), []byte(pointer), 0600)
	os.WriteFile(filepath.Join(work, ".gitattributes"), []byte("*.bin filter=lfs diff=lfs merge=lfs -text\n"), 0600)
	os.WriteFile(filepath.Join(work, ".lfsconfig"), []byte("[lfs]\nurl = https://should-never-contact.example/lfs\n"), 0600)
	gitTest(t, "-C", work, "-c", "filter.lfs.clean=cat", "-c", "filter.lfs.required=false", "add", ".")
	gitTest(t, "-C", work, "commit", "-m", "LFS pointer")
	upstreamRepo := filepath.Join(uproot, "origin.git")
	gitTest(t, "init", "--bare", upstreamRepo)
	gitTest(t, "-C", work, "-c", "core.hooksPath=/dev/null", "push", upstreamRepo, "main")
	db, e := store.Open(filepath.Join(root, "db"))
	if e != nil {
		t.Fatal(e)
	}
	if e = db.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	credentials := filepath.Join(root, "creds")
	os.MkdirAll(credentials, 0700)
	os.WriteFile(filepath.Join(credentials, "lab.json"), []byte(`{"Username":"reader","Token":"upstream-secret"}`), 0600)
	manager, e := New(db, filepath.Join(root, "mirrors"), credentials, "")
	if e != nil {
		t.Fatal(e)
	}
	var upstreamRequests, downloads atomic.Int64
	var objectData atomic.Value
	objectData.Store(map[string][]byte{oid: contents})
	var corrupt atomic.Bool
	var foreignHref atomic.Value
	foreignHref.Store("")
	var upstream *httptest.Server
	upstream = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests.Add(1)
		u, p, ok := r.BasicAuth()
		if !ok || u != "reader" || p != "upstream-secret" {
			t.Error("upstream auth missing")
			http.Error(w, "auth", 401)
			return
		}
		switch r.URL.Path {
		case "/origin.git/info/lfs/objects/batch":
			var batch lfsBatch
			json.NewDecoder(r.Body).Decode(&batch)
			if batch.Operation != "download" {
				t.Error("upstream write operation")
			}
			out := []lfsObject{}
			for _, o := range batch.Objects {
				href := upstream.URL + "/origin.git/lfs-data/" + o.OID
				if v := foreignHref.Load().(string); v != "" {
					href = v
				}
				o.Actions = map[string]lfsAction{"download": {Href: href}}
				out = append(out, o)
			}
			lfsJSON(w, 200, lfsBatch{Transfer: "basic", Objects: out})
		default: // Only registered object hashes in this fixture may be downloaded.
			if !strings.HasPrefix(r.URL.Path, "/origin.git/lfs-data/") {
				if strings.Contains(r.URL.String(), "receive-pack") {
					t.Error("upstream write attempted")
				}
				(&cgi.Handler{Path: manager.Backend, Dir: uproot, Env: []string{"GIT_PROJECT_ROOT=" + uproot, "GIT_HTTP_EXPORT_ALL=1"}}).ServeHTTP(w, r)
				return
			}
			data, ok := objectData.Load().(map[string][]byte)[strings.TrimPrefix(r.URL.Path, "/origin.git/lfs-data/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			downloads.Add(1)
			if corrupt.Load() {
				w.Write([]byte("corrupt"))
			} else {
				w.Write(data)
			}
		}
	}))
	defer upstream.Close()
	ca := filepath.Join(root, "up-ca.pem")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: upstream.Certificate().Raw}), 0600)
	manager.CAFile = ca
	repo := store.MirrorRepo{ID: 1, Name: "LFS", Upstream: upstream.URL + "/origin.git", Branch: "main", CredentialRef: "lab", Enabled: true}
	if e = manager.Sync(ctx, repo); e != nil {
		t.Fatal("sync", e)
	}
	repo.LastSuccess = 1
	oldHead := gitTest(t, "-C", manager.Path(1), "rev-parse", "main")
	// Bad LFS data must not publish new Git refs or replace the previous manifest.
	os.WriteFile(filepath.Join(work, "code.txt"), []byte("new revision"), 0600)
	gitTest(t, "-C", work, "add", "code.txt")
	gitTest(t, "-C", work, "commit", "-m", "new revision")
	gitTest(t, "-C", work, "-c", "core.hooksPath=/dev/null", "push", upstreamRepo, "main")
	os.Remove(manager.lfsPath(1, oid))
	corrupt.Store(true)
	if e = manager.Sync(ctx, repo); e == nil {
		t.Fatal("corrupt LFS accepted")
	}
	if gitTest(t, "-C", manager.Path(1), "rev-parse", "main") != oldHead {
		t.Fatal("failed sync published refs")
	}
	corrupt.Store(false)
	if e = manager.Sync(ctx, repo); e != nil {
		t.Fatal(e)
	}
	// Cross-origin storage is opt-in and must not receive GitLab credentials.
	var foreignRequests atomic.Int64
	foreign := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignRequests.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("upstream credential leaked to storage")
		}
		w.Write(contents)
	}))
	defer foreign.Close()
	foreignHref.Store(foreign.URL + "/object")
	os.Remove(manager.lfsPath(1, oid))
	if e = manager.Sync(ctx, repo); e == nil {
		t.Fatal("unapproved storage origin accepted")
	}
	if foreignRequests.Load() != 0 {
		t.Fatal("unapproved origin contacted")
	}
	manager.LFSDownloadOrigins = []string{foreign.URL}
	if e = manager.Sync(ctx, repo); e != nil {
		t.Fatal("approved storage", e)
	}
	if foreignRequests.Load() != 1 {
		t.Fatal("storage not fetched")
	}
	// Same-size cache corruption is detected and repaired on the next sync.
	os.WriteFile(manager.lfsPath(1, oid), bytes.Repeat([]byte("X"), len(contents)), 0600)
	if e = manager.Sync(ctx, repo); e != nil {
		t.Fatal(e)
	}
	if foreignRequests.Load() != 2 {
		t.Fatal("corrupt cache not repaired")
	}
	requestsBefore := upstreamRequests.Load()
	var uploadAttempts atomic.Int64
	var downstream *httptest.Server
	downstream = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer portal-secret" {
			http.Error(w, "auth", 401)
			return
		}
		suffix := strings.TrimPrefix(r.URL.Path, "/git/mirrors/1/")
		if strings.HasPrefix(suffix, "info/lfs/") {
			if r.Method == "POST" {
				body, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(body))
				if bytes.Contains(body, []byte(`"upload"`)) {
					uploadAttempts.Add(1)
				}
			}
			manager.ServeLFS(w, r, 1, suffix, downstream.URL)
		} else {
			manager.Serve(w, r, repo, suffix)
		}
	}))
	defer downstream.Close()
	ca = filepath.Join(root, "portal-ca.pem")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: downstream.Certificate().Raw}), 0600)
	config := filepath.Join(root, "client.json")
	data, _ := json.Marshal(map[string]string{"portal_url": downstream.URL, "token": "portal-secret", "ca_file": ca})
	os.WriteFile(config, data, 0600)
	script, _ := filepath.Abs("../../scripts/awsportal-mirror")
	clone := filepath.Join(root, "clone")
	cmd := exec.Command("python3", script, "--config", config, "clone", "1", clone)
	out, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("real LFS CLI clone: %v %s", e, out)
	}
	actual, _ := os.ReadFile(filepath.Join(clone, "model.bin"))
	if string(actual) != string(contents) {
		t.Fatal("LFS contents not materialized")
	}
	if strings.Contains(string(out), "portal-secret") {
		t.Fatal("token in output")
	}
	if upstreamRequests.Load() != requestsBefore {
		t.Fatal("clone reached upstream")
	}
	updated := []byte(strings.Repeat("updated model\n", 8192))
	updatedHash := sha256.Sum256(updated)
	updatedOID := hex.EncodeToString(updatedHash[:])
	objectData.Store(map[string][]byte{oid: contents, updatedOID: updated})
	foreignHref.Store("")
	os.WriteFile(filepath.Join(work, "model.bin"), []byte(fmt.Sprintf("version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize %d\n", updatedOID, len(updated))), 0600)
	gitTest(t, "-C", work, "-c", "filter.lfs.clean=cat", "-c", "filter.lfs.required=false", "add", "model.bin")
	gitTest(t, "-C", work, "commit", "-m", "updated LFS model")
	gitTest(t, "-C", work, "-c", "core.hooksPath=/dev/null", "push", upstreamRepo, "main")
	if e = manager.Sync(ctx, repo); e != nil {
		t.Fatal(e)
	}
	requestsBefore = upstreamRequests.Load()
	cmd = exec.Command("python3", script, "--config", config, "fetch", "1", clone)
	if out, e = cmd.CombinedOutput(); e != nil {
		t.Fatalf("LFS fetch: %v %s", e, out)
	}
	actual, _ = os.ReadFile(filepath.Join(clone, "model.bin"))
	if string(actual) != string(contents) {
		t.Fatal("fetch changed worktree")
	}
	gitTest(t, "-C", clone, "merge", "--ff-only", "origin/main")
	gitTest(t, "-C", clone, "lfs", "checkout")
	actual, _ = os.ReadFile(filepath.Join(clone, "model.bin"))
	if string(actual) != string(updated) {
		t.Fatal("fetched LFS not materialized")
	}
	push := exec.Command("git", "-C", clone, "lfs", "push", "--all", "origin")
	push.Env = gitEnv(map[string]string{"http.sslCAInfo": ca, "http." + downstream.URL + "/git/mirrors/1.extraHeader": "Authorization: Bearer portal-secret", "lfs.url": downstream.URL + "/git/mirrors/1/info/lfs"})
	if output, e := push.CombinedOutput(); e == nil {
		t.Fatal("real LFS push succeeded", string(output))
	}
	if uploadAttempts.Load() == 0 {
		t.Fatal("real LFS push did not exercise upload rejection")
	}
	client := downstream.Client()
	for _, body := range []string{`{"operation":"upload","objects":[]}`, `{"operation":"download","objects":[{"oid":"` + strings.Repeat("a", 64) + `","size":10}]}`} {
		req, _ := http.NewRequest("POST", downstream.URL+"/git/mirrors/1/info/lfs/objects/batch", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer portal-secret")
		req.Header.Set("Content-Type", lfsMedia)
		response, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		b, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if strings.Contains(body, "upload") && response.StatusCode != 403 {
			t.Fatal("upload accepted")
		}
		if strings.Contains(body, "download") && !strings.Contains(string(b), "404") {
			t.Fatal("unknown object accepted", string(b))
		}
	}
	req, _ := http.NewRequest("GET", downstream.URL+"/git/mirrors/1/info/lfs/objects/"+oid, nil)
	req.Header.Set("Authorization", "Bearer portal-secret")
	req.Header.Set("Range", "bytes=0-9")
	response, e := client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	rangeBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 206 || string(rangeBody) != string(contents[:10]) {
		t.Fatal("range failed")
	}
	if upstreamRequests.Load() != requestsBefore {
		t.Fatal("user LFS traffic reached upstream")
	}
	if downloads.Load() != 4 {
		t.Fatal("unexpected LFS downloads", downloads.Load())
	}
}

func TestLFSLimitsAndUnknownRequestsCannotFetchUpstream(t *testing.T) {
	t.Setenv("AWSPORTAL_MIRROR_LFS_MAX_OBJECT_BYTES", "1024")
	t.Setenv("AWSPORTAL_MIRROR_LFS_MAX_SYNC_BYTES", "2048")
	t.Setenv("AWSPORTAL_MIRROR_LFS_DOWNLOAD_ORIGINS", "https://storage.example:443")
	m, e := New(nil, t.TempDir(), "", "")
	if e != nil || m.LFSMaxObjectBytes != 1024 || m.LFSMaxSyncBytes != 2048 || len(m.LFSDownloadOrigins) != 1 {
		t.Fatal(m, e)
	}
	t.Setenv("AWSPORTAL_MIRROR_LFS_DOWNLOAD_ORIGINS", "https://secret@storage.example")
	if _, e = New(nil, t.TempDir(), "", ""); e == nil {
		t.Fatal("credential origin accepted")
	}
	t.Setenv("AWSPORTAL_MIRROR_LFS_DOWNLOAD_ORIGINS", "")
	t.Setenv("AWSPORTAL_MIRROR_LFS_MAX_OBJECT_BYTES", "-1")
	if _, e = New(nil, t.TempDir(), "", ""); e == nil {
		t.Fatal("negative limit accepted")
	}
}
