// Package mirror fetches registered HTTPS repositories and serves only local upload-pack.
package mirror

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/sptree-m/awsportal/internal/store"
	"io"
	"net/http"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Manager struct {
	Root, CredentialDir, CAFile, Git, Backend string
	DB                                        *store.Store
	slots                                     chan struct{}
	LFSMaxObjectBytes, LFSMaxSyncBytes        int64
	LFSDownloadOrigins                        []string
}

func New(db *store.Store, root, credentials, ca string) (*Manager, error) {
	git, e := exec.LookPath("git")
	if e != nil {
		return nil, e
	}
	out, e := exec.Command(git, "--exec-path").Output()
	if e != nil {
		return nil, e
	}
	backend := filepath.Join(strings.TrimSpace(string(out)), "git-http-backend")
	if _, e = os.Stat(backend); e != nil {
		return nil, e
	}
	root, e = filepath.Abs(root)
	if e != nil {
		return nil, e
	}
	if e = os.MkdirAll(root, 0700); e != nil {
		return nil, e
	}
	m := &Manager{Root: root, CredentialDir: credentials, CAFile: ca, Git: git, Backend: backend, DB: db, slots: make(chan struct{}, 4), LFSMaxObjectBytes: 10 << 30, LFSMaxSyncBytes: 50 << 30}
	if e = m.configureLFS(); e != nil {
		return nil, e
	}
	return m, nil
}
func (m *Manager) Path(id int64) string {
	return filepath.Join(m.Root, strconv.FormatInt(id, 10)+".git")
}
func gitEnv(config map[string]string) []string {
	env := []string{}
	for _, v := range os.Environ() {
		k, _, _ := strings.Cut(v, "=")
		if strings.HasPrefix(k, "GIT_") || strings.HasPrefix(k, "SSH_") || strings.HasSuffix(strings.ToUpper(k), "PROXY") {
			continue
		}
		env = append(env, v)
	}
	base := map[string]string{"credential.helper": "", "protocol.allow": "never", "protocol.https.allow": "always", "http.followRedirects": "false", "http.sslVerify": "true", "core.hooksPath": "/dev/null", "gc.auto": "0", "pack.threads": "1", "pack.windowMemory": "16m", "core.deltaBaseCacheLimit": "16m", "fetch.fsckObjects": "true", "transfer.fsckObjects": "true", "http.receivepack": "false", "http.getanyfile": "false", "transfer.hideRefs": stagePrefix, "uploadpack.allowAnySHA1InWant": "false", "uploadpack.allowReachableSHA1InWant": "false", "uploadpack.allowTipSHA1InWant": "false"}
	for k, v := range config {
		base[k] = v
	}
	env = append(env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_COUNT="+strconv.Itoa(len(base)))
	i := 0
	for k, v := range base {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, k), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, v))
		i++
	}
	return env
}
func (m *Manager) Sync(ctx context.Context, r store.MirrorRepo) error {
	if e := store.ValidateMirror(r); e != nil {
		return e
	}
	if r.ID < 1 {
		return fmt.Errorf("invalid mirror ID")
	}
	if !r.Enabled {
		return fmt.Errorf("disabled")
	}
	config := map[string]string{}
	if m.CAFile != "" {
		config["http.sslCAInfo"] = m.CAFile
	}
	if r.CredentialRef != "" {
		if m.CredentialDir == "" {
			return fmt.Errorf("credential directory required")
		}
		path := filepath.Join(m.CredentialDir, r.CredentialRef+".json")
		info, e := os.Lstat(path)
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("credential file must be a private regular file")
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return fmt.Errorf("credential unavailable")
		}
		var c struct{ Username, Token string }
		if json.Unmarshal(b, &c) != nil || c.Username == "" || c.Token == "" || strings.ContainsAny(c.Username, ":\r\n") {
			return fmt.Errorf("invalid credential")
		}
		config["http."+r.Upstream+".extraHeader"] = "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(c.Username+":"+c.Token))
	}
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, m.Git, args...)
		cmd.Env = gitEnv(config)
		boundProcess(cmd)
		if e := cmd.Run(); e != nil {
			return fmt.Errorf("git command failed")
		}
		return nil
	}
	path := m.Path(r.ID)
	if _, e := os.Stat(path); os.IsNotExist(e) {
		if e = run("init", "--bare", "--template=", path); e != nil {
			return e
		}
	}
	if e := run("-C", path, "fetch", "--atomic", "--prune", "--no-write-fetch-head", "--no-auto-maintenance", "--", r.Upstream, "+refs/heads/*:"+stagePrefix+"heads/*", "+refs/tags/*:"+stagePrefix+"tags/*"); e != nil {
		return e
	}
	// Verify the selected default branch before exposing the repository for its first clone.
	if e := run("-C", path, "show-ref", "--verify", stagePrefix+"heads/"+r.Branch); e != nil {
		return e
	}
	if e := m.syncLFS(ctx, r.ID, r.Upstream, config["http."+r.Upstream+".extraHeader"]); e != nil {
		return e
	}
	if e := m.publishRefs(ctx, path, r.Branch); e != nil {
		return e
	}
	return run("-C", path, "symbolic-ref", "HEAD", "refs/heads/"+r.Branch)
}
func (m *Manager) Run(ctx context.Context) {
	if m.DB.RecoverMirrorJobs(ctx) != nil {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		m.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (m *Manager) Tick(ctx context.Context) {
	repos, e := m.DB.Mirrors(ctx)
	if e != nil {
		return
	}
	now := time.Now().Unix()
	for _, r := range repos {
		if !r.Enabled || r.IntervalMinutes == 0 {
			continue
		}
		var last int64
		m.DB.DB.QueryRowContext(ctx, "SELECT COALESCE(MAX(created),0) FROM mirror_jobs WHERE repo_id=?", r.ID).Scan(&last)
		if now-last >= r.IntervalMinutes*60 {
			m.DB.EnqueueMirror(ctx, r.ID, 0)
		}
	}
	job, e := m.DB.ClaimMirrorJob(ctx)
	if e != nil {
		return
	}
	repo, e := m.DB.Mirror(ctx, job.RepoID)
	if e == nil && job.RequestedBy > 0 {
		var name string
		e = m.DB.DB.QueryRowContext(ctx, "SELECT username FROM users WHERE id=?", job.RequestedBy).Scan(&name)
		if e == nil {
			u, err := m.DB.UserByName(ctx, name)
			if err != nil || !m.DB.MirrorAccess(ctx, u, repo.ID, true) {
				e = fmt.Errorf("sync access revoked")
			}
		}
	}
	if e == nil {
		task, cancel := context.WithTimeout(ctx, 10*time.Minute)
		e = m.Sync(task, repo)
		cancel()
	}
	m.DB.FinishMirrorJob(context.Background(), job, repo.Revision, e)
	result := "ok"
	if e != nil {
		result = "deny"
	}
	m.DB.Audit(context.Background(), "mirror-worker", "mirror.sync", strconv.FormatInt(repo.ID, 10), result, fmt.Sprintf("job=%d;requested_by=%d", job.ID, job.RequestedBy))
}
func (m *Manager) Serve(w http.ResponseWriter, r *http.Request, repo store.MirrorRepo, suffix string) {
	// Fixed local endpoints. No client URL, arbitrary file, API, push or upstream request is forwarded.
	q := r.URL.Query()
	valid := r.Method == "GET" && suffix == "info/refs" && len(q) == 1 && len(q["service"]) == 1 && q.Get("service") == "git-upload-pack" || r.Method == "POST" && suffix == "git-upload-pack" && len(q) == 0 && r.Header.Get("Content-Type") == "application/x-git-upload-pack-request"
	if !valid {
		http.Error(w, "read-only Git endpoint", 403)
		return
	}
	if repo.LastSuccess == 0 {
		http.Error(w, "mirror is not ready; request sync and wait", 409)
		return
	}
	select {
	case m.slots <- struct{}{}:
		defer func() { <-m.slots }()
	default:
		http.Error(w, "mirror busy", 503)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()

	body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if e != nil {
		http.Error(w, "Git request too large", 413)
		return
	}
	if encoding := r.Header.Get("Content-Encoding"); encoding != "" {
		if encoding != "gzip" {
			http.Error(w, "unsupported encoding", 415)
			return
		}
		z, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			http.Error(w, "invalid gzip", 400)
			return
		}
		body, err = io.ReadAll(io.LimitReader(z, (4<<20)+1))
		z.Close()
		if err != nil || len(body) > 4<<20 {
			http.Error(w, "Git request too large", 413)
			return
		}
	}
	cmd := exec.CommandContext(ctx, m.Backend)
	boundProcess(cmd)
	cmd.Dir = m.Root
	cmd.Stdin = bytes.NewReader(body)
	cmd.Stderr = io.Discard
	env := gitEnv(nil)
	env = append(env, "GIT_PROJECT_ROOT="+m.Root, "GIT_HTTP_EXPORT_ALL=1", "GATEWAY_INTERFACE=CGI/1.1", "SERVER_PROTOCOL=HTTP/1.1", "REQUEST_METHOD="+r.Method, "QUERY_STRING="+func() string {
		if suffix == "info/refs" {
			return "service=git-upload-pack"
		}
		return ""
	}(), "PATH_INFO=/"+strconv.FormatInt(repo.ID, 10)+".git/"+suffix, "CONTENT_TYPE="+r.Header.Get("Content-Type"), "CONTENT_LENGTH="+strconv.Itoa(len(body)))
	if r.Header.Get("Git-Protocol") == "version=2" {
		env = append(env, "GIT_PROTOCOL=version=2")
	}
	cmd.Env = env
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		http.Error(w, "Git backend unavailable", 503)
		return
	}
	if e = cmd.Start(); e != nil {
		http.Error(w, "Git backend unavailable", 503)
		return
	}
	defer func() { cancel(); cmd.Wait() }()
	reader := bufio.NewReader(stdout)
	headers, e := textproto.NewReader(reader).ReadMIMEHeader()
	if e != nil {
		http.Error(w, "Git backend failed", 502)
		return
	}
	status := 200
	if value := headers.Get("Status"); value != "" {
		parts := strings.Fields(value)
		if len(parts) == 0 {
			http.Error(w, "Git backend failed", 502)
			return
		}
		status, e = strconv.Atoi(parts[0])
		if e != nil || status < 200 || status > 599 {
			http.Error(w, "Git backend failed", 502)
			return
		}
	}
	for key, values := range headers {
		if key == "Status" {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	io.Copy(w, reader)
}
func boundProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 3 * time.Second
}
