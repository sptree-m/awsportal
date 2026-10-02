// Package proxy provides an authenticated, default-deny explicit forward proxy.
package proxy

import (
	"context"
	"encoding/base64"
	"errors"
	"github.com/sptree-m/awsportal/internal/store"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

type Handler struct {
	DB        *store.Store
	transport *http.Transport
	slots     chan struct{}
	dial      func(context.Context, string, string) (net.Conn, error)
}

func New(db *store.Store) *Handler {
	return &Handler{DB: db, slots: make(chan struct{}, 64), dial: publicDial, transport: &http.Transport{Proxy: nil, DialContext: publicDial, DisableKeepAlives: true, ResponseHeaderTimeout: 20 * time.Second, MaxResponseHeaderBytes: 1 << 20}}
}

var blocked = []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "::/96", "::ffff:0:0/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2002::/16", "fc00::/7", "fe80::/10", "ff00::/8"}

func PublicIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	a = a.Unmap()
	if !a.IsGlobalUnicast() {
		return false
	}
	for _, s := range blocked {
		if netip.MustParsePrefix(s).Contains(a) {
			return false
		}
	}
	return true
}
func publicDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, e := net.SplitHostPort(address)
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ips, e := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if e != nil || len(ips) == 0 {
		return nil, errors.New("DNS failed")
	}
	for _, ip := range ips {
		if !PublicIP(ip) {
			return nil, errors.New("non-public destination")
		}
	}
	var last error
	for _, ip := range ips {
		c, e := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), port))
		if e == nil {
			return c, nil
		}
		last = e
	}
	return nil, last
}
func stripHop(h http.Header) {
	for _, line := range h.Values("Connection") {
		for _, x := range strings.Split(line, ",") {
			h.Del(strings.TrimSpace(x))
		}
	}
	for _, x := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "TE", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.Del(x)
	}
}
func (p *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !p.DB.ProxyEnabled(r.Context()) {
		http.Error(w, "proxy disabled", 503)
		return
	}
	raw := r.Header.Get("Proxy-Authorization")
	username, token, ok := "", "", false
	if strings.HasPrefix(raw, "Basic ") {
		if b, e := base64.StdEncoding.DecodeString(strings.TrimPrefix(raw, "Basic ")); e == nil {
			username, token, ok = strings.Cut(string(b), ":")
		}
	}
	u, e := p.DB.ProxyUser(r.Context(), username, token)
	if !ok || e != nil {
		w.Header().Set("Proxy-Authenticate", `Basic realm="awsportal"`)
		http.Error(w, "proxy credential required", 407)
		return
	}
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		http.Error(w, "proxy busy", 503)
		return
	}
	var host, port string
	if r.Method == "CONNECT" {
		host, port, e = net.SplitHostPort(r.Host)
		if e != nil || r.RequestURI != r.Host {
			http.Error(w, "invalid CONNECT authority", 400)
			return
		}
	} else {
		if r.URL.Scheme != "http" || r.URL.User != nil || !r.URL.IsAbs() {
			http.Error(w, "absolute HTTP URL required", 400)
			return
		}
		host = r.URL.Hostname()
		port = r.URL.Port()
		if port == "" {
			port = "80"
		}
		if r.Header.Get("Upgrade") != "" {
			http.Error(w, "upgrade unsupported", 400)
			return
		}
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	n, er := strconv.Atoi(port)
	if er != nil || n < 1 || n > 65535 {
		http.Error(w, "invalid port", 400)
		return
	}
	port = strconv.Itoa(n)
	if _, e = store.CanonicalDomain(host); e != nil || !p.DB.ProxyAllowed(r.Context(), u, host, port, r.Method) {
		p.DB.Audit(r.Context(), u.Username, "proxy.request", net.JoinHostPort(host, port), "deny", r.Method)
		http.Error(w, "destination denied", 403)
		return
	}
	audit := func(result string) {
		p.DB.Audit(r.Context(), u.Username, "proxy.request", net.JoinHostPort(host, port), result, r.Method)
	}
	if r.Method == "CONNECT" {
		upstream, e := p.dial(r.Context(), "tcp", net.JoinHostPort(host, port))
		if e != nil {
			audit("deny")
			http.Error(w, "destination unavailable", 502)
			return
		}
		defer upstream.Close()
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "HTTP/1 required", 505)
			return
		}
		client, buf, e := hj.Hijack()
		if e != nil {
			return
		}
		defer client.Close()
		deadline := time.Now().Add(5 * time.Minute)
		client.SetDeadline(deadline)
		upstream.SetDeadline(deadline)
		if _, e = buf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); e != nil {
			return
		}
		if e = buf.Flush(); e != nil {
			return
		}
		audit("allow")
		done := make(chan struct{})
		go func() {
			io.Copy(upstream, buf)
			if c, ok := upstream.(*net.TCPConn); ok {
				c.CloseWrite()
			}
			close(done)
		}()
		io.Copy(client, upstream)
		client.Close()
		upstream.Close()
		<-done
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	out := r.Clone(ctx)
	out.RequestURI = ""
	out.URL.Host = net.JoinHostPort(host, port)
	out.Host = out.URL.Host
	out.Header = r.Header.Clone()
	stripHop(out.Header)
	out.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	resp, e := p.transport.RoundTrip(out)
	if e != nil {
		audit("deny")
		http.Error(w, "upstream unavailable", 502)
		return
	}
	defer resp.Body.Close()
	stripHop(resp.Header)
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	audit("allow")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
