package main

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
)

type portalTransport struct {
	mode, addr string
	tlsConfig  *tls.Config
}

// Validate and load TLS before starting the database or background workers.
// HTTP is an explicit opt-in for a protected reverse-proxy backend.
func loadPortalTransport() (portalTransport, error) {
	mode := os.Getenv("AWSPORTAL_HTTP_MODE")
	if mode == "" {
		mode = "https"
	}
	t := portalTransport{mode: mode, addr: os.Getenv("AWSPORTAL_ADDR")}
	cert, key := os.Getenv("AWSPORTAL_TLS_CERT"), os.Getenv("AWSPORTAL_TLS_KEY")
	switch mode {
	case "https":
		if t.addr == "" {
			t.addr = ":443"
		}
		if cert == "" || key == "" {
			return t, fmt.Errorf("HTTPS requires both AWSPORTAL_TLS_CERT and AWSPORTAL_TLS_KEY; for a reverse-proxy backend explicitly set AWSPORTAL_HTTP_MODE=http")
		}
		pair, err := tls.LoadX509KeyPair(cert, key)
		if err != nil {
			return t, fmt.Errorf("load Portal TLS certificate/key: %w", err)
		}
		t.tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}
	case "http":
		if t.addr == "" {
			t.addr = "127.0.0.1:8080"
		}
		if cert != "" || key != "" {
			return t, fmt.Errorf("AWSPORTAL_TLS_CERT and AWSPORTAL_TLS_KEY must be unset in HTTP mode")
		}
	default:
		return t, fmt.Errorf("AWSPORTAL_HTTP_MODE must be https or http")
	}
	return t, nil
}

func (t portalTransport) serve(s *http.Server) error {
	if t.mode == "https" {
		// The certificate was loaded at startup; never fall back to plaintext.
		return s.ListenAndServeTLS("", "")
	}
	return s.ListenAndServe()
}
