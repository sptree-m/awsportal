package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestInstanceAdminEndpointsAndDisabledOperations(t *testing.T) {
	a, ec2 := newHandlerTestApp(t)
	ctx := context.Background()
	for _, v := range []struct{ name, role string }{{"admin", "portal_admin"}, {"alice", "user"}, {"manager", "group_admin"}} {
		if e := a.db.CreateUser(ctx, v.name, "x", v.role, ""); e != nil {
			t.Fatal(e)
		}
	}
	admin, _ := a.db.UserByName(ctx, "admin")
	alice, _ := a.db.UserByName(ctx, "alice")
	manager, _ := a.db.UserByName(ctx, "manager")
	if _, e := a.db.DB.Exec(`INSERT INTO instances(instance_id,name,dcv_host) VALUES('i-dev','Development','host')`); e != nil {
		t.Fatal(e)
	}
	form := url.Values{"operation": {"disable"}, "instance_id": {"i-dev"}}.Encode()
	for _, u := range []string{alice.Username, manager.Username} {
		usr, _ := a.db.UserByName(ctx, u)
		r := requestAs(a, usr, "POST", "/admin/instances", strings.NewReader(form))
		w := httptest.NewRecorder()
		a.require(a.instanceAdminChange)(w, r)
		if w.Code != 403 {
			t.Fatalf("role %s admin change=%d", usr.Role, w.Code)
		}
	}
	r := requestAs(a, admin, "POST", "/admin/instances", strings.NewReader(form))
	r.Header.Set("HX-Request", "true")
	r.Header.Set("HX-Target", "instance-admin-live")
	w := httptest.NewRecorder()
	a.require(a.instanceAdminChange)(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "再有効化") {
		t.Fatalf("disable failed %d %s", w.Code, w.Body.String())
	}
	ec2.states["i-dev"] = "running"
	r = requestAs(a, admin, "POST", "/instance/i-dev/stop", nil)
	r.SetPathValue("id", "i-dev")
	r.SetPathValue("action", "stop")
	w = httptest.NewRecorder()
	a.require(a.instanceAction)(w, r)
	if w.Code != 403 || len(ec2.stopped) != 0 {
		t.Fatal("admin controlled disabled instance")
	}
	r = requestAs(a, admin, "GET", "/dcv/i-dev", nil)
	r.SetPathValue("id", "i-dev")
	w = httptest.NewRecorder()
	a.require(a.dcv)(w, r)
	if w.Code != 403 {
		t.Fatal("disabled DCV allowed")
	}
	// Persisted roles and account state override the existing session snapshot.
	r = requestAs(a, admin, "GET", "/admin/instances", nil)
	if _, e := a.db.DB.Exec("UPDATE users SET role='user' WHERE username='admin'"); e != nil {
		t.Fatal(e)
	}
	w = httptest.NewRecorder()
	a.require(a.instanceAdminPage)(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal("stale admin session allowed")
	}
}

func TestDCVPolicyHTTPRolesAndAdminUI(t *testing.T) {
	a, _ := newHandlerTestApp(t)
	ctx := context.Background()
	for _, role := range []string{"portal_admin", "user", "group_admin"} {
		if e := a.db.CreateUser(ctx, role, "x", role, ""); e != nil {
			t.Fatal(e)
		}
	}
	_, _ = a.db.DB.Exec(`INSERT INTO instances(instance_id,name,dcv_host) VALUES('i-a','A','host')`)
	form := url.Values{"operation": {"dcv-policy"}, "instance_id": {"i-a"}, "dcv_features": {"display"}}.Encode()
	for _, role := range []string{"portal_admin", "user", "group_admin"} {
		u, _ := a.db.UserByName(ctx, role)
		r := requestAs(a, u, "POST", "/admin/instances", strings.NewReader(form))
		w := httptest.NewRecorder()
		a.require(a.instanceAdminChange)(w, r)
		want := 403
		if role == "portal_admin" {
			want = 303
		}
		if w.Code != want {
			t.Fatal(role, w.Code, w.Body.String())
		}
	}
	admin, _ := a.db.UserByName(ctx, "portal_admin")
	r := requestAs(a, admin, "GET", "/admin/instances", nil)
	w := httptest.NewRecorder()
	a.require(a.instanceAdminPage)(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "スクリーンキャプチャ") || !strings.Contains(w.Body.String(), "未適用・新規接続停止") {
		t.Fatal(w.Code, w.Body.String())
	}
}
