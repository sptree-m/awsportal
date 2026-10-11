package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sptree-m/awsportal/internal/store"
)

func TestEnvironmentUIReadOnlyPollAndAdminBoundary(t *testing.T) {
	a, ec2 := newHandlerTestApp(t)
	ctx := context.Background()
	for _, n := range []struct{ name, role string }{{"admin", "portal_admin"}, {"alice", "user"}} {
		if err := a.db.CreateUser(ctx, n.name, "x", n.role, ""); err != nil {
			t.Fatal(err)
		}
	}
	admin, _ := a.db.UserByName(ctx, "admin")
	alice, _ := a.db.UserByName(ctx, "alice")
	if _, err := a.db.DB.Exec(`INSERT INTO groups(id,name) VALUES(1,'team')`); err != nil {
		t.Fatal(err)
	}
	eid, err := a.db.CreateEnvironment(ctx, admin, store.Environment{Name: "Pilot", Mode: "shared", GroupID: 1, ProfileID: "shared-cpu-v1"}, "pilot")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.db.SetEnvironmentACL(ctx, admin, eid, "user", alice.ID, "environment.connect", false, "pilot"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /environments", a.require(a.environmentPage))
	mux.HandleFunc("POST /environments/{id}/connect", a.require(a.environmentConnect))
	mux.HandleFunc("POST /admin/environments", a.require(a.environmentAdminChange))
	mux.HandleFunc("GET /admin/environments", a.require(a.environmentAdminPage))
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		r := requestAs(a, alice, "GET", "/environments", nil)
		mux.ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "Pilot") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	var count int
	a.db.DB.QueryRow(`SELECT COUNT(*) FROM connection_requests`).Scan(&count)
	if count != 0 || len(ec2.started) > 0 || len(ec2.stopped) > 0 {
		t.Fatal("poll mutated state")
	}
	for _, u := range []store.User{alice, admin} {
		w := httptest.NewRecorder()
		body := url.Values{"operation": {"auto-terminate"}, "enabled": {"1"}}
		mux.ServeHTTP(w, requestAs(a, u, "POST", "/admin/environments", strings.NewReader(body.Encode())))
		want := 400
		if u.ID == alice.ID {
			want = 403
		}
		if w.Code != want {
			t.Fatal(w.Code, want)
		}
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, requestAs(a, admin, "GET", "/admin/environments", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	req, err := a.db.RequestEnvironment(ctx, alice, eid, "request-key", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if req.State != "WAITING" {
		t.Fatal(req)
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, requestAs(a, alice, "GET", "/environments", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "WAITING") {
		t.Fatal(w.Code, w.Body.String())
	}
}
