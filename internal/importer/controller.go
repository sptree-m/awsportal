package importer

import (
	"context"
	"encoding/json"
	"github.com/sptree-m/awsportal/internal/shared"
	"github.com/sptree-m/awsportal/internal/store"
	"time"
)

type Verifier interface {
	Verify(context.Context, store.ImportJob) (store.ImportEvidence, error)
}
type Controller struct {
	Store    *store.Store
	Workflow shared.Workflow
	Verifier Verifier
	Enabled  bool
}

func (c *Controller) Run(ctx context.Context) {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if err := c.Tick(ctx, now); err != nil {
				c.Store.Audit(ctx, "import-worker", "import.reconcile", "", "error", "import deferred; resources retained")
			}
		}
	}
}
func (c *Controller) Tick(ctx context.Context, now time.Time) error {
	if err := c.Store.TimeoutImports(ctx, now); err != nil {
		return err
	}
	jobs, err := c.Store.ImportJobs(ctx)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if c.Workflow == nil {
			continue
		}
		switch j.State {
		case "REQUESTED", "PROVISIONING":
			var permit bool
			if err = c.Store.DB.QueryRowContext(ctx, `SELECT enabled FROM import_controls WHERE id=1`).Scan(&permit); err != nil {
				return err
			}
			allowed := c.Enabled && permit
			if j.State == "REQUESTED" && !allowed {
				continue
			}
			if j.ExecutionARN == "" && !allowed {
				lookup, ok := c.Workflow.(interface {
					Lookup(context.Context, store.CloudOperation) (string, bool, error)
				})
				if !ok {
					continue
				}
				arn, exists, err := lookup.Lookup(ctx, store.CloudOperation{ID: j.ID, Kind: "PROVISION", Input: j.ProvisionInput})
				if err != nil || !exists {
					continue
				}
				if err = c.Store.MarkImportProvision(ctx, j.ID, arn); err != nil {
					return err
				}
				j.ExecutionARN = arn
			}
			if j.ExecutionARN == "" {
				if err = c.Store.MarkImportProvision(ctx, j.ID, ""); err != nil {
					return err
				}
				raw := json.RawMessage(j.ProvisionInput)
				arn, err := c.Workflow.Start(ctx, store.CloudOperation{ID: j.ID, Kind: "PROVISION", Input: string(raw)})
				if err != nil {
					continue
				}
				if err = c.Store.MarkImportProvision(ctx, j.ID, arn); err != nil {
					return err
				}
				j.ExecutionARN = arn
			}
			state, out, err := c.Workflow.Status(ctx, j.ExecutionARN)
			if err != nil {
				continue
			}
			if state == "SUCCEEDED" || state == "FAILED" || state == "TIMED_OUT" || state == "ABORTED" {
				if err = c.Store.FinishImportProvision(ctx, j.ID, out, state != "SUCCEEDED"); err != nil {
					return err
				}
			}
		case "VALIDATING":
			if c.Verifier == nil {
				continue
			}
			e, err := c.Verifier.Verify(ctx, j)
			if err != nil {
				if permanent, ok := err.(interface{ Permanent() bool }); ok && permanent.Permanent() {
					if err = c.Store.FailImportVerification(ctx, j.ID); err != nil {
						return err
					}
				}
				continue
			}
			if err = c.Store.VerifyImport(ctx, j.ID, e); err != nil {
				return err
			}
		case "VERIFIED", "CLEANUP_REQUESTED":
			operationID := j.ID
			if j.CleanupOperationID != "" {
				operationID = j.CleanupOperationID
			}
			raw, _ := json.Marshal(map[string]any{"operation_id": operationID, "environment_id": "windows", "generation": 1, "kind": "TERMINATE", "instance_id": j.InstanceID})
			arn, err := c.Workflow.Start(ctx, store.CloudOperation{ID: operationID, Kind: "TERMINATE", Input: string(raw)})
			if err != nil {
				continue
			}
			if err = c.Store.StartImportCleanup(ctx, j.ID, arn); err != nil {
				return err
			}
		case "CLEANING_UP":
			state, out, err := c.Workflow.Status(ctx, j.CleanupARN)
			if err != nil {
				continue
			}
			if state == "SUCCEEDED" || state == "FAILED" || state == "TIMED_OUT" || state == "ABORTED" {
				if err = c.Store.FinishImportCleanup(ctx, j.ID, out, state != "SUCCEEDED"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
