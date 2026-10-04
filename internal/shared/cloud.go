package shared

import (
	"context"
	"github.com/sptree-m/awsportal/internal/store"
	"time"
)

type Workflow interface {
	Start(context.Context, store.CloudOperation) (string, error)
	Status(context.Context, string) (string, store.CloudResult, error)
}

// CloudTick reconciles persisted operations even after feature switches are off.
// Switches authorize only new reservations; in-flight AWS operations remain known.
func (c *Controller) CloudTick(ctx context.Context, now time.Time) error {
	if c.Workflow == nil {
		return nil
	}
	if err := c.Store.ReservePoolOperations(ctx, now, c.ScaleOut, c.Terminate); err != nil {
		return err
	}
	ops, err := c.Store.PendingCloudOperations(ctx)
	if err != nil {
		return err
	}
	for _, o := range ops {
		global := c.ScaleOut
		if o.Kind == "TERMINATE" {
			global = c.Terminate
		}
		allowed, err := c.Store.CloudSubmissionAllowed(ctx, o, global)
		if err != nil {
			return err
		}
		if !allowed && (o.State == "RESERVED" || o.State == "DRAINING") {
			if err = c.Store.CancelUnsubmitted(ctx, o); err != nil {
				return err
			}
			continue
		}
		if o.State == "SUBMITTING" && !allowed {
			lookup, ok := c.Workflow.(interface {
				Lookup(context.Context, store.CloudOperation) (string, bool, error)
			})
			if !ok {
				continue
			}
			arn, exists, err := lookup.Lookup(ctx, o)
			if err != nil || !exists {
				continue
			}
			if err = c.Store.MarkCloudStarted(ctx, o.ID, arn); err != nil {
				return err
			}
			o.ExecutionARN = arn
			o.State = "RUNNING"
		}
		if o.State == "DRAINING" {
			ready, err := c.Store.ReconcileDrain(ctx, o, now)
			if err != nil {
				return err
			}
			if !ready {
				continue
			}
			o.State = "RESERVED"
		}
		if o.State == "RESERVED" || o.State == "SUBMITTING" {
			submitted, err := c.Store.MarkCloudSubmitting(ctx, o.ID)
			if err != nil {
				return err
			}
			if !submitted {
				continue
			}
			arn, err := c.Workflow.Start(ctx, o)
			if err != nil {
				continue
			}
			if err = c.Store.MarkCloudStarted(ctx, o.ID, arn); err != nil {
				return err
			}
			o.ExecutionARN = arn
		}
		state, result, err := c.Workflow.Status(ctx, o.ExecutionARN)
		if err != nil {
			continue
		}
		if state == "SUCCEEDED" || state == "FAILED" || state == "TIMED_OUT" || state == "ABORTED" {
			if err = c.Store.FinishCloudOperation(ctx, o.ID, result, state != "SUCCEEDED", now); err != nil {
				return err
			}
		}
	}
	return nil
}
