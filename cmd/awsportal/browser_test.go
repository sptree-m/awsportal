package main

import (
	"context"
	awsapi "github.com/sptree-m/awsportal/internal/aws"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"
)

type browserCost struct{}

func (browserCost) UserCosts(_ context.Context, _ string, start, end time.Time) (awsapi.CostReport, error) {
	return awsapi.CostReport{Start: start.Format("2006-01-02"), End: end.Format("2006-01-02"), Total: 123, Rows: []awsapi.CostRow{{User: "admin", Compute: 100, Storage: 23, Total: 123}}}, nil
}

type browserEC2 struct {
	mu    sync.Mutex
	state string
}

func (f *browserEC2) Start(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = "pending"
	return nil
}
func (f *browserEC2) Stop(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = "stopping"
	return nil
}
func (f *browserEC2) State(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state == "pending" {
		f.state = "running"
	} else if f.state == "stopping" {
		f.state = "stopped"
	}
	return f.state, nil
}
func (f *browserEC2) States(context.Context, []string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return map[string]string{"i-dev": f.state, "i-train": "running"}, nil
}

// Exercise real server templates/headers and bundled htmx in Chromium. AWS is simulated.
func TestBrowserConsole(t *testing.T) {
	if os.Getenv("AWSPORTAL_BROWSER_TEST") != "1" {
		t.Skip("set AWSPORTAL_BROWSER_TEST=1 with Node/Playwright installed")
	}
	a, _ := newHandlerTestApp(t)
	ctx := context.Background()
	if err := a.db.CreateUser(ctx, "admin", "x", "portal_admin", ""); err != nil {
		t.Fatal(err)
	}
	admin, _ := a.db.UserByName(ctx, "admin")
	if _, err := a.db.DB.ExecContext(ctx, `INSERT INTO instances(instance_id,name,dcv_host) VALUES('i-dev','ADAS Development','dev.local'),('i-train','CV Training','train.local')`); err != nil {
		t.Fatal(err)
	}
	a.cost = browserCost{}
	a.ec2 = &browserEC2{state: "stopped"}
	a.sessions["browser-session"] = session{User: admin, Expires: time.Now().Add(time.Hour)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", a.require(a.dashboard))
	mux.HandleFunc("GET /instances", a.require(a.instancesPage))
	mux.HandleFunc("GET /instances/{id}/row", a.require(a.instanceRow))
	mux.HandleFunc("GET /instances/{id}", a.require(a.instanceDetail))
	mux.HandleFunc("POST /instance/{id}/{action}", a.require(a.instanceAction))
	mux.HandleFunc("GET /admin/users", a.require(a.adminUsers))
	mux.HandleFunc("GET /admin/audit", a.require(a.adminAudit))
	mux.HandleFunc("GET /mfa", a.require(a.mfaPage))
	mux.HandleFunc("GET /costs", a.require(a.costDashboard))
	mux.Handle("GET /static/", staticHandler(http.StripPrefix("/static/", http.FileServer(http.FS(web)))))
	server := httptest.NewServer(headers(mux))
	defer server.Close()
	cmd := exec.Command("node", "../../tests/browser-console.cjs", server.URL)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	t.Log(string(out))
	if err != nil {
		t.Fatal(err)
	}
}
