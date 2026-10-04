package main

import (
	"encoding/json"
	"github.com/sptree-m/awsportal/internal/billing"
	"github.com/sptree-m/awsportal/internal/store"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *app) stageTwoPage(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value("user").(store.User)
	if u.Role != "portal_admin" {
		http.Error(w, "administrator required", 403)
		return
	}
	runs, err := a.db.BillingRuns(r.Context())
	if err != nil {
		http.Error(w, "DB error", 500)
		return
	}
	jobs, err := a.db.ImportJobs(r.Context())
	if err != nil {
		http.Error(w, "DB error", 500)
		return
	}
	ops, err := a.db.CloudOperations(r.Context())
	if err != nil {
		http.Error(w, "DB error", 500)
		return
	}
	images, err := a.db.GoldenImages(r.Context())
	if err != nil {
		http.Error(w, "DB error", 500)
		return
	}
	metrics, err := a.db.OperationalMetrics(r.Context(), time.Now())
	if err != nil {
		http.Error(w, "DB error", 500)
		return
	}
	a.renderPage(w, r, "two-stage.html", map[string]any{"User": u, "Runs": runs, "Imports": jobs, "Operations": ops, "Images": images, "Metrics": metrics})
}
func (a *app) stageTwoChange(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value("user").(store.User)
	if u.Role != "portal_admin" {
		http.Error(w, "administrator required", 403)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	if r.ParseForm() != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	integer := func(name string) int64 { x, _ := strconv.ParseInt(r.FormValue(name), 10, 64); return x }
	var err error
	switch r.FormValue("operation") {
	case "cloud_reconcile":
		err = a.db.RetryCloudOperation(r.Context(), u, r.FormValue("id"), r.FormValue("reason"))
	case "pool":
		err = a.db.SetPoolControl(r.Context(), u, integer("environment_id"), r.FormValue("scale_out") == "true", r.FormValue("terminate") == "true", r.FormValue("acceptance_ref"), r.FormValue("reason"))
	case "import_enable":
		err = a.db.SetImportEnabled(r.Context(), u, r.FormValue("enabled") == "true")
	case "import":
		_, err = a.db.RequestImport(r.Context(), u, r.FormValue("idempotency_key"), r.FormValue("source_root"), r.FormValue("bucket"), r.FormValue("prefix"), time.Now())
	case "import_cleanup":
		err = a.db.RequestImportCleanup(r.Context(), u, r.FormValue("id"), r.FormValue("reason"))
	case "golden_register":
		err = a.db.RegisterGolden(r.Context(), u, store.GoldenImage{ProfileID: r.FormValue("profile"), Version: r.FormValue("version"), AMI: r.FormValue("ami"), TemplateID: r.FormValue("template"), TemplateVersion: r.FormValue("template_version"), Checksum: r.FormValue("checksum"), ValidationRef: r.FormValue("validation_ref"), Channel: "NEXT"})
	case "golden_promote":
		err = a.db.PromoteGolden(r.Context(), u, r.FormValue("profile"), r.FormValue("version"), r.FormValue("reason"))
	case "import_cancel":
		err = a.db.CancelImport(r.Context(), u, r.FormValue("id"))
	case "billing":
		var start, end time.Time
		start, err = time.Parse("2006-01-02", r.FormValue("start"))
		if err == nil {
			end, err = time.Parse("2006-01-02", r.FormValue("end"))
		}
		var p store.BillingPolicy
		if err == nil {
			err = json.Unmarshal([]byte(r.FormValue("policy")), &p)
		}
		if err == nil {
			_, err = a.db.QueueBilling(r.Context(), u, r.FormValue("source_key"), r.FormValue("source_version"), billing.Scope{Account: r.FormValue("account"), Currency: r.FormValue("currency"), Basis: r.FormValue("basis"), Start: start, End: end}, p)
		}
	case "billing_finalize":
		err = a.db.FinalizeBilling(r.Context(), u, r.FormValue("id"))
	default:
		http.Error(w, "unknown operation", 400)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	a.db.Audit(r.Context(), u.Username, "two-stage."+r.FormValue("operation"), r.FormValue("id"), "ok", r.FormValue("reason"))
	http.Redirect(w, r, "/admin/two-stage", 303)
}
func (a *app) importAgentState(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if len(token) != 64 {
		http.Error(w, "unauthorized", 401)
		return
	}
	j, err := a.db.ImportAgentJob(r.Context(), token)
	if err != nil {
		http.Error(w, "unauthorized", 401)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(j)
}
func (a *app) importAgentEvent(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if len(token) != 64 {
		http.Error(w, "unauthorized", 401)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var event struct {
		State    string               `json:"state"`
		Sequence int64                `json:"sequence"`
		Evidence store.ImportEvidence `json:"evidence"`
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(&event) != nil {
		http.Error(w, "invalid event", 400)
		return
	}
	if err := a.db.ImportAgentEvent(r.Context(), token, event.State, event.Sequence, event.Evidence, time.Now()); err != nil {
		http.Error(w, "event rejected", 409)
		return
	}
	w.WriteHeader(204)
}
