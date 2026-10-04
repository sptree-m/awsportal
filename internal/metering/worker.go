package metering

import (
	"context"
	awsapi "github.com/sptree-m/awsportal/internal/aws"
	"github.com/sptree-m/awsportal/internal/store"
	"time"
)

type Meter interface {
	Snapshot(context.Context, string, time.Time) (awsapi.StorageSnapshot, error)
}
type Worker struct {
	Store *store.Store
	Meter Meter
}

// This worker has its own cadence and timeouts; AWS inventory cannot block seat
// reconciliation. Changed storage/boot lifecycle resources are retried next tick.
func (w *Worker) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			w.Tick(ctx, now)
		}
	}
}
func (w *Worker) Tick(ctx context.Context, now time.Time) error {
	if err := w.Store.ArchiveHistory(ctx); err != nil {
		return err
	}
	if inventory, ok := w.Meter.(interface {
		Inventory(context.Context, string) ([]string, string, error)
	}); ok {
		rs, err := w.Store.DB.QueryContext(ctx, `SELECT i.instance_id FROM instances i JOIN environment_instances ei ON ei.instance_id=i.id WHERE i.enabled=1 AND ei.lifecycle!='TERMINATED' ORDER BY i.id LIMIT 100`)
		if err != nil {
			return err
		}
		var instances []string
		for rs.Next() {
			var id string
			if err = rs.Scan(&id); err != nil {
				rs.Close()
				return err
			}
			instances = append(instances, id)
		}
		err = rs.Err()
		rs.Close()
		if err != nil {
			return err
		}
		for _, id := range instances {
			timeout, cancel := context.WithTimeout(ctx, 10*time.Second)
			volumes, raw, err := inventory.Inventory(timeout, id)
			cancel()
			if err != nil {
				continue
			}
			if err = w.Store.RecordInstanceInventory(ctx, id, volumes, raw, now); err != nil {
				return err
			}
		}
	}
	rows, err := w.Store.DB.QueryContext(ctx, `SELECT r.resource_id FROM resource_registry r WHERE r.valid_to IS NULL AND (r.resource_id LIKE 'fs-%' OR r.resource_id LIKE 'vol-%') AND NOT EXISTS(SELECT 1 FROM storage_snapshots ss WHERE ss.resource_id=r.resource_id AND ss.observed_at>?) LIMIT 100`, now.Add(-time.Hour).Unix())
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		timeout, cancel := context.WithTimeout(ctx, 10*time.Second)
		snapshot, err := w.Meter.Snapshot(timeout, id, now)
		cancel()
		if err != nil {
			continue
		}
		if err = w.Store.RecordStorageSnapshot(ctx, id, snapshot.Payload, now); err != nil {
			return err
		}
	}
	return nil
}
