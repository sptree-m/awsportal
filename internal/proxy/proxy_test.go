package proxy

import (
	"bufio"
	"context"
	"encoding/base64"
	"github.com/sptree-m/awsportal/internal/store"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Handler, string) {
	t.Helper()
	s, e := store.Open(filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	s.Migrate(context.Background())
	s.CreateUser(context.Background(), "alice", "x", "user", "")
	u, _ := s.UserByName(context.Background(), "alice")
	tok, e := s.IssueProxyCredential(context.Background(), u.ID, "test", 1)
	if e != nil {
		t.Fatal(e)
	}
	s.SetProxyEnabled(context.Background(), true)
	s.SaveProxyRule(context.Background(), store.ProxyRule{Name: "test", Scope: "all", Domain: "example.com", Ports: "80,443", Methods: "GET,CONNECT", Effect: "allow", Enabled: true})
	return New(s), "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:"+tok))
}
func TestPublicDestination(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "100.100.100.200", "192.168.1.1", "::1", "::ffff:127.0.0.1", "fe80::1", "fd00:ec2::254", "64:ff9b::a00:1", "2002:7f00:1::"} {
		if PublicIP(net.ParseIP(ip)) {
			t.Fatal(ip)
		}
	}
	if !PublicIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("public rejected")
	}
	if c, e := publicDial(context.Background(), "tcp", "127.0.0.1:80"); e == nil {
		c.Close()
		t.Fatal("private dial succeeded")
	}
}
func TestHTTPAuthenticationPolicyAndForwarding(t *testing.T) {
	p, auth := fixture(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("X-Remove") != "" || r.Host != "example.com:80" {
			t.Error("credential/header/host leak")
		}
		w.Header().Set("X-Upstream", "yes")
		io.WriteString(w, "hello")
	}))
	defer up.Close()
	p.transport.DialContext = func(ctx context.Context, n, a string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(up.URL, "http://"))
	}
	p.dial = p.transport.DialContext
	for _, tt := range []struct {
		method, target, auth string
		status               int
	}{{"GET", "http://example.com/path", "", 407}, {"PUT", "http://example.com/path", auth, 403}, {"GET", "http://evil.com/", auth, 403}, {"GET", "http://169.254.169.254/", auth, 403}, {"GET", "http://example.com/path", auth, 200}} {
		r := httptest.NewRequest(tt.method, tt.target, nil)
		r.Header.Set("Proxy-Authorization", tt.auth)
		r.Header.Set("Connection", "X-Remove")
		r.Header.Set("X-Remove", "secret")
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		if w.Code != tt.status {
			t.Fatalf("%+v: %d %s", tt, w.Code, w.Body.String())
		}
		if tt.status == 200 && w.Body.String() != "hello" {
			t.Fatal("not forwarded")
		}
	}
}
func TestConnectTunnel(t *testing.T) {
	p, auth := fixture(t)
	p.dial = func(ctx context.Context, n, a string) (net.Conn, error) {
		c, s := net.Pipe()
		go func() { defer s.Close(); b := make([]byte, 4); io.ReadFull(s, b); s.Write(b) }()
		return c, nil
	}
	server := httptest.NewServer(p)
	defer server.Close()
	c, e := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	io.WriteString(c, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\nProxy-Authorization: "+auth+"\r\n\r\nping")
	br := bufio.NewReader(c)
	resp, e := http.ReadResponse(br, &http.Request{Method: "CONNECT"})
	if e != nil || resp.StatusCode != 200 {
		t.Fatalf("CONNECT %v %v", resp, e)
	}
	b := make([]byte, 4)
	if _, e = io.ReadFull(br, b); e != nil || string(b) != "ping" {
		t.Fatalf("buffered tunnel: %s %v", b, e)
	}
}

func TestHTTPSViaConnect(t *testing.T) {
	p, auth := fixture(t)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "" {
			t.Error("proxy secret leaked")
		}
		io.WriteString(w, "encrypted response")
	}))
	defer upstream.Close()
	p.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(upstream.URL, "https://"))
	}
	server := httptest.NewServer(p)
	defer server.Close()
	proxyURL, _ := url.Parse(server.URL)
	tr := upstream.Client().Transport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyURL(proxyURL)
	tr.ProxyConnectHeader = http.Header{"Proxy-Authorization": {auth}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 3 * time.Second}
	resp, e := client.Get("https://example.com/")
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(resp.Body)
	if e != nil || string(b) != "encrypted response" {
		t.Fatalf("HTTPS %s %v", b, e)
	}
}
