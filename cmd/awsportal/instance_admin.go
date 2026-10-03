package main

import (
	"crypto/rand"
	"encoding/hex"
	"github.com/sptree-m/awsportal/internal/store"
	"net/http"
	"strconv"
)

func (a *app) instanceAdminPage(w http.ResponseWriter, r *http.Request) {
	a.instanceAdminPageSecret(w, r, "")
}
func (a *app) instanceAdminPageSecret(w http.ResponseWriter, r *http.Request, secret string) {
	u := r.Context().Value("user").(store.User)
	if u.Role != "portal_admin" {
		http.Error(w, "管理者のみ実行できます", 403)
		return
	}
	xs, e := a.db.ManagedInstances(r.Context())
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
	members, e := a.db.GroupMembers(r.Context())
	if e != nil {
		http.Error(w, "DB error", 500)
		return
	}
	a.renderView(w, r, "instance-admin.html", "instance-admin-live", "instance-admin-live", map[string]any{"User": u, "Instances": xs, "Users": users, "Groups": groups, "Members": members, "DCVSecret": secret})
}
func (a *app) instanceAdminChange(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value("user").(store.User)
	if u.Role != "portal_admin" {
		http.Error(w, "管理者のみ実行できます", 403)
		return
	}
	if e := r.ParseForm(); e != nil {
		http.Error(w, "入力を確認してください", 400)
		return
	}
	op := r.FormValue("operation")
	id := r.FormValue("instance_id")
	var e error
	secret := ""
	switch op {
	case "dcv-config":
		raw := make([]byte, 32)
		if _, e = rand.Read(raw); e != nil {
			http.Error(w, "生成失敗", 500)
			return
		}
		secret = hex.EncodeToString(raw)
		e = a.db.ConfigureDCV(r.Context(), u, id, r.FormValue("dcv_host"), r.FormValue("dcv_mode"), secret)
	case "disable", "enable":
		e = a.db.SetInstanceEnabled(r.Context(), u, id, op == "enable")
	case "assign", "unassign":
		subject, err := strconv.ParseInt(r.FormValue("subject_id"), 10, 64)
		if err != nil || subject < 1 {
			http.Error(w, "割当先を選択してください", 400)
			return
		}
		permission := r.FormValue("permission")
		if op == "assign" && permission != "view" && permission != "control" {
			http.Error(w, "権限を選択してください", 400)
			return
		}
		e = a.db.SetAssignment(r.Context(), u, id, r.FormValue("kind"), subject, permission == "control", op == "unassign")
	case "create-group":
		e = a.db.CreateGroup(r.Context(), u, r.FormValue("name"))
	case "add-member", "remove-member":
		gid, er := strconv.ParseInt(r.FormValue("group_id"), 10, 64)
		uid, ur := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
		if er != nil || ur != nil || gid < 1 || uid < 1 {
			http.Error(w, "グループとユーザーを選択してください", 400)
			return
		}
		e = a.db.SetGroupMember(r.Context(), u, gid, uid, op == "remove-member")
		id = r.FormValue("group_id")
	default:
		http.Error(w, "操作が不正です", 400)
		return
	}
	detail := "kind=" + r.FormValue("kind") + ";subject=" + r.FormValue("subject_id") + ";permission=" + r.FormValue("permission") + ";group=" + r.FormValue("group_id") + ";user=" + r.FormValue("user_id") + ";name=" + r.FormValue("name")
	if e != nil {
		a.db.Audit(r.Context(), u.Username, "instance.admin."+op, id, "deny", detail)
		http.Error(w, "設定できません。対象と入力を確認してください", 400)
		return
	}
	a.db.Audit(r.Context(), u.Username, "instance.admin."+op, id, "ok", detail)
	if secret != "" {
		w.Header().Set("Cache-Control", "no-store")
		a.instanceAdminPageSecret(w, r, secret)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		a.instanceAdminPage(w, r)
		return
	}
	http.Redirect(w, r, "/admin/instances", 303)
}
