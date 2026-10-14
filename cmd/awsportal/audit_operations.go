package main

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/sptree-m/awsportal/internal/store"
)

type auditOperationKey struct{}
type auditedResponse struct {
	http.ResponseWriter
	status int
}

func (w *auditedResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *auditedResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(b)
}
func (w *auditedResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (a *app) auditedMutation(w http.ResponseWriter, r *http.Request, u store.User, next http.HandlerFunc) {
	if r.Method == "GET" || r.Method == "HEAD" || r.Method == "OPTIONS" || r.Context().Value(auditOperationKey{}) != nil {
		next(w, r)
		return
	}
	id, err := a.db.BeginAuditOperation(r.Context(), u.Username, "browser.mutation", r.URL.Path)
	if err != nil {
		store.AuditFailure("browser.mutation")
		http.Error(w, "監査を保存できないため操作を実行しません", 503)
		return
	}
	out := &auditedResponse{ResponseWriter: w}
	finished := false
	defer func() {
		if !finished {
			store.AuditFailure("browser.mutation.interrupted")
			return
		}
		status := out.status
		if status == 0 {
			status = 200
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer cancel()
		if err := a.db.CompleteAuditOperation(ctx, id, "http_status="+strconv.Itoa(status)); err != nil {
			store.AuditFailure("browser.mutation.complete")
		}
	}()
	next(out, r.WithContext(context.WithValue(r.Context(), auditOperationKey{}, id)))
	finished = true
}
