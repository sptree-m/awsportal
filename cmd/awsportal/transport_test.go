package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func portalTestCertificate(t *testing.T) (string, string, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key")
	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certPEM)
	return certPath, keyPath, roots
}

func TestPortalTransport(t *testing.T) {
	cert, key, _ := portalTestCertificate(t)
	_, otherKey, _ := portalTestCertificate(t)
	for _, tc := range []struct {
		name, mode, addr, cert, key, wantAddr string
		wantError                             bool
	}{
		{name: "default HTTPS", cert: cert, key: key, wantAddr: ":443"},
		{name: "custom HTTPS", mode: "https", addr: "127.0.0.1:9443", cert: cert, key: key, wantAddr: "127.0.0.1:9443"},
		{name: "HTTP backend", mode: "http", wantAddr: "127.0.0.1:8080"},
		{name: "custom HTTP backend", mode: "http", addr: "127.0.0.1:8081", wantAddr: "127.0.0.1:8081"},
		{name: "missing TLS configuration", wantError: true},
		{name: "missing key", cert: cert, wantError: true},
		{name: "missing certificate", key: key, wantError: true},
		{name: "unreadable certificate", cert: cert + ".missing", key: key, wantError: true},
		{name: "invalid certificate", cert: key, key: key, wantError: true},
		{name: "mismatched key", cert: cert, key: otherKey, wantError: true},
		{name: "HTTP with TLS configuration", mode: "http", cert: cert, key: key, wantError: true},
		{name: "HTTP with partial TLS configuration", mode: "http", key: key, wantError: true},
		{name: "invalid mode", mode: "auto", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AWSPORTAL_HTTP_MODE", tc.mode)
			t.Setenv("AWSPORTAL_ADDR", tc.addr)
			t.Setenv("AWSPORTAL_TLS_CERT", tc.cert)
			t.Setenv("AWSPORTAL_TLS_KEY", tc.key)
			transport, err := loadPortalTransport()
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v, wantError = %v", err, tc.wantError)
			}
			if err != nil {
				return
			}
			if transport.addr != tc.wantAddr {
				t.Fatalf("addr = %q, want %q", transport.addr, tc.wantAddr)
			}
			if tc.mode == "http" {
				if transport.tlsConfig != nil {
					t.Fatal("HTTP must not configure TLS")
				}
			} else if transport.mode != "https" || transport.tlsConfig.MinVersion != tls.VersionTLS12 || len(transport.tlsConfig.Certificates) != 1 {
				t.Fatal("HTTPS must load the certificate and require TLS 1.2")
			}
		})
	}
}
