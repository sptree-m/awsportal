package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSiteAdminRightsBrandEscapingAndUserManual(t *testing.T) {
	a, _ := newHandlerTestApp(t)
	ctx := context.Background()
	for _, role := range []string{"user", "portal_admin"} {
		if err := a.db.CreateUser(ctx, role, "x", role, ""); err != nil {
			t.Fatal(err)
		}
	}
	user, _ := a.db.UserByName(ctx, "user")
	admin, _ := a.db.UserByName(ctx, "portal_admin")
	form := url.Values{"brand_title": {"研究開発 <img src=x onerror=alert(1)>"}, "brand_subtitle": {"利用者向け\n社内環境"}, "home_title": {"研究環境"}, "home_message": {"一行目\n<script>alert(2)</script>"}, "login_message": {"ログイン案内"}, "help_message": {"管理者へ確認"}, "portal_url": {"https://portal.company.example"}, "proxy_url": {"https://proxy.company.example:3128"}}
	for _, tc := range []struct {
		method  string
		handler http.HandlerFunc
	}{{"GET", a.siteAdminPage}, {"POST", a.siteAdminChange}} {
		w := httptest.NewRecorder()
		r := requestAs(a, user, tc.method, "/admin/site", strings.NewReader(form.Encode()))
		a.require(tc.handler)(w, r)
		if w.Code != 403 {
			t.Fatal("ordinary user settings", w.Code)
		}
		w = httptest.NewRecorder()
		a.require(tc.handler)(w, httptest.NewRequest(tc.method, "/admin/site", strings.NewReader(form.Encode())))
		if w.Code != 303 {
			t.Fatal("anonymous settings", w.Code)
		}
	}
	r := requestAs(a, admin, "POST", "/admin/site", strings.NewReader(form.Encode()))
	r.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	a.require(a.siteAdminChange)(w, r)
	if w.Code != 204 || w.Header().Get("HX-Redirect") != "/admin/site?saved=1" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	for _, tc := range []struct {
		path    string
		handler http.HandlerFunc
		must    string
	}{{"/", a.dashboard, "研究環境"}, {"/manual", a.manualPage, "https://portal.company.example"}, {"/instances", a.instancesPage, "利用者向け"}, {"/mfa", a.mfaPage, "利用者向け"}} {
		w = httptest.NewRecorder()
		a.require(tc.handler)(w, requestAs(a, user, "GET", tc.path, nil))
		body := w.Body.String()
		if w.Code != 200 || !strings.Contains(body, tc.must) || !strings.Contains(body, "研究開発 &lt;img") {
			t.Fatal(tc.path, w.Code, body)
		}
		if strings.Contains(body, "<script>alert(2)</script>") || strings.Contains(body, "<img src=x") || strings.Contains(body, `href="/admin/site"`) {
			t.Fatal("unescaped content/admin nav", tc.path, body)
		}
	}
	w = httptest.NewRecorder()
	a.loginPage(w, httptest.NewRequest("GET", "/login", nil))
	if !strings.Contains(w.Body.String(), "ログイン案内") || !strings.Contains(w.Body.String(), "研究開発 &lt;img") {
		t.Fatal(w.Body.String())
	}
	w = httptest.NewRecorder()
	a.require(a.manualPage)(w, httptest.NewRequest("GET", "/manual", nil))
	if w.Code != 303 {
		t.Fatal("manual anonymous", w.Code)
	}
	r = requestAs(a, user, "GET", "/", nil)
	r.Header.Set("HX-Request", "true")
	r.Header.Set("HX-Target", "dashboard-live")
	w = httptest.NewRecorder()
	a.require(a.dashboard)(w, r)
	if strings.Contains(w.Body.String(), "<html") || !strings.Contains(w.Body.String(), "一行目") {
		t.Fatal("fragment lost customization", w.Body.String())
	}
	form.Set("proxy_url", "https://secret@proxy.example")
	w = httptest.NewRecorder()
	a.require(a.siteAdminChange)(w, requestAs(a, admin, "POST", "/admin/site", strings.NewReader(form.Encode())))
	if w.Code != 400 {
		t.Fatal("credential URL accepted", w.Code)
	}
	stored, _ := a.db.SiteSettings(ctx)
	if stored.ProxyURL != "https://proxy.company.example:3128" {
		t.Fatal("invalid form changed setting")
	}
	var count int
	if err := a.db.DB.QueryRow("SELECT COUNT(*) FROM audit_log WHERE action='site.settings.save'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("save audit", count)
	}
}

// Japanese text expands to nine bytes per character in a URL-encoded form.
func TestSiteFormAcceptsDocumentedJapaneseLimits(t *testing.T) {
	a, _ := newHandlerTestApp(t)
	ctx := context.Background()
	if err := a.db.CreateUser(ctx, "admin", "x", "portal_admin", ""); err != nil {
		t.Fatal(err)
	}
	admin, _ := a.db.UserByName(ctx, "admin")
	form := url.Values{"brand_title": {strings.Repeat("名", 80)}, "brand_subtitle": {strings.Repeat("説", 160)}, "home_title": {strings.Repeat("題", 80)}, "home_message": {strings.Repeat("文", 4000)}, "login_message": {strings.Repeat("案", 1000)}, "help_message": {strings.Repeat("補", 2000)}}
	w := httptest.NewRecorder()
	a.require(a.siteAdminChange)(w, requestAs(a, admin, "POST", "/admin/site", strings.NewReader(form.Encode())))
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	got, err := a.db.SiteSettings(ctx)
	if err != nil || got.HomeMessage != form.Get("home_message") || got.HelpMessage != form.Get("help_message") {
		t.Fatal("maximum Japanese text not preserved", err)
	}
}
