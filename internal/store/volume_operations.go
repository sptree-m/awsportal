package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const volumeSchema = `CREATE TABLE IF NOT EXISTS volume_operations(id TEXT PRIMARY KEY,instance_id INTEGER NOT NULL REFERENCES instances(id),volume_id TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'RESERVED',execution_arn TEXT NOT NULL DEFAULT '',input TEXT NOT NULL,reason TEXT NOT NULL,created_at INTEGER NOT NULL);CREATE UNIQUE INDEX IF NOT EXISTS volume_operation_active ON volume_operations(volume_id) WHERE state!='SUCCEEDED';`

// Only recorded transient volumes on READY dynamic Shared instances are eligible.
// Uncertain/failed operations hold the instance until explicitly reconciled.
func (s *Store) RequestVolumePerformance(ctx context.Context, u User, iid int64, volume string, iops, throughput int64, reason string) error {
	if err := adminOnly(u); err != nil {
		return err
	}
	if strings.TrimSpace(reason) == "" || iops < 3000 || iops > 16000 || throughput < 125 || throughput > 1000 || throughput*4 > iops {
		return fmt.Errorf("reason and valid gp3 performance required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var awsID string
	var eid, generation int64
	err = tx.QueryRowContext(ctx, `SELECT i.instance_id,ei.environment_id,ei.generation FROM instances i JOIN environment_instances ei ON ei.instance_id=i.id JOIN dynamic_instances di ON di.instance_id=i.id JOIN instance_resources r ON r.instance_id=i.id WHERE i.id=? AND r.resource_id=? AND ei.lifecycle='READY' AND di.terminated_at=0 AND NOT EXISTS(SELECT 1 FROM cloud_operations o WHERE o.instance_id=i.id AND o.kind='TERMINATE' AND o.state NOT IN ('SUCCEEDED','CANCELLED'))`, iid, volume).Scan(&awsID, &eid, &generation)
	if err != nil {
		return fmt.Errorf("eligible READY managed transient volume required: %w", err)
	}
	id, err := operationID()
	if err != nil {
		return err
	}
	input, _ := json.Marshal(map[string]any{"operation_id": id, "kind": "MODIFY_VOLUME", "environment_id": eid, "generation": generation, "instance_id": awsID, "volume_id": volume, "iops": iops, "throughput": throughput})
	if _, err = tx.ExecContext(ctx, `INSERT INTO volume_operations(id,instance_id,volume_id,input,reason,created_at) VALUES(?,?,?,?,?,?)`, id, iid, volume, string(input), reason, time.Now().Unix()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE environment_instances SET idle_since=0 WHERE instance_id=?`, iid); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO environment_events(environment_id,instance_id,kind,detail,at) VALUES(?,?,'volume_performance',?,?)`, eid, iid, u.Username+" "+volume+": "+reason, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) VolumeOperations(ctx context.Context) ([]CloudOperation, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,instance_id,state,execution_arn,input,created_at FROM volume_operations ORDER BY CASE WHEN state='SUCCEEDED' THEN 1 ELSE 0 END,created_at DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CloudOperation
	for rows.Next() {
		var o CloudOperation
		o.Kind = "MODIFY_VOLUME"
		if err = rows.Scan(&o.ID, &o.InstanceID, &o.State, &o.ExecutionARN, &o.Input, &o.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
func (s *Store) MarkVolumeOperation(ctx context.Context, id, state, arn string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE volume_operations SET state=?,execution_arn=CASE WHEN ?='' THEN execution_arn ELSE ? END WHERE id=?`, state, arn, arn, id)
	return err
}
func (s *Store) RetryVolumeOperation(ctx context.Context, u User, id, reason string) error {
	if err := adminOnly(u); err != nil {
		return err
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("reconciliation reason required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var input string
	if err = tx.QueryRowContext(ctx, `SELECT input FROM volume_operations WHERE id=? AND state='FAILED'`, id).Scan(&input); err != nil {
		return err
	}
	var data map[string]any
	if err = json.Unmarshal([]byte(input), &data); err != nil {
		return err
	}
	n, _ := data["retry_attempt"].(float64)
	data["retry_attempt"] = int(n) + 1
	b, _ := json.Marshal(data)
	if _, err = tx.ExecContext(ctx, `UPDATE volume_operations SET state='RESERVED',execution_arn='',input=?,reason=? WHERE id=?`, string(b), reason, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO environment_events(kind,detail,at) VALUES('volume_reconcile',?,?)`, u.Username+" "+id+": "+reason, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
