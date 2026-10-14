package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Exercise production route registration, not the reduced handler-test mux.
func TestProductionStartup(t *testing.T) {
	if os.Getenv("AWSPORTAL_TEST_MAIN_CHILD") == "1" {
		main()
		return
	}
	cert, key, roots := portalTestCertificate(t)
	for _, mode := range []string{"https", "http"} {
		t.Run(mode, func(t *testing.T) {
			testProductionStartup(t, mode, cert, key, roots)
		})
	}
}

func testProductionStartup(t *testing.T, mode, cert, key string, roots *x509.CertPool) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestProductionStartup$")
	cmd.Env = append(os.Environ(), "AWSPORTAL_TEST_MAIN_CHILD=1", "AWSPORTAL_WEBAUTHN_ORIGIN=http://localhost:8080", "AWSPORTAL_DB="+filepath.Join(t.TempDir(), "main.db"), "AWSPORTAL_ADDR="+addr, "AWS_REGION=ap-northeast-1", "AWS_EC2_METADATA_DISABLED=true", "AWSPORTAL_MIRROR_ROOT=", "AWSPORTAL_PROXY_ADDR=")
	if mode == "https" {
		// Empty mode exercises the default HTTPS startup path.
		cmd.Env = append(cmd.Env, "AWSPORTAL_HTTP_MODE=", "AWSPORTAL_TLS_CERT="+cert, "AWSPORTAL_TLS_KEY="+key, "AWSPORTAL_WEBAUTHN_ORIGIN=https://localhost")
	} else {
		cmd.Env = append(cmd.Env, "AWSPORTAL_HTTP_MODE=http", "AWSPORTAL_TLS_CERT=", "AWSPORTAL_TLS_KEY=")
	}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() { cmd.Process.Kill(); <-done }()
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}}
	defer client.CloseIdleConnections()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			done <- err
			t.Fatalf("production startup failed: %v\n%s", err, output.String())
		default:
		}
		response, err := client.Get(mode + "://" + addr + "/healthz")
		if err == nil {
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode == 200 && string(body) == "ok" {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("production health endpoint did not become ready")
}
