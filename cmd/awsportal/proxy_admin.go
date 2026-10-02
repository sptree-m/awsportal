package main

import (
	"encoding/json"
	"github.com/sptree-m/awsportal/internal/store"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *app) proxyAdminPage(w http.ResponseWriter, r *http.Request) { a.proxyAdminRender(w, r, "") }
func (a *app) proxyAdminRender(w http.ResponseWriter, r *http.Request, token string) {
	u := r.Context().Value("user").(store.User)
	if u.Role != "portal_admin" {
		http.Error(w, "管理者のみ実行できます", 403)
		return
	}
	rules, e := a.db.ProxyRules(r.Context())
	if e != nil {
		http.Error(w, "DB error", 500)
		return
	}
	users, e := a.db.ListUsers(r.Context())
	if e != nil {
		http.Error(w, "DB error", 500)
		return
	}
	groups, e := a.db.Groups(r.Context())
	if e != nil {
		http.Error(w, "DB error", 500)
		return
	}
	creds, e := a.db.ProxyCredentials(r.Context())
	if e != nil {
		http.Error(w, "DB error", 500)
		return
	}
	a.renderView(w, r, "proxy.html", "proxy-live", "proxy-live", map[string]any{"User": u, "Rules": rules, "Users": users, "Groups": groups, "Credentials": creds, "Enabled": a.db.ProxyEnabled(r.Context()), "Token": token, "Listener": env("AWSPORTAL_PROXY_ADDR", "")})
}
func (a *app) proxyAdminChange(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value("user").(store.User)
	if u.Role != "portal_admin" {
		http.Error(w, "管理者のみ実行できます", 403)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if r.ParseForm() != nil {
		http.Error(w, "入力が不正です", 400)
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	subject, _ := strconv.ParseInt(r.FormValue("subject_id"), 10, 64)
	op := r.FormValue("operation")
	var e error
	token := ""
	switch op {
	case "enabled":
		e = a.db.SetProxyEnabled(r.Context(), r.FormValue("enabled") == "1")
	case "save":
		scope, sid, valid := strings.Cut(r.FormValue("target"), ":")
		subject, parseErr := strconv.ParseInt(sid, 10, 64)
		if !valid || parseErr != nil {
			http.Error(w, "適用対象を選択してください", 400)
			return
		}
		e = a.db.SaveProxyRule(r.Context(), store.ProxyRule{ID: id, Name: r.FormValue("name"), Scope: scope, SubjectID: subject, Domain: r.FormValue("domain"), Kind: r.FormValue("kind"), Ports: r.FormValue("ports"), Methods: r.FormValue("methods"), Effect: r.FormValue("effect"), Enabled: r.FormValue("enabled") == "1"})
	case "delete":
		e = a.db.DeleteProxyRule(r.Context(), id)
	case "issue":
		days, _ := strconv.Atoi(r.FormValue("days"))
		token, e = a.db.IssueProxyCredential(r.Context(), subject, r.FormValue("label"), days)
	case "revoke":
		e = a.db.RevokeProxyCredential(r.Context(), id)
	default:
		http.Error(w, "操作が不正です", 400)
		return
	}
	if e != nil {
		http.Error(w, "保存できません。名前・接続先・対象・入力形式を確認してください", 400)
		return
	}
	detail, _ := json.Marshal(map[string]string{"target": r.FormValue("target"), "subject_id": r.FormValue("subject_id"), "name": r.FormValue("name"), "domain": r.FormValue("domain"), "ports": r.FormValue("ports"), "methods": r.FormValue("methods"), "effect": r.FormValue("effect"), "enabled": r.FormValue("enabled"), "label": r.FormValue("label"), "days": r.FormValue("days")})
	a.db.Audit(r.Context(), u.Username, "proxy.admin."+op, strconv.FormatInt(id, 10), "ok", string(detail))
	// Newly issued secrets are rendered once and never put into a URL or audit log.
	if token != "" || r.Header.Get("HX-Request") == "true" {
		a.proxyAdminRender(w, r, token)
		return
	}
	http.Redirect(w, r, "/admin/proxy", 303)
}

func proxyTemplateFuncs() template.FuncMap {
	return template.FuncMap{"dictRule": func(r any, root any) map[string]any {
		rule := store.ProxyRule{Scope: "all", Effect: "allow", Enabled: true}
		if x, ok := r.(store.ProxyRule); ok {
			rule = x
		}
		return map[string]any{"Rule": rule, "Root": root}
	}, "proxyTime": func(t int64) string { return time.Unix(t, 0).Format("2006-01-02 15:04 MST") }}
}
