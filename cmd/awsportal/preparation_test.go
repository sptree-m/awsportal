package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPreparationHostCapacity(t *testing.T) {
	caPath, hostsPath, _ := preparationFixture(t)
	for _, count := range []int{64, 128, 129} {
		hosts := make([]preparationHost, count)
		for i := range hosts {
			hosts[i] = preparationHost{Name: fmt.Sprintf("dcv%03d.company.example", i), IP: fmt.Sprintf("192.0.2.%d", i+1)}
		}
		raw, _ := json.Marshal(map[string]any{"hosts": hosts})
		if err := os.WriteFile(hostsPath, raw, 0600); err != nil {
			t.Fatal(err)
		}
		p, err := loadClientPreparation(caPath, hostsPath)
		if count > 128 {
			if err == nil {
				t.Fatal("oversized inventory accepted")
			}
			continue
		}
		if err != nil || len(p.Hosts) != count {
			t.Fatal("host capacity", count, err)
		}
		zr, err := zip.NewReader(bytes.NewReader(p.bundle), int64(len(p.bundle)))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, f := range zr.File {
			if f.Name != "client-setup.json" {
				continue
			}
			r, _ := f.Open()
			var config clientPreparation
			err := json.NewDecoder(r).Decode(&config)
			r.Close()
			if err != nil || len(config.Hosts) != count {
				t.Fatal("ZIP lost mappings", count, err)
			}
			found = true
		}
		if !found {
			t.Fatal("missing inventory in ZIP")
		}
	}
}

func TestPreparationGeneratorRequiresPortalAdmin(t *testing.T) {
	a, _ := newHandlerTestApp(t)
	w := httptest.NewRecorder()
	a.require(a.preparationGeneratorDownload)(w, httptest.NewRequest("GET", "/admin/preparation/generate-preparation.py", nil))
	if w.Code != 303 {
		t.Fatal("anonymous tool access", w.Code)
	}
	for _, role := range []string{"user", "group_admin", "portal_admin"} {
		if err := a.db.CreateUser(context.Background(), role, "x", role, ""); err != nil {
			t.Fatal(err)
		}
		u, err := a.db.UserByName(context.Background(), role)
		if err != nil {
			t.Fatal(err)
		}
		w = httptest.NewRecorder()
		a.require(a.preparationGeneratorDownload)(w, requestAs(a, u, "GET", "/admin/preparation/generate-preparation.py", nil))
		if role != "portal_admin" {
			if w.Code != 403 {
				t.Fatal(role, w.Code)
			}
		} else if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), preparationGenerator) || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("admin tool download", w.Code)
		}
	}
}

func preparationFixture(t *testing.T) (string, string, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	dir := t.TempDir()
	caPath, hostsPath := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "hosts.json")
	if err := os.WriteFile(caPath, ca, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hostsPath, []byte(`{"hosts":[{"name":"portal.company.example","ip":"192.0.2.10"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	return caPath, hostsPath, ca
}

func TestPreparationPublicDownloadsAndLogin(t *testing.T) {
	a, _ := newHandlerTestApp(t)
	caPath, hostsPath, ca := preparationFixture(t)
	p, err := loadClientPreparation(caPath, hostsPath)
	if err != nil {
		t.Fatal(err)
	}
	a.preparation = p
	for _, path := range []string{"/preparation/ca.crt", "/preparation/client-setup.zip"} {
		w := httptest.NewRecorder()
		a.preparationDownload(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") {
			t.Fatal(path, w.Code, w.Header())
		}
		if path == "/preparation/ca.crt" && !bytes.Equal(w.Body.Bytes(), ca) {
			t.Fatal("CA changed")
		}
	}
	zr, err := zip.NewReader(bytes.NewReader(p.bundle), int64(len(p.bundle)))
	if err != nil || len(zr.File) != 5 {
		t.Fatal("incomplete preparation ZIP", err)
	}
	for _, f := range zr.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(r)
		r.Close()
		if err != nil || bytes.Contains(b, []byte("-----BEGIN PRIVATE KEY-----")) {
			t.Fatal("invalid public bundle", f.Name, err)
		}
	}
	w := httptest.NewRecorder()
	a.loginPage(w, httptest.NewRequest("GET", "/login", nil))
	if !strings.Contains(w.Body.String(), p.Fingerprint) || !strings.Contains(w.Body.String(), `href="/preparation/client-setup.zip"`) || !strings.Contains(w.Body.String(), "portal.company.example") {
		t.Fatal("login lacks configured preparation")
	}
	a.preparation = nil
	w = httptest.NewRecorder()
	a.loginPage(w, httptest.NewRequest("GET", "/login", nil))
	if !strings.Contains(w.Body.String(), "事前準備") || strings.Contains(w.Body.String(), `href="/preparation/`) {
		t.Fatal("unconfigured preparation links")
	}
	w = httptest.NewRecorder()
	a.preparationDownload(w, httptest.NewRequest("GET", "/preparation/client-setup.zip", nil))
	if w.Code != 404 {
		t.Fatal("unconfigured download", w.Code)
	}
}

func TestPreparationRejectsPrivateMaterialAndUnsafeHosts(t *testing.T) {
	caPath, hostsPath, ca := preparationFixture(t)
	for _, invalid := range [][]byte{
		append(append([]byte{}, ca...), []byte("-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----\n")...),
		append([]byte("-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----\n"), ca...),
		append(append([]byte{}, ca...), ca...),
		[]byte("not a certificate"),
	} {
		if err := os.WriteFile(caPath, invalid, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadClientPreparation(caPath, hostsPath); err == nil {
			t.Fatal("invalid/private certificate accepted")
		}
	}
	if err := os.WriteFile(caPath, ca, 0600); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"hosts":[]}`,
		`{"hosts":[{"name":"portal.example\nmalicious","ip":"192.0.2.10"}]}`,
		`{"hosts":[{"name":"portal.example","ip":"192.0.2.10"},{"name":"PORTAL.example","ip":"192.0.2.20"}]}`,
		`{"hosts":[{"name":"portal.example","ip":"0.0.0.0"}]}`,
		`{"hosts":[{"name":"portal.example","ip":"example.com"}]}`,
		`{"hosts":[{"name":"portal.example","ip":"192.0.2.10"}],"private_key":"secret"}`,
		`{"hosts":[{"name":"portal.example","ip":"192.0.2.10"}]} {}`,
	} {
		if err := os.WriteFile(hostsPath, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadClientPreparation(caPath, hostsPath); err == nil {
			t.Fatal("unsafe hosts accepted", raw)
		}
	}
	if _, err := loadClientPreparation(caPath, ""); err == nil {
		t.Fatal("partial config accepted")
	}
}
