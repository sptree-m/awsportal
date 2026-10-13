package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"embed"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

//go:embed preparation/*
var preparationFiles embed.FS

type preparationHost struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
}

type clientPreparation struct {
	Hosts       []preparationHost `json:"hosts"`
	Fingerprint string            `json:"ca_sha256"`
	certificate []byte
	bundle      []byte
}

var preparationHostname = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

func loadClientPreparation(caPath, hostsPath string) (*clientPreparation, error) {
	if caPath == "" && hostsPath == "" {
		return nil, nil
	}
	if caPath == "" || hostsPath == "" {
		return nil, fmt.Errorf("both preparation CA and hosts files are required")
	}
	ca, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("read preparation CA: %w", err)
	}
	if len(ca) > 64<<10 || !bytes.HasPrefix(bytes.TrimSpace(ca), []byte("-----BEGIN CERTIFICATE-----")) {
		return nil, fmt.Errorf("preparation CA must be one public PEM certificate")
	}
	block, rest := pem.Decode(ca)
	if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("preparation CA must contain only one public certificate, no private key")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !cert.IsCA || !cert.BasicConstraintsValid || time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) {
		return nil, fmt.Errorf("preparation CA must be a currently valid CA certificate")
	}
	raw, err := os.ReadFile(hostsPath)
	if err != nil {
		return nil, fmt.Errorf("read preparation hosts: %w", err)
	}
	if len(raw) > 16<<10 {
		return nil, fmt.Errorf("preparation hosts file too large")
	}
	p := &clientPreparation{certificate: pem.EncodeToMemory(block)}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var settings struct {
		Hosts []preparationHost `json:"hosts"`
	}
	if err := dec.Decode(&settings); err != nil {
		return nil, fmt.Errorf("invalid preparation hosts JSON: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("preparation hosts must be one JSON document")
	}
	if len(settings.Hosts) == 0 || len(settings.Hosts) > 32 {
		return nil, fmt.Errorf("preparation hosts requires 1 to 32 entries")
	}
	seen := map[string]bool{}
	for _, h := range settings.Hosts {
		h.Name = strings.ToLower(h.Name)
		ip := net.ParseIP(h.IP)
		if len(h.Name) > 253 || !preparationHostname.MatchString(h.Name) || net.ParseIP(h.Name) != nil || ip == nil || seen[h.Name] || ip.IsUnspecified() || ip.IsMulticast() {
			return nil, fmt.Errorf("invalid or duplicate preparation host mapping")
		}
		seen[h.Name] = true
		h.IP = ip.String()
		p.Hosts = append(p.Hosts, h)
	}
	digest := sha256.Sum256(cert.Raw)
	p.Fingerprint = strings.ToUpper(hex.EncodeToString(digest[:]))
	config, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := map[string][]byte{"ca.crt": p.certificate, "client-setup.json": config}
	for _, name := range []string{"setup-windows.ps1", "setup-unix.py", "README.txt"} {
		files[name], err = preparationFiles.ReadFile("preparation/" + name)
		if err != nil {
			return nil, err
		}
	}
	for _, name := range []string{"ca.crt", "client-setup.json", "setup-windows.ps1", "setup-unix.py", "README.txt"} {
		f, err := zw.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(files[name]); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	p.bundle = buf.Bytes()
	return p, nil
}

func (a *app) preparationDownload(w http.ResponseWriter, r *http.Request) {
	if a.preparation == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	var data []byte
	var name, kind string
	switch r.URL.Path {
	case "/preparation/ca.crt":
		data, name, kind = a.preparation.certificate, "awsportal-ca.crt", "application/x-pem-file"
	case "/preparation/client-setup.zip":
		data, name, kind = a.preparation.bundle, "awsportal-client-setup.zip", "application/zip"
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Write(data)
}
