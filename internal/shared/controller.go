// Package shared contains the fixed-pilot worker. It intentionally has no AWS
// provisioning, stopping, or termination capability in stage one.
package shared

import (
	"context"
	"time"

	"github.com/sptree-m/awsportal/internal/store"
)

type UsageSink interface {
	Put(context.Context, string, []byte) error
}
type Controller struct {
	Store *store.Store
	Sink  UsageSink
}

func (c *Controller) Run(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	exportTicker := time.NewTicker(time.Minute)
	defer exportTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := c.Tick(ctx, now); err != nil {
				c.Store.Audit(ctx, "controller", "environment.reconcile", "", "error", "reconciliation failed; existing work retained")
			}
		case <-exportTicker.C:
			if c.Sink != nil {
				if err := c.Export(ctx); err != nil {
					c.Store.Audit(ctx, "collector", "usage.export", "", "error", "export deferred")
				}
			}
		}
	}
}
func (c *Controller) Tick(ctx context.Context, now time.Time) error {
	if err := c.Store.ReconcileEnvironments(ctx, now); err != nil {
		return err
	}
	_, err := c.Store.RecordIdleDecisions(ctx, now)
	return err
}
func (c *Controller) Export(ctx context.Context) error {
	if c.Sink == nil {
		return nil
	}
	batches, err := c.Store.PendingUsageExports(ctx)
	if err != nil {
		return err
	}
	for _, batch := range batches {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		timeout, cancel := context.WithTimeout(ctx, 15*time.Second)
		err = c.Sink.Put(timeout, batch.Key, []byte(batch.Payload))
		cancel()
		if markErr := c.Store.CompleteUsageExport(ctx, batch.ID, err == nil); markErr != nil {
			return markErr
		}
	}
	return nil
}
