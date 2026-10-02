package main

import (
	"github.com/sptree-m/awsportal/internal/store"
	"log"
	"net/http"
)

// Text remains ordinary strings: html/template escapes it in every page.
func (a *app) siteData(r *http.Request, data any) map[string]any {
	values := map[string]any{}
	if m, ok := data.(map[string]any); ok {
		for k, v := range m {
			values[k] = v
		}
	}
	settings, err := a.db.SiteSettings(r.Context())
	if err != nil {
		log.Printf("site settings read failed: %v", err)
		settings = store.DefaultSiteSettings()
	}
	values["Site"] = settings
	return values
}
func (a *app) renderPage(w http.ResponseWriter, r *http.Request, page string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.tpl.ExecuteTemplate(w, page, a.siteData(r, data)); err != nil {
		log.Printf("render %s: %v", page, err)
	}
}
func (a *app) manualPage(w http.ResponseWriter, r *http.Request) {
	settings, err := a.db.SiteSettings(r.Context())
	if err != nil {
		http.Error(w, "設定を取得できません", 500)
		return
	}
	portal, proxy := settings.PortalURL, settings.ProxyURL
	if portal == "" {
		portal = "https://portal.example"
	}
	if proxy == "" {
		proxy = "https://proxy.example:3128"
	}
	a.renderPage(w, r, "manual.html", map[string]any{"User": r.Context().Value("user").(store.User), "PortalExampleURL": portal, "ProxyExampleURL": proxy, "PortalConfigured": settings.PortalURL != "", "ProxyConfigured": settings.ProxyURL != ""})
}
func (a *app) siteAdminPage(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value("user").(store.User)
	if u.Role != "portal_admin" {
		http.Error(w, "管理者のみ実行できます", 403)
		return
	}
	a.renderPage(w, r, "site-settings.html", map[string]any{"User": u, "Saved": r.URL.Query().Get("saved") == "1"})
}
func (a *app) siteAdminChange(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value("user").(store.User)
	if u.Role != "portal_admin" {
		http.Error(w, "管理者のみ実行できます", 403)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	if r.ParseForm() != nil {
		http.Error(w, "入力が大きすぎるか不正です", 400)
		return
	}
	settings := store.SiteSettings{BrandTitle: r.PostFormValue("brand_title"), BrandSubtitle: r.PostFormValue("brand_subtitle"), HomeTitle: r.PostFormValue("home_title"), HomeMessage: r.PostFormValue("home_message"), LoginMessage: r.PostFormValue("login_message"), HelpMessage: r.PostFormValue("help_message"), PortalURL: r.PostFormValue("portal_url"), ProxyURL: r.PostFormValue("proxy_url")}
	if err := a.db.SaveSiteSettings(r.Context(), u, settings); err != nil {
		http.Error(w, "保存できません。タイトル、文字数、HTTPS接続先（認証情報・パスなし）を確認してください", 400)
		return
	}
	a.db.Audit(r.Context(), u.Username, "site.settings.save", "site", "ok", "")
	// Reload the whole page after save so brand/title and form always agree.
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/admin/site?saved=1")
		w.WriteHeader(204)
		return
	}
	http.Redirect(w, r, "/admin/site?saved=1", http.StatusSeeOther)
}
