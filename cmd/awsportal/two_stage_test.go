package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTwoStageAdminAndSettlementOwnerBoundaries(t *testing.T) {
	a, _ := newHandlerTestApp(t)
	ctx := context.Background()
	for _, u := range []struct{ name, role string }{{"admin", "portal_admin"}, {"alice", "user"}, {"bob", "user"}} {
		if err := a.db.CreateUser(ctx, u.name, "x", u.role, ""); err != nil {
			t.Fatal(err)
		}
	}
	admin, _ := a.db.UserByName(ctx, "admin")
	alice, _ := a.db.UserByName(ctx, "alice")
	bob, _ := a.db.UserByName(ctx, "bob")
	if _, err := a.db.DB.Exec(`INSERT INTO billing_runs(id,source_version,scope,policy,formula,state,created_at) VALUES('run','source','{}','{}','test','FINAL',1);INSERT INTO billing_allocations VALUES('run',?,'1000000'),('run',?,'2000000')`, alice.ID, bob.ID); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/two-stage", a.require(a.stageTwoPage))
	mux.HandleFunc("POST /admin/two-stage", a.require(a.stageTwoChange))
	mux.HandleFunc("GET /settlements", a.require(a.settlements))
	mux.HandleFunc("GET /settlements.csv", a.require(a.settlements))
	for _, method := range []string{"GET", "POST"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, requestAs(a, alice, method, "/admin/two-stage", nil))
		if w.Code != 403 {
			t.Fatal(method, w.Code)
		}
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, requestAs(a, admin, "GET", "/admin/two-stage", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Golden AMI") {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, path := range []string{"/settlements", "/settlements.csv"} {
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, requestAs(a, alice, "GET", path, nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), "alice") || strings.Contains(w.Body.String(), "bob") {
			t.Fatal("settlement owner leak", w.Code, w.Body.String())
		}
	}
}
