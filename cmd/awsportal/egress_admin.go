package main

import (
	"context"
	"database/sql"
	"github.com/sptree-m/awsportal/internal/store"
	"net/http"
	"strconv"
	"time"
)

type egressView struct {
	Instance store.ManagedInstance
	Policy   store.EgressPolicy
}

func (a *app) egressPage(w http.ResponseWriter, r *http.Request) { a.egressRender(w, r, "") }
func (a *app) egressRender(w http.ResponseWriter, r *http.Request, message string) {
	u := r.Context().Value("user").(store.User)
	if u.Role != "portal_admin" {
		http.Error(w, "管理者のみ実行できます", 403)
		return
	}
	instances, e := a.db.ManagedInstances(r.Context())
	if e != nil {
		http.Error(w, "DB error", 500)
		return
	}
	views := []egressView{}
	for _, i := range instances {
		p, e := a.db.Egress(r.Context(), i.InstanceID)
		if e != nil && e != sql.ErrNoRows {
			http.Error(w, "DB error", 500)
			return
		}
		views = append(views, egressView{i, p})
	}
	a.renderView(w, r, "egress.html", "egress-live", "egress-live", map[string]any{"User": u, "Views": views, "Message": message})
}
func (a *app) egressChange(w http.ResponseWriter, r *http.Request) {
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
	id := r.FormValue("instance_id")
	var n int
	if a.db.DB.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM instances WHERE instance_id=?", id).Scan(&n) != nil || n != 1 {
		http.Error(w, "登録済みEC2だけ管理できます", 400)
		return
	}
	// Serialize AWS changes and policy revisions to avoid applying stale drafts concurrently.
	a.egressMu.Lock()
	defer a.egressMu.Unlock()
	op := r.FormValue("operation")
	var e error
	message := ""
	switch op {
	case "save":
		rules, err := store.ParseEgressRules(r.FormValue("rules"))
		if err != nil {
			http.Error(w, "各行は IP/CIDR tcp/udp ポート[-ポート] の形式です", 400)
			return
		}
		port, err := strconv.ParseInt(r.FormValue("proxy_port"), 10, 32)
		if err != nil {
			http.Error(w, "プロキシポートが不正です", 400)
			return
		}
		e = a.db.SaveEgress(r.Context(), store.EgressPolicy{InstanceID: id, SecurityGroupID: r.FormValue("security_group_id"), ProxyGroupID: r.FormValue("proxy_group_id"), ProxyPort: int32(port), Rules: rules})
		message = "保存済み。AWSへ適用はまだ行っていません。"
	case "apply", "inspect":
		if a.egress == nil {
			http.Error(w, "AWS制御が未構成です", 503)
			return
		}
		p, err := a.db.Egress(r.Context(), id)
		if err != nil {
			http.Error(w, "先に設定を保存してください", 400)
			return
		}
		rev, err := strconv.ParseInt(r.FormValue("revision"), 10, 64)
		if err != nil || rev != p.Revision {
			http.Error(w, "設定が更新されています。画面を再読み込みしてください", 409)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		if op == "inspect" {
			message, e = a.egress.InspectEgress(ctx, p)
		} else {
			e = a.egress.ApplyEgress(ctx, p)
			recordErr := a.db.RecordEgressApply(r.Context(), id, p.Revision, e)
			if e == nil {
				e = recordErr
			}
			message = "AWSへ適用し、設定一致を確認しました。"
		}
	default:
		http.Error(w, "操作が不正です", 400)
		return
	}
	result := "ok"
	if e != nil {
		result = "deny"
		message = "処理失敗: " + e.Error()
	}
	a.db.Audit(r.Context(), u.Username, "egress.admin."+op, id, result, message)
	a.egressRender(w, r, id+": "+message)
}
