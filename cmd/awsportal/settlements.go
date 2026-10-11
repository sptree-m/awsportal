package main

import (
	"encoding/csv"
	"github.com/sptree-m/awsportal/internal/store"
	"net/http"
	"strconv"
)

func (a *app) settlements(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value("user").(store.User)
	rows, err := a.db.UserSettlements(r.Context(), u)
	if err != nil {
		http.Error(w, "DB error", 500)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/settlements.csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=awsportal-settlements.csv")
		c := csv.NewWriter(w)
		c.Write([]string{"run_id", "state", "scope", "user_id", "username", "amount", "source_version", "formula"})
		for _, x := range rows {
			c.Write([]string{x.RunID, x.State, x.Scope, strconv.FormatInt(x.UserID, 10), x.Username, x.Amount, x.Source, x.Formula})
		}
		c.Flush()
		return
	}
	a.renderPage(w, r, "settlements.html", map[string]any{"User": u, "Settlements": rows})
}
