package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/http"
	"strconv"
	"time"

	"github.com/sptree-m/awsportal/internal/store"
)

type environmentCard struct {
	store.Environment
	Request store.ConnectionRequest
	Key     string
}

func (a *app) environmentPage(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value("user").(store.User)
	xs, err := a.db.VisibleEnvironments(r.Context(), u)
	if err != nil {
		http.Error(w, "DB error", 500)
		return
	}
	cards := []environmentCard{}
	for _, x := range xs {
		var key [16]byte
		if _, err = rand.Read(key[:]); err != nil {
			http.Error(w, "key generation failed", 500)
			return
		}
		card := environmentCard{Environment: x, Key: hex.EncodeToString(key[:])}
		var requestID int64
		err = a.db.DB.QueryRowContext(r.Context(), `SELECT id FROM connection_requests WHERE environment_id=? AND user_id=? AND state NOT IN ('RELEASED','CANCELLED','FAILED','TIMED_OUT')`, x.ID, u.ID).Scan(&requestID)
		if err != nil && err != sql.ErrNoRows {
			http.Error(w, "DB error", 500)
			return
		}
		if err == nil {
			card.Request, err = a.db.EnvironmentRequest(r.Context(), u, requestID)
			if err != nil {
				http.Error(w, "DB error", 500)
				return
			}
		}
		cards = append(cards, card)
	}
	w.Header().Set("Cache-Control", "no-store")
	jobs, err := a.db.VisibleManagedJobs(r.Context(), u, time.Now())
	if err != nil {
		http.Error(w, "DB error", 500)
		return
	}
	a.renderView(w, r, "environments.html", "environments-live", "environments-live", map[string]any{"User": u, "Environments": cards, "Jobs": jobs})
}
func pathID(r *http.Request) (int64, error) { return strconv.ParseInt(r.PathValue("id"), 10, 64) }
func (a *app) environmentConnect(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value("user").(store.User)
	id, err := pathID(r)
	if err != nil || r.ParseForm() != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	_, err = a.db.RequestEnvironment(r.Context(), u, id, r.FormValue("idempotency_key"), time.Now())
	if err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	a.db.Audit(r.Context(), u.Username, "environment.connect", strconv.FormatInt(id, 10), "ok", "")
	if r.Header.Get("HX-Request") == "true" {
		a.environmentPage(w, r)
		return
	}
	http.Redirect(w, r, "/environments", 303)
}
func (a *app) environmentEnd(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value("user").(store.User)
	id, err := pathID(r)
	if err != nil || r.ParseForm() != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	err = a.db.EndEnvironmentRequest(r.Context(), u, id, r.FormValue("operation") == "cancel", time.Now())
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	a.db.Audit(r.Context(), u.Username, "environment.release", strconv.FormatInt(id, 10), "ok", "")
	if r.Header.Get("HX-Request") == "true" {
		a.environmentPage(w, r)
		return
	}
	http.Redirect(w, r, "/environments", 303)
}
func (a *app) environmentRequestState(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value("user").(store.User)
	id, err := pathID(r)
	if err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	_, err = a.db.EnvironmentRequest(r.Context(), u, id)
	if err != nil {
		http.Error(w, "access denied", 403)
		return
	}
	a.environmentPage(w, r)
}
func (a *app) environmentDCV(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value("user").(store.User)
	id, err := pathID(r)
	if err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	x, err := a.db.EnvironmentRequest(r.Context(), u, id)
	if err != nil || x.State != "READY" && x.State != "CONNECTED" {
		http.Error(w, "Shared session not ready", 409)
		return
	}
	// Token creation is always a POST initiated by the user, never a GET/poll.
	a.issueNativeDCV(w, r, u, x.InstanceID)
}
func (a *app) environmentAdminPage(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value("user").(store.User)
	if u.Role != "portal_admin" {
		http.Error(w, "administrator required", 403)
		return
	}
	xs, err := a.db.VisibleEnvironments(r.Context(), u)
	if err != nil {
		http.Error(w, "DB error", 500)
		return
	}
	users, err := a.db.ListUsers(r.Context())
	if err != nil {
		http.Error(w, "DB error", 500)
		return
	}
	groups, err := a.db.Groups(r.Context())
	if err != nil {
		http.Error(w, "DB error", 500)
		return
	}
	// Viewing does not record decisions. Only the background controller does that.
	rows, err := a.db.DB.QueryContext(r.Context(), `SELECT i.instance_id,ei.idle_reason,ei.idle_since,(SELECT COUNT(*) FROM environment_assignments ea WHERE ea.instance_id=i.id AND ea.state!='RELEASED') FROM environment_instances ei JOIN instances i ON i.id=ei.instance_id`)
	if err != nil {
		http.Error(w, "DB error", 500)
		return
	}
	var instances []store.IdleDecision
	for rows.Next() {
		var x store.IdleDecision
		if err = rows.Scan(&x.InstanceID, &x.Reason, &x.Since, &x.Occupied); err != nil {
			rows.Close()
			http.Error(w, "DB error", 500)
			return
		}
		instances = append(instances, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		http.Error(w, "DB error", 500)
		return
	}
	jobs, err := a.db.VisibleManagedJobs(r.Context(), u, time.Now())
	if err != nil {
		http.Error(w, "DB error", 500)
		return
	}
	a.renderView(w, r, "environment-admin.html", "environment-admin-live", "environment-admin-live", map[string]any{"User": u, "Environments": xs, "Users": users, "Groups": groups, "Instances": instances, "Jobs": jobs})
}
func (a *app) environmentAdminChange(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value("user").(store.User)
	if u.Role != "portal_admin" {
		http.Error(w, "administrator required", 403)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	parse := func(name string) int64 { v, _ := strconv.ParseInt(r.FormValue(name), 10, 64); return v }
	reason := r.FormValue("reason")
	var err error
	switch r.FormValue("operation") {
	case "create":
		_, err = a.db.CreateEnvironment(r.Context(), u, store.Environment{Name: r.FormValue("name"), Mode: "shared", ProfileID: "shared-cpu-v1", GroupID: parse("group_id")}, reason)
	case "register":
		err = a.db.RegisterEnvironmentInstance(r.Context(), u, parse("environment_id"), r.FormValue("instance_id"), reason)
	case "acl":
		err = a.db.SetEnvironmentACL(r.Context(), u, parse("environment_id"), r.FormValue("kind"), parse("subject_id"), "environment.connect", r.FormValue("remove") == "1", reason)
	case "storage":
		err = a.db.SetUserStorage(r.Context(), u, parse("user_id"), r.FormValue("efs_id"), r.FormValue("access_point_id"), reason)
	default:
		http.Error(w, "Stage 1: automatic scale-out, termination and import are unavailable", 400)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	a.db.Audit(r.Context(), u.Username, "environment.admin."+r.FormValue("operation"), r.FormValue("environment_id"), "ok", reason)
	if r.Header.Get("HX-Request") == "true" {
		a.environmentAdminPage(w, r)
		return
	}
	http.Redirect(w, r, "/admin/environments", 303)
}
