package main

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestProxyAdministration(t *testing.T) {
	a, _ := newHandlerTestApp(t)
	ctx := context.Background()
	a.db.CreateUser(ctx, "admin", "x", "portal_admin", "")
	a.db.CreateUser(ctx, "alice", "x", "user", "")
	admin, _ := a.db.UserByName(ctx, "admin")
	alice, _ := a.db.UserByName(ctx, "alice")
	for _, method := range []string{"GET", "POST"} {
		r := requestAs(a, alice, method, "/admin/proxy", nil)
		w := httptest.NewRecorder()
		if method == "GET" {
			a.require(a.proxyAdminPage)(w, r)
		} else {
			a.require(a.proxyAdminChange)(w, r)
		}
		if w.Code != 403 {
			t.Fatal("user admin access", method, w.Code)
		}
	}
	form := url.Values{"operation": {"save"}, "target": {"all:0"}, "name": {"Example"}, "domain": {"example.com"}, "ports": {"443"}, "methods": {"CONNECT"}, "effect": {"allow"}, "enabled": {"1"}}
	r := requestAs(a, admin, "POST", "/admin/proxy", strings.NewReader(form.Encode()))
	r.Header.Set("HX-Request", "true")
	r.Header.Set("HX-Target", "proxy-live")
	w := httptest.NewRecorder()
	a.require(a.proxyAdminChange)(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Example") || strings.Contains(w.Body.String(), "<!doctype") {
		t.Fatal(w.Code, w.Body.String())
	}
	form = url.Values{"operation": {"issue"}, "subject_id": {"2"}, "label": {"build"}, "days": {"30"}}
	r = requestAs(a, admin, "POST", "/admin/proxy", strings.NewReader(form.Encode()))
	w = httptest.NewRecorder()
	a.require(a.proxyAdminChange)(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "発行済みトークン") {
		t.Fatal(w.Code, w.Body.String())
	}
	r = requestAs(a, admin, "GET", "/admin/proxy", nil)
	w = httptest.NewRecorder()
	a.require(a.proxyAdminPage)(w, r)
	if strings.Contains(w.Body.String(), "発行済みトークン") {
		t.Fatal("secret persisted")
	}
}
