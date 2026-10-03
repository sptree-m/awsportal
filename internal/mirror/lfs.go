package mirror

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const lfsMedia = "application/vnd.git-lfs+json"
const stagePrefix = "refs/awsportal-sync/"

var lfsOID = regexp.MustCompile(`^[a-f0-9]{64}$`)

type lfsObject struct {
	OID           string               `json:"oid"`
	Size          int64                `json:"size"`
	Authenticated bool                 `json:"authenticated,omitempty"`
	Actions       map[string]lfsAction `json:"actions,omitempty"`
	Error         *lfsError            `json:"error,omitempty"`
}
type lfsAction struct {
	Href   string            `json:"href"`
	Header map[string]string `json:"header,omitempty"`
}
type lfsError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type lfsBatch struct {
	Operation string      `json:"operation,omitempty"`
	Transfers []string    `json:"transfers,omitempty"`
	Transfer  string      `json:"transfer,omitempty"`
	HashAlgo  string      `json:"hash_algo,omitempty"`
	Objects   []lfsObject `json:"objects"`
}

func (m *Manager) output(ctx context.Context, path string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, m.Git, append([]string{"-C", path}, args...)...)
	cmd.Env = gitEnv(nil)
	boundProcess(cmd)
	var out bytes.Buffer
	cmd.Stdout = &limitedBuffer{b: &out, max: 16 << 20}
	if cmd.Run() != nil {
		return nil, fmt.Errorf("Git metadata unavailable")
	}
	return out.Bytes(), nil
}

type limitedBuffer struct {
	b   *bytes.Buffer
	max int
}

func (w *limitedBuffer) Write(p []byte) (int, error) {
	if w.b.Len()+len(p) > w.max {
		return 0, fmt.Errorf("metadata limit exceeded")
	}
	return w.b.Write(p)
}
func (m *Manager) pointers(ctx context.Context, path string) (map[string]int64, error) {
	rev := exec.CommandContext(ctx, m.Git, "-C", path, "rev-list", "--objects", "--no-object-names", "--glob="+stagePrefix+"*")
	check := exec.CommandContext(ctx, m.Git, "-C", path, "cat-file", "--batch-check")
	rev.Env = gitEnv(nil)
	check.Env = gitEnv(nil)
	boundProcess(rev)
	boundProcess(check)
	pipe, err := rev.StdoutPipe()
	if err != nil {
		return nil, err
	}
	check.Stdin = pipe
	stream, err := check.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = check.Start(); err != nil {
		return nil, err
	}
	if err = rev.Start(); err != nil {
		check.Process.Kill()
		check.Wait()
		return nil, err
	}
	ids := []string{}
	scanner := bufio.NewScanner(stream)
	records := 0
	for scanner.Scan() {
		records++
		if records > 2000000 {
			err = fmt.Errorf("repository metadata limit exceeded")
			break
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) != 3 {
			err = fmt.Errorf("invalid Git metadata")
			break
		}
		size, e := strconv.ParseInt(fields[2], 10, 64)
		if e != nil {
			err = e
			break
		}
		if fields[1] == "blob" && size <= 1024 {
			ids = append(ids, fields[0])
			if len(ids) > 100000 {
				err = fmt.Errorf("small-blob limit exceeded")
				break
			}
		}
	}
	if scanner.Err() != nil {
		err = scanner.Err()
	}
	if err != nil {
		rev.Process.Kill()
		check.Process.Kill()
	}
	e1, e2 := rev.Wait(), check.Wait()
	if err != nil || e1 != nil || e2 != nil {
		return nil, fmt.Errorf("Git pointer scan failed")
	}
	cmd := exec.CommandContext(ctx, m.Git, "-C", path, "cat-file", "--batch")
	cmd.Env = gitEnv(nil)
	boundProcess(cmd)
	cmd.Stdin = strings.NewReader(strings.Join(ids, "\n") + "\n")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	defer func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	reader := bufio.NewReader(out)
	objects := map[string]int64{}
	for range ids {
		line, e := reader.ReadString('\n')
		if e != nil {
			return nil, e
		}
		f := strings.Fields(line)
		if len(f) != 3 {
			return nil, fmt.Errorf("invalid blob header")
		}
		n, e := strconv.Atoi(f[2])
		if e != nil || n < 0 || n > 1024 {
			return nil, fmt.Errorf("invalid pointer size")
		}
		b := make([]byte, n+1)
		if _, e = io.ReadFull(reader, b); e != nil {
			return nil, e
		}
		text := string(b[:n])
		if !strings.HasPrefix(text, "version https://git-lfs.github.com/spec/v1\n") {
			continue
		}
		var oid string
		size := int64(-1)
		for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
			switch {
			case strings.HasPrefix(line, "oid sha256:"):
				oid = strings.TrimPrefix(line, "oid sha256:")
			case strings.HasPrefix(line, "size "):
				size, e = strconv.ParseInt(strings.TrimPrefix(line, "size "), 10, 64)
				if e != nil {
					return nil, fmt.Errorf("invalid LFS size")
				}
			case strings.HasPrefix(line, "ext-"):
				return nil, fmt.Errorf("LFS pointer extensions unsupported")
			}
		}
		if !lfsOID.MatchString(oid) || size < 0 || size > m.LFSMaxObjectBytes {
			return nil, fmt.Errorf("invalid or oversized LFS pointer")
		}
		if old, ok := objects[oid]; ok && old != size {
			return nil, fmt.Errorf("inconsistent LFS pointer")
		}
		objects[oid] = size
	}
	if cmd.Wait() != nil {
		return nil, fmt.Errorf("Git pointer read failed")
	}
	return objects, nil
}
func (m *Manager) configureLFS() error {
	for _, item := range []struct {
		name   string
		target *int64
	}{{"AWSPORTAL_MIRROR_LFS_MAX_OBJECT_BYTES", &m.LFSMaxObjectBytes}, {"AWSPORTAL_MIRROR_LFS_MAX_SYNC_BYTES", &m.LFSMaxSyncBytes}} {
		if value := os.Getenv(item.name); value != "" {
			n, e := strconv.ParseInt(value, 10, 64)
			if e != nil || n < 1 || n > 1<<50 {
				return fmt.Errorf("invalid LFS byte limit")
			}
			*item.target = n
		}
	}
	for _, value := range strings.FieldsFunc(os.Getenv("AWSPORTAL_MIRROR_LFS_DOWNLOAD_ORIGINS"), func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		u, e := validDownloadURL(value)
		if e != nil || u.RawQuery != "" || u.ForceQuery || (u.Path != "" && u.Path != "/") {
			return fmt.Errorf("LFS origins must be HTTPS hosts with optional port")
		}
		m.LFSDownloadOrigins = append(m.LFSDownloadOrigins, origin(u))
	}
	return nil
}

type contextFileReader struct {
	ctx  context.Context
	file io.Reader
}

func (r contextFileReader) Read(b []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.file.Read(b)
}
func verifiedLFSFile(ctx context.Context, path, oid string, size int64) bool {
	f, e := os.Open(path)
	if e != nil {
		return false
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Size() != size {
		return false
	}
	hash := sha256.New()
	n, e := io.Copy(hash, contextFileReader{ctx: ctx, file: f})
	return e == nil && n == size && hex.EncodeToString(hash.Sum(nil)) == oid
}
func (m *Manager) lfsPath(id int64, oid string) string {
	return filepath.Join(m.Path(id), "awsportal-lfs", "objects", oid[:2], oid)
}
func (m *Manager) manifestPath(id int64) string {
	return filepath.Join(m.Path(id), "awsportal-lfs", "manifest.json")
}
func (m *Manager) manifest(id int64) (map[string]int64, error) {
	v := map[string]int64{}
	f, e := os.Open(m.manifestPath(id))
	if os.IsNotExist(e) {
		return v, nil
	}
	if e != nil {
		return nil, e
	}
	defer f.Close()
	e = json.NewDecoder(io.LimitReader(f, 16<<20)).Decode(&v)
	return v, e
}
func atomicJSON(path string, v any) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".manifest-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = json.NewEncoder(f).Encode(v); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
func validDownloadURL(raw string) (*url.URL, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("invalid LFS download URL")
	}
	return u, nil
}
func origin(u *url.URL) string { return strings.ToLower(u.Scheme + "://" + u.Host) }
func (m *Manager) lfsHTTP() (*http.Client, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if m.CAFile != "" {
		roots, e := x509.SystemCertPool()
		if e != nil {
			roots = x509.NewCertPool()
		}
		pem, e := os.ReadFile(m.CAFile)
		if e != nil || !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("invalid LFS CA")
		}
		tlsConfig.RootCAs = roots
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig, Proxy: nil, ResponseHeaderTimeout: 30 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
func (m *Manager) syncLFS(ctx context.Context, id int64, upstream, auth string) error {
	auth = strings.TrimPrefix(auth, "Authorization: ")
	pointers, e := m.pointers(ctx, m.Path(id))
	if e != nil {
		return e
	}
	known, e := m.manifest(id)
	if e != nil {
		return e
	}
	pending := []lfsObject{}
	total := int64(0)
	for oid, size := range pointers {
		if e := ctx.Err(); e != nil {
			return e
		}
		if old, ok := known[oid]; ok && old != size {
			return fmt.Errorf("LFS size conflict")
		}
		if verifiedLFSFile(ctx, m.lfsPath(id, oid), oid, size) {
			continue
		}
		if size > m.LFSMaxSyncBytes-total {
			return fmt.Errorf("LFS sync byte limit exceeded")
		}
		total += size
		pending = append(pending, lfsObject{OID: oid, Size: size})
	}
	client, e := m.lfsHTTP()
	if e != nil {
		return e
	}
	defer client.CloseIdleConnections()
	up, _ := url.Parse(upstream)
	for start := 0; start < len(pending); start += 100 {
		end := start + 100
		if end > len(pending) {
			end = len(pending)
		}
		want := pending[start:end]
		body, _ := json.Marshal(lfsBatch{Operation: "download", Transfers: []string{"basic"}, HashAlgo: "sha256", Objects: want})
		req, e := http.NewRequestWithContext(ctx, "POST", upstream+"/info/lfs/objects/batch", bytes.NewReader(body))
		if e != nil {
			return fmt.Errorf("LFS request failed")
		}
		req.Header.Set("Accept", lfsMedia)
		req.Header.Set("Content-Type", lfsMedia)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		response, e := client.Do(req)
		if e != nil {
			return fmt.Errorf("LFS batch unavailable")
		}
		var batch lfsBatch
		if response.StatusCode != 200 {
			response.Body.Close()
			return fmt.Errorf("LFS batch rejected")
		}
		e = json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&batch)
		response.Body.Close()
		if e != nil || batch.Transfer != "" && batch.Transfer != "basic" {
			return fmt.Errorf("invalid LFS batch")
		}
		actions := map[string]lfsObject{}
		for _, o := range batch.Objects {
			actions[o.OID] = o
		}
		for _, object := range want {
			o, ok := actions[object.OID]
			a, has := o.Actions["download"]
			if !ok || !has || o.Error != nil || o.Size != object.Size {
				return fmt.Errorf("LFS object unavailable")
			}
			target, e := validDownloadURL(a.Href)
			if e != nil {
				return e
			}
			allowed := origin(target) == origin(up)
			for _, v := range m.LFSDownloadOrigins {
				if origin(target) == v {
					allowed = true
				}
			}
			if !allowed {
				return fmt.Errorf("LFS download origin not allowed")
			}
			req, e = http.NewRequestWithContext(ctx, "GET", target.String(), nil)
			if e != nil {
				return fmt.Errorf("LFS download failed")
			}
			if origin(target) == origin(up) && strings.HasPrefix(target.Path, up.Path+"/") && auth != "" {
				req.Header.Set("Authorization", auth)
			}
			for key, value := range a.Header {
				if strings.ContainsAny(key+value, "\r\n") || len(key)+len(value) > 16384 {
					return fmt.Errorf("invalid LFS action header")
				}
				switch strings.ToLower(key) {
				case "host", "cookie", "proxy-authorization", "content-length", "connection", "transfer-encoding":
					return fmt.Errorf("forbidden LFS action header")
				}
				req.Header.Set(key, value)
			}
			response, e = client.Do(req)
			if e != nil {
				return fmt.Errorf("LFS download failed")
			}
			if response.StatusCode != 200 {
				response.Body.Close()
				return fmt.Errorf("LFS download rejected")
			}
			path := m.lfsPath(id, object.OID)
			if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
				response.Body.Close()
				return e
			}
			f, e := os.CreateTemp(filepath.Dir(path), ".download-")
			if e != nil {
				response.Body.Close()
				return e
			}
			hash := sha256.New()
			n, copyErr := io.Copy(io.MultiWriter(f, hash), io.LimitReader(response.Body, object.Size+1))
			response.Body.Close()
			syncErr := f.Sync()
			closeErr := f.Close()
			if copyErr != nil || syncErr != nil || closeErr != nil || n != object.Size || hex.EncodeToString(hash.Sum(nil)) != object.OID {
				os.Remove(f.Name())
				return fmt.Errorf("LFS content verification failed")
			}
			if e = os.Rename(f.Name(), path); e != nil {
				os.Remove(f.Name())
				return e
			}
		}
	}
	for oid, size := range pointers {
		known[oid] = size
	}
	if len(known) > 100000 {
		return fmt.Errorf("LFS manifest limit exceeded")
	}
	return atomicJSON(m.manifestPath(id), known)
}
func (m *Manager) publishRefs(ctx context.Context, path, branch string) error {
	data, e := m.output(ctx, path, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/", "refs/tags/", stagePrefix)
	if e != nil {
		return e
	}
	old, next := map[string]string{}, map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		if strings.HasPrefix(f[0], stagePrefix) {
			next["refs/"+strings.TrimPrefix(f[0], stagePrefix)] = f[1]
		} else {
			old[f[0]] = f[1]
		}
	}
	if _, ok := next["refs/heads/"+branch]; !ok {
		return fmt.Errorf("default branch unavailable")
	}
	commands := "start\n"
	for ref, oid := range next {
		if prev, ok := old[ref]; ok {
			commands += "update " + ref + " " + oid + " " + prev + "\n"
		} else {
			commands += "create " + ref + " " + oid + "\n"
		}
	}
	for ref, oid := range old {
		if _, ok := next[ref]; !ok {
			commands += "delete " + ref + " " + oid + "\n"
		}
	}
	commands += "prepare\ncommit\n"
	cmd := exec.CommandContext(ctx, m.Git, "-C", path, "update-ref", "--stdin")
	cmd.Env = gitEnv(nil)
	boundProcess(cmd)
	cmd.Stdin = strings.NewReader(commands)
	if cmd.Run() != nil {
		return fmt.Errorf("Git reference publication failed")
	}
	return nil
}
func lfsJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", lfsMedia)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func (m *Manager) ServeLFS(w http.ResponseWriter, r *http.Request, id int64, suffix, baseURL string) {
	if r.URL.RawQuery != "" {
		http.Error(w, "LFS query forbidden", 403)
		return
	}
	if suffix != "info/lfs/objects/batch" && !(r.Method == "GET" && strings.HasPrefix(suffix, "info/lfs/objects/")) {
		http.Error(w, "read-only LFS endpoint", 403)
		return
	}
	select {
	case m.slots <- struct{}{}:
		defer func() { <-m.slots }()
	default:
		http.Error(w, "mirror busy", 503)
		return
	}
	known, e := m.manifest(id)
	if e != nil {
		http.Error(w, "LFS manifest unavailable", 503)
		return
	}
	if suffix == "info/lfs/objects/batch" {
		media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if r.Method != "POST" || media != lfsMedia {
			http.Error(w, "read-only LFS batch", 403)
			return
		}
		var batch lfsBatch
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&batch) != nil {
			http.Error(w, "invalid LFS batch", 400)
			return
		}
		if batch.Operation != "download" {
			http.Error(w, "LFS upload forbidden", 403)
			return
		}
		if len(batch.Objects) > 1000 || batch.HashAlgo != "" && batch.HashAlgo != "sha256" {
			http.Error(w, "invalid LFS batch", 422)
			return
		}
		if len(batch.Transfers) > 0 {
			basic := false
			for _, v := range batch.Transfers {
				basic = basic || v == "basic"
			}
			if !basic {
				http.Error(w, "basic transfer required", 422)
				return
			}
		}
		if _, e := validDownloadURL(baseURL); e != nil {
			http.Error(w, "configure portal HTTPS URL in site settings", 503)
			return
		}
		objects := []lfsObject{}
		for _, o := range batch.Objects {
			answer := lfsObject{OID: o.OID, Size: o.Size}
			size, ok := known[o.OID]
			if !ok || !lfsOID.MatchString(o.OID) || size != o.Size {
				answer.Error = &lfsError{404, "Object not available in this mirror"}
			} else {
				answer.Authenticated = true
				answer.Actions = map[string]lfsAction{"download": {Href: strings.TrimSuffix(baseURL, "/") + "/git/mirrors/" + strconv.FormatInt(id, 10) + "/info/lfs/objects/" + o.OID}}
			}
			objects = append(objects, answer)
		}
		lfsJSON(w, 200, lfsBatch{Transfer: "basic", HashAlgo: "sha256", Objects: objects})
		return
	}
	oid := strings.TrimPrefix(suffix, "info/lfs/objects/")
	size, ok := known[oid]
	if !ok || !lfsOID.MatchString(oid) {
		http.NotFound(w, r)
		return
	}
	f, e := os.Open(m.lfsPath(id, oid))
	if e != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Size() != size {
		http.Error(w, "LFS object unavailable", 503)
		return
	}
	http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Minute))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, oid, info.ModTime(), f)
}
