package main

import (
	"bytes"
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
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestProductionStartup$")
	cmd.Env = append(os.Environ(), "AWSPORTAL_TEST_MAIN_CHILD=1", "AWSPORTAL_DB="+filepath.Join(t.TempDir(), "main.db"), "AWSPORTAL_ADDR="+addr, "AWS_REGION=ap-northeast-1", "AWS_EC2_METADATA_DISABLED=true", "AWSPORTAL_MIRROR_ROOT=", "AWSPORTAL_PROXY_ADDR=")
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() { cmd.Process.Kill(); <-done }()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			done <- err
			t.Fatalf("production startup failed: %v\n%s", err, output.String())
		default:
		}
		response, err := client.Get("http://" + addr + "/healthz")
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
