package shared

import "context"

func (c *Controller) VolumeTick(ctx context.Context) error {
	if c.PerformanceWorkflow == nil {
		return nil
	}
	ops, err := c.Store.VolumeOperations(ctx)
	if err != nil {
		return err
	}
	for _, o := range ops {
		if o.State == "FAILED" || o.State == "SUCCEEDED" {
			continue
		}
		if o.ExecutionARN == "" {
			if err = c.Store.MarkVolumeOperation(ctx, o.ID, "SUBMITTING", ""); err != nil {
				return err
			}
			arn, e := c.PerformanceWorkflow.Start(ctx, o)
			if e != nil {
				continue
			}
			if err = c.Store.MarkVolumeOperation(ctx, o.ID, "RUNNING", arn); err != nil {
				return err
			}
			o.ExecutionARN = arn
		}
		state, _, e := c.PerformanceWorkflow.Status(ctx, o.ExecutionARN)
		if e != nil {
			continue
		}
		if state == "SUCCEEDED" {
			err = c.Store.MarkVolumeOperation(ctx, o.ID, "SUCCEEDED", "")
		} else if state == "FAILED" || state == "TIMED_OUT" || state == "ABORTED" {
			err = c.Store.MarkVolumeOperation(ctx, o.ID, "FAILED", "")
		}
		if err != nil {
			return err
		}
	}
	return nil
}
