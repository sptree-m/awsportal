package store

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

const inventorySchema = `CREATE TABLE IF NOT EXISTS instance_storage_inventory(instance_id INTEGER PRIMARY KEY,fingerprint TEXT NOT NULL,observed_at INTEGER NOT NULL);`

// Retain current configuration and immutable change/hourly events. Start/stop
// transitions observed by the minute collector are events even within the hour.
func (s *Store) RecordInstanceInventory(ctx context.Context, aws string, volumes []string, fingerprint string, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var iid, owner, group, eid int64
	if err = tx.QueryRowContext(ctx, `SELECT i.id,COALESCE(e.owner_user_id,0),e.group_id,e.id FROM instances i JOIN environment_instances ei ON ei.instance_id=i.id JOIN environments e ON e.id=ei.environment_id WHERE i.instance_id=?`, aws).Scan(&iid, &owner, &group, &eid); err != nil {
		return err
	}
	var previous string
	var at int64
	tx.QueryRowContext(ctx, `SELECT fingerprint,observed_at FROM instance_storage_inventory WHERE instance_id=?`, iid).Scan(&previous, &at)
	if previous == fingerprint && at > now.Add(-time.Hour).Unix() {
		return nil
	}
	for _, v := range volumes {
		if !regexp.MustCompile(`^vol-[a-f0-9]{8,17}$`).MatchString(v) {
			return fmt.Errorf("invalid EBS identity")
		}
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO instance_resources(instance_id,resource_id) VALUES(?,?)`, iid, v); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO resource_registry(resource_id,owner_user_id,group_id,environment_id,valid_from) SELECT ?,NULLIF(?,0),?,?,? WHERE NOT EXISTS(SELECT 1 FROM resource_registry WHERE resource_id=? AND valid_to IS NULL)`, v, owner, group, eid, now.Unix(), v); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO instance_storage_inventory VALUES(?,?,?) ON CONFLICT(instance_id) DO UPDATE SET fingerprint=excluded.fingerprint,observed_at=excluded.observed_at`, iid, fingerprint, now.Unix()); err != nil {
		return err
	}
	var record map[string]any
	if err = json.Unmarshal([]byte(fingerprint), &record); err != nil {
		return err
	}
	record["observed_at"] = now.Unix()
	record["record_type"] = "instance_storage_inventory"
	raw, _ := json.Marshal(record)
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO usage_export_queue(object_key,payload) VALUES(?,?)`, fmt.Sprintf("usage/v3/inventory/%s/%020d.json", aws, now.Unix()), string(raw)); err != nil {
		return err
	}
	return tx.Commit()
}
