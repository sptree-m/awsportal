package main

import (
	"encoding/json"
	"github.com/sptree-m/awsportal/internal/store"
	"net/http"
	"strings"
	"time"
)

func (a *app) dcvAgentCredential(w http.ResponseWriter, r *http.Request) (string, bool) {
	w.Header().Set("Cache-Control", "no-store")
	// TLS is terminated by the deployment's trusted reverse proxy. The bearer
	// credential is unique to one EC2; user API tokens and portal cookies cannot substitute.
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		http.Error(w, "unauthorized", 401)
		return "", false
	}
	id, e := a.db.DCVAgentInstance(r.Context(), strings.TrimPrefix(auth, "Bearer "))
	if e != nil {
		http.Error(w, "unauthorized", 401)
		return "", false
	}
	return id, true
}
func (a *app) dcvAgentState(w http.ResponseWriter, r *http.Request) {
	id, ok := a.dcvAgentCredential(w, r)
	if !ok {
		return
	}
	accounts, e := a.db.DCVAccounts(r.Context(), id, time.Now())
	if e != nil {
		http.Error(w, "DB error", 500)
		return
	}
	policy, e := a.db.DCVPolicy(r.Context(), id)
	if e != nil {
		http.Error(w, "DB error", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"instance_id": id, "accounts": accounts, "policy": policy, "browser_block_required": true})
}
func (a *app) dcvAgentHeartbeat(w http.ResponseWriter, r *http.Request) {
	id, ok := a.dcvAgentCredential(w, r)
	if !ok {
		return
	}
	var report struct {
		BrowserBlocked  bool    `json:"browser_blocked"`
		AppliedRevision int64   `json:"applied_revision"`
		ReadyUsers      []int64 `json:"ready_users"`
		Error           string  `json:"error"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128*1024)
	if e := json.NewDecoder(r.Body).Decode(&report); e != nil {
		http.Error(w, "invalid heartbeat", 400)
		return
	}
	blocked := int64(0)
	if report.BrowserBlocked {
		blocked = 1
	}
	if e := a.db.DCVHeartbeat(r.Context(), id, report.ReadyUsers, report.Error, time.Now(), report.AppliedRevision, blocked); e != nil {
		http.Error(w, "invalid heartbeat", 400)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (a *app) dcvAgentAuth(w http.ResponseWriter, r *http.Request) {
	id, ok := a.dcvAgentCredential(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	if e := r.ParseForm(); e != nil {
		http.Error(w, "invalid", 400)
		return
	}
	username, valid := a.db.ConsumeInstanceDCVToken(r.Context(), store.DCVTokenHash(r.FormValue("authenticationToken")), r.FormValue("sessionId"), id, time.Now())
	if !valid {
		dcvAuthReply(w, http.StatusUnauthorized, "no", "", "unauthorized")
		return
	}
	a.db.Audit(r.Context(), username, "dcv.auth", id, "ok", "per-user session; one-time token")
	dcvAuthReply(w, http.StatusOK, "yes", username, "")
}
