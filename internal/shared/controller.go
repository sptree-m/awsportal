// Package shared reconciles leases, metrics exports and opt-in cloud workflows.
package shared

import (
	"context"
	"time"

	"github.com/sptree-m/awsportal/internal/store"
)

type UsageSink interface {
	Put(context.Context, string, []byte) error
}
type PowerReader interface {
	States(context.Context, []string) (map[string]string, error)
}
type Controller struct {
	Power               PowerReader
	Store               *store.Store
	Sink                UsageSink
	Workflow            Workflow
	ScaleOut, Terminate bool
	Parquet             bool
}

func (c *Controller) Run(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	exportTicker := time.NewTicker(time.Minute)
	defer exportTicker.Stop()
	c.run(ctx, ticker.C, exportTicker.C)
}
func (c *Controller) run(ctx context.Context, ticks, exports <-chan time.Time) {
	if c.Sink != nil {
		go c.exportLoop(ctx, exports)
	}
	if c.Workflow != nil {
		go c.cloudLoop(ctx)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticks:
			if err := c.Tick(ctx, now); err != nil {
				c.Store.Audit(ctx, "controller", "environment.reconcile", "", "error", "reconciliation failed; existing work retained")
			}
		}
	}
}

// Slow S3 requests must not delay seat reconciliation or continuous idle
// observations. One exporter consumes the durable queue independently.
func (c *Controller) exportLoop(ctx context.Context, ticks <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			if err := c.Export(ctx); err != nil && ctx.Err() == nil {
				c.Store.Audit(ctx, "collector", "usage.export", "", "error", "export deferred")
			}
		}
	}
}
func (c *Controller) Tick(ctx context.Context, now time.Time) error {
	if c.Power != nil {
		ids, err := c.Store.PersonalLeaseInstances(ctx)
		if err != nil {
			return err
		}
		timeout, cancel := context.WithTimeout(ctx, 5*time.Second)
		states, err := c.Power.States(timeout, ids)
		cancel()
		if err == nil {
			for id, state := range states {
				if state == "stopped" || state == "terminated" {
					if err = c.Store.ConfirmPersonalStopped(ctx, id, state); err != nil {
						return err
					}
				}
			}
		}
	}

	if err := c.Store.ReconcileEnvironments(ctx, now); err != nil {
		return err
	}
	_, err := c.Store.RecordIdleDecisions(ctx, now)
	if err != nil {
		return err
	}
	return nil
}
func (c *Controller) Export(ctx context.Context) error {
	if c.Sink == nil {
		return nil
	}
	if c.Parquet {
		xs, err := c.Store.PrepareParquet(ctx, time.Now())
		if err != nil {
			return err
		}
		for _, x := range xs {
			timeout, cancel := context.WithTimeout(ctx, 15*time.Second)
			err = c.Sink.Put(timeout, x.Key, x.Payload)
			cancel()
			if err != nil {
				continue
			}
			if err = c.Store.CompleteParquet(ctx, x.Key); err != nil {
				return err
			}
		}
		return c.Store.RetainUsage(ctx, time.Now())
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

// AWS workflow latency never stalls reservation or heartbeat decisions.
func (c *Controller) cloudLoop(ctx context.Context) {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			timeout, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := c.CloudTick(timeout, now)
			cancel()
			if err != nil && ctx.Err() == nil {
				c.Store.Audit(ctx, "cloud-worker", "cloud.reconcile", "", "error", "workflow deferred; capacity retained")
			}
		}
	}
}
