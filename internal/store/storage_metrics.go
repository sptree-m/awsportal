package store

import (
	"context"
	"fmt"
	"time"
)

const storageMetricSchema = `CREATE TABLE IF NOT EXISTS storage_snapshots(resource_id TEXT NOT NULL,observed_at INTEGER NOT NULL,payload TEXT NOT NULL,PRIMARY KEY(resource_id,observed_at));`

func (s *Store) RecordStorageSnapshot(ctx context.Context, id, payload string, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	key := fmt.Sprintf("usage/v3/storage/%s/%020d.json", id, now.Unix())
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO storage_snapshots VALUES(?,?,?)`, id, now.Unix(), payload); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO usage_export_queue(object_key,payload) VALUES(?,?)`, key, payload); err != nil {
		return err
	}
	return tx.Commit()
}
