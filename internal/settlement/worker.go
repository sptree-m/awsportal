package settlement

import (
	"context"
	"github.com/sptree-m/awsportal/internal/store"
	"io"
	"time"
)

type Source interface {
	Open(context.Context, string, string) (io.ReadCloser, string, error)
}
type Worker struct {
	Store  *store.Store
	Source Source
}

func (w *Worker) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Tick(ctx)
		}
	}
}
func (w *Worker) Tick(ctx context.Context) error {
	xs, err := w.Store.PendingBilling(ctx)
	if err != nil {
		return err
	}
	for _, x := range xs {
		var username string
		if err = w.Store.DB.QueryRowContext(ctx, `SELECT username FROM users WHERE id=?`, x.UserID).Scan(&username); err != nil {
			return err
		}
		u, err := w.Store.UserByName(ctx, username)
		if err != nil {
			return err
		}
		if !u.Enabled || u.Role != "portal_admin" {
			return w.Store.FinishBillingJob(ctx, x.ID, "", true)
		}
		r, version, err := w.Source.Open(ctx, x.Key, x.Version)
		if err != nil {
			w.Store.FinishBillingJob(ctx, x.ID, "", true)
			continue
		}
		id, err := w.Store.ImportBilling(ctx, u, version, x.Scope, x.Policy, r)
		r.Close()
		if err = w.Store.FinishBillingJob(ctx, x.ID, id, err != nil); err != nil {
			return err
		}
	}
	return nil
}
