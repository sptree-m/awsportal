package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sptree-m/awsportal/internal/store"
)

func TestEnvironmentJobHistoryOwnerAndAdmin(t *testing.T) {
	a, _ := newHandlerTestApp(t)
	ctx := context.Background()
	for _, n := range []struct{ name, role string }{{"admin", "portal_admin"}, {"alice", "user"}, {"bob", "user"}} {
		if err := a.db.CreateUser(ctx, n.name, "x", n.role, ""); err != nil {
			t.Fatal(err)
		}
	}
	admin, _ := a.db.UserByName(ctx, "admin")
	alice, _ := a.db.UserByName(ctx, "alice")
	bob, _ := a.db.UserByName(ctx, "bob")
	if _, err := a.db.DB.Exec(`INSERT INTO groups(id,name) VALUES(1,'team'); INSERT INTO instances(id,instance_id,name,dcv_host) VALUES(1,'i-test','pilot','pilot.example'); INSERT INTO environments(id,name,mode,group_id,profile_id) VALUES(1,'pilot','shared',1,'shared-cpu-v1');`); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("c", 32)
	raw, _ := json.Marshal(store.JobMeasurement{JobID: id, UserID: alice.ID, State: "RUNNING", Quality: "ok"})
	if _, err := a.db.DB.Exec(`INSERT INTO managed_jobs VALUES(1,?,1,?,'boot-test','RUNNING',1,0,?,?)`, id, alice.ID, time.Now().Add(-2*time.Minute).Unix(), string(raw)); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /environments", a.require(a.environmentPage))
	mux.HandleFunc("GET /admin/environments", a.require(a.environmentAdminPage))
	for _, u := range []store.User{alice, bob, admin} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, requestAs(a, u, "GET", "/environments", nil))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		has := strings.Contains(w.Body.String(), id)
		if has != (u.ID == alice.ID || u.ID == admin.ID) {
			t.Fatal("job ownership leak", u.Username)
		}
		if has && !strings.Contains(w.Body.String(), "計測が古い") {
			t.Fatal("stale job not marked")
		}
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, requestAs(a, admin, "GET", "/admin/environments", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), id) {
		t.Fatal(w.Code, w.Body.String())
	}
}
