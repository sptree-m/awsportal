package main

import (
	"encoding/json"
	"errors"
	"github.com/sptree-m/awsportal/internal/store"
	"net/http"
	"strconv"
	"strings"
)

func (a *app) mirrorPage(w http.ResponseWriter, r *http.Request) { a.mirrorRender(w, r, "") }
func (a *app) mirrorRender(w http.ResponseWriter, r *http.Request, token string) {
	u := r.Context().Value("user").(store.User)
	repos, e := a.db.Mirrors(r.Context())
	if e != nil {
		http.Error(w, "DB error", 500)
		return
	}
	visible := []store.MirrorRepo{}
	for _, repo := range repos {
		if u.Role == "portal_admin" || a.db.MirrorAccess(r.Context(), u, repo.ID, false) {
			visible = append(visible, repo)
		}
	}
	syncRepos := map[int64]bool{}
	for _, repo := range visible {
		syncRepos[repo.ID] = a.db.MirrorAccess(r.Context(), u, repo.ID, true)
	}
	data := map[string]any{"User": u, "Repos": visible, "Token": token, "Available": a.mirrors != nil, "SyncRepos": syncRepos}
	if u.Role == "portal_admin" {
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
		grants, e := a.db.MirrorGrants(r.Context())
		if e != nil {
			http.Error(w, "DB error", 500)
			return
		}
		tokens, e := a.db.MirrorTokens(r.Context())
		if e != nil {
			http.Error(w, "DB error", 500)
			return
		}
		jobs, e := a.db.MirrorJobs(r.Context())
		if e != nil {
			http.Error(w, "DB error", 500)
			return
		}
		data["Users"] = users
		data["Groups"] = groups
		data["Grants"] = grants
		data["Tokens"] = tokens
		data["Jobs"] = jobs
	}
	a.renderView(w, r, "mirrors.html", "mirrors-live", "mirrors-live", data)
}
func (a *app) mirrorChange(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value("user").(store.User)
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if r.ParseForm() != nil {
		http.Error(w, "入力が不正です", 400)
		return
	}
	op := r.FormValue("operation")
	id, _ := strconv.ParseInt(r.FormValue("repo_id"), 10, 64)
	var e error
	token := ""
	if op == "sync" {
		if a.mirrors == nil {
			http.Error(w, "ミラー未構成", 503)
			return
		}
		if !a.db.MirrorAccess(r.Context(), u, id, true) {
			http.Error(w, "同期権限がありません", 403)
			return
		}
		_, e = a.db.EnqueueMirror(r.Context(), id, u.ID)
	} else {
		if u.Role != "portal_admin" {
			http.Error(w, "管理者のみ実行できます", 403)
			return
		}
		switch op {
		case "save":
			interval, parseErr := strconv.ParseInt(r.FormValue("interval"), 10, 64)
			if parseErr != nil {
				http.Error(w, "同期間隔が不正です", 400)
				return
			}
			e = a.db.SaveMirror(r.Context(), store.MirrorRepo{ID: id, Name: r.FormValue("name"), Upstream: r.FormValue("upstream"), CredentialRef: r.FormValue("credential_ref"), Branch: r.FormValue("branch"), IntervalMinutes: interval, Enabled: r.FormValue("enabled") == "1"})
		case "grant", "ungrant":
			scope, subject, ok := strings.Cut(r.FormValue("target"), ":")
			sid, err := strconv.ParseInt(subject, 10, 64)
			if !ok || err != nil {
				http.Error(w, "割り当て先を選択してください", 400)
				return
			}
			e = a.db.SetMirrorGrant(r.Context(), id, scope, sid, r.FormValue("can_sync") == "1", op == "ungrant")
		case "issue":
			uid, _ := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
			days, _ := strconv.Atoi(r.FormValue("days"))
			token, e = a.db.IssueMirrorToken(r.Context(), uid, r.FormValue("label"), days, r.FormValue("can_sync") == "1")
		case "revoke":
			tid, _ := strconv.ParseInt(r.FormValue("token_id"), 10, 64)
			e = a.db.RevokeMirrorToken(r.Context(), tid)
		default:
			http.Error(w, "操作が不正です", 400)
			return
		}
	}
	if e != nil {
		if errors.Is(e, store.ErrMirrorCooldown) {
			w.Header().Set("Retry-After", "30")
			http.Error(w, "同期要求は30秒以上空けてください", 429)
		} else {
			http.Error(w, "保存・同期要求に失敗。対象と入力を確認してください", 400)
		}
		return
	}
	a.db.Audit(r.Context(), u.Username, "mirror.admin."+op, strconv.FormatInt(id, 10), "ok", "")
	if token != "" || r.Header.Get("HX-Request") == "true" {
		a.mirrorRender(w, r, token)
		return
	}
	http.Redirect(w, r, "/mirrors", 303)
}
func (a *app) mirrorAPIUser(w http.ResponseWriter, r *http.Request) (store.User, bool, bool) {
	raw := r.Header.Get("Authorization")
	token := strings.TrimPrefix(raw, "Bearer ")
	if token == raw {
		if name, password, ok := r.BasicAuth(); ok {
			u, sync, e := a.db.MirrorTokenUser(r.Context(), password)
			if e == nil && name == u.Username {
				return u, sync, true
			}
		}
	} else {
		u, sync, e := a.db.MirrorTokenUser(r.Context(), token)
		if e == nil {
			return u, sync, true
		}
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="awsportal-mirror"`)
	http.Error(w, "mirror credential required", 401)
	return store.User{}, false, false
}
func mirrorJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func (a *app) mirrorSyncAPI(w http.ResponseWriter, r *http.Request) {
	u, tokenSync, ok := a.mirrorAPIUser(w, r)
	if !ok {
		return
	}
	id, e := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if e != nil || !tokenSync || !a.db.MirrorAccess(r.Context(), u, id, true) {
		http.Error(w, "sync forbidden", 403)
		return
	}
	if a.mirrors == nil {
		http.Error(w, "mirror not configured", 503)
		return
	}
	if r.ContentLength != 0 || r.URL.RawQuery != "" {
		http.Error(w, "sync accepts no URL, body or options", 400)
		return
	}
	job, e := a.db.EnqueueMirror(r.Context(), id, u.ID)
	if errors.Is(e, store.ErrMirrorCooldown) {
		w.Header().Set("Retry-After", "30")
		http.Error(w, "sync cooldown", 429)
		return
	}
	if e != nil {
		http.Error(w, "job unavailable", 500)
		return
	}
	a.db.RecordMirrorAccess(r.Context(), u)
	a.db.Audit(r.Context(), u.Username, "mirror.sync.request", strconv.FormatInt(id, 10), "ok", strconv.FormatInt(job.ID, 10))
	mirrorJSON(w, 202, map[string]any{"job_id": job.ID, "state": job.State, "status_url": "/api/mirror-jobs/" + strconv.FormatInt(job.ID, 10)})
}
func (a *app) mirrorJobAPI(w http.ResponseWriter, r *http.Request) {
	u, _, ok := a.mirrorAPIUser(w, r)
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	j, e := a.db.MirrorJob(r.Context(), id)
	if e != nil || !a.db.MirrorAccess(r.Context(), u, j.RepoID, false) {
		http.Error(w, "job not found", 404)
		return
	}
	a.db.RecordMirrorAccess(r.Context(), u)
	mirrorJSON(w, 200, map[string]any{"job_id": j.ID, "repo_id": j.RepoID, "state": j.State, "message": j.Message, "finished": j.Finished})
}
func (a *app) mirrorGit(w http.ResponseWriter, r *http.Request) {
	u, _, ok := a.mirrorAPIUser(w, r)
	if !ok {
		return
	}
	id, e := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if e != nil || !a.db.MirrorAccess(r.Context(), u, id, false) {
		http.Error(w, "repository forbidden", 403)
		return
	}
	if a.mirrors == nil {
		http.Error(w, "mirror not configured", 503)
		return
	}
	repo, e := a.db.Mirror(r.Context(), id)
	if e != nil {
		http.Error(w, "not found", 404)
		return
	}
	a.db.RecordMirrorAccess(r.Context(), u)
	a.mirrors.Serve(w, r, repo, r.PathValue("suffix"))
}
