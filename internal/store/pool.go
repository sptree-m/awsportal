package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const poolSchema = `
CREATE TABLE IF NOT EXISTS instance_resources(instance_id INTEGER NOT NULL REFERENCES instances(id),resource_id TEXT UNIQUE NOT NULL,PRIMARY KEY(instance_id,resource_id));
CREATE TABLE IF NOT EXISTS pool_controls(environment_id INTEGER PRIMARY KEY REFERENCES environments(id),scale_out INTEGER NOT NULL DEFAULT 0,terminate INTEGER NOT NULL DEFAULT 0,acceptance_ref TEXT NOT NULL DEFAULT '',revision INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS cloud_operations(id TEXT PRIMARY KEY,environment_id INTEGER NOT NULL REFERENCES environments(id),instance_id INTEGER REFERENCES instances(id),generation INTEGER NOT NULL,kind TEXT NOT NULL CHECK(kind IN ('PROVISION','TERMINATE')),state TEXT NOT NULL DEFAULT 'RESERVED',execution_arn TEXT NOT NULL DEFAULT '',input TEXT NOT NULL,created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,error TEXT NOT NULL DEFAULT '');
CREATE UNIQUE INDEX IF NOT EXISTS active_instance_operation ON cloud_operations(instance_id) WHERE kind='TERMINATE' AND state NOT IN ('SUCCEEDED','CANCELLED');
CREATE TABLE IF NOT EXISTS dynamic_instances(instance_id INTEGER PRIMARY KEY REFERENCES instances(id),operation_id TEXT UNIQUE NOT NULL REFERENCES cloud_operations(id),terminated_at INTEGER NOT NULL DEFAULT 0);
INSERT OR IGNORE INTO environment_migrations(version) VALUES(3);
`

type CloudOperation struct {
	ID, Kind, State, ExecutionARN, Input             string
	EnvironmentID, InstanceID, Generation, CreatedAt int64
}
type CloudResult struct {
	VolumeIDs []string `json:"volume_ids"`

	InstanceID        string `json:"instance_id"`
	Host              string `json:"host"`
	TokenHash         string `json:"token_hash"`
	ResourcesGone     bool   `json:"resources_gone"`
	CredentialRevoked bool   `json:"credential_revoked"`
}

// Both switches default off. acceptance_ref identifies the recorded real-machine
// stage-one acceptance, including the two-user five-business-day observation.
func (s *Store) SetPoolControl(ctx context.Context, admin User, eid int64, scale, terminate bool, acceptance, reason string) error {
	if err := adminOnly(admin); err != nil {
		return err
	}
	if reason == "" || ((scale || terminate) && acceptance == "") {
		return fmt.Errorf("acceptance evidence and reason required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var mode string
	if err = tx.QueryRowContext(ctx, `SELECT mode FROM environments WHERE id=?`, eid).Scan(&mode); err != nil {
		return err
	}
	if mode != "shared" {
		return fmt.Errorf("shared pool required")
	}
	if scale || terminate {
		var approved bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM golden_images WHERE profile_id='shared-cpu-v1' AND channel='STABLE')`).Scan(&approved); err != nil {
			return err
		}
		if !approved {
			return fmt.Errorf("validated stable CPU Golden AMI required")
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO pool_controls(environment_id,scale_out,terminate,acceptance_ref) VALUES(?,?,?,?) ON CONFLICT(environment_id) DO UPDATE SET scale_out=excluded.scale_out,terminate=excluded.terminate,acceptance_ref=excluded.acceptance_ref,revision=revision+1`, eid, scale, terminate, acceptance); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO environment_events(environment_id,kind,detail,at) VALUES(?,'pool_control',?,?)`, eid, fmt.Sprintf("%s scale=%v terminate=%v acceptance=%s: %s", admin.Username, scale, terminate, acceptance, reason), time.Now().Unix()); err != nil {
		return err
	}
	if !scale {
		if _, err = tx.ExecContext(ctx, `UPDATE cloud_operations SET state='CANCELLED' WHERE environment_id=? AND kind='PROVISION' AND state='RESERVED'`, eid); err != nil {
			return err
		}
	}
	if !terminate {
		if _, err = tx.ExecContext(ctx, `UPDATE cloud_operations SET state='CANCELLED' WHERE environment_id=? AND kind='TERMINATE' AND state IN ('RESERVED','DRAINING')`, eid); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE environment_instances SET lifecycle='READY',idle_since=0 WHERE environment_id=? AND lifecycle='DRAINING' AND NOT EXISTS(SELECT 1 FROM cloud_operations o WHERE o.instance_id=environment_instances.instance_id AND o.kind='TERMINATE' AND o.state NOT IN ('SUCCEEDED','CANCELLED'))`, eid); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func operationID() (string, error) {
	var b [16]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), err
}

// Reserve before calling AWS. Uncertain and failed operations consume capacity
// until an operator proves no resource exists; a timeout never frees a slot.
func (s *Store) ReservePoolOperations(ctx context.Context, now time.Time, scale, terminate bool) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if scale {
		rows, err := tx.QueryContext(ctx, `SELECT e.id,COUNT(r.id),MIN(r.reason) FROM environments e JOIN pool_controls p ON p.environment_id=e.id JOIN connection_requests r ON r.environment_id=e.id WHERE e.enabled=1 AND e.mode='shared' AND p.scale_out=1 AND p.acceptance_ref!='' AND r.state='WAITING' AND r.reason NOT IN ('HOME storage not configured','HOME held by another environment','HOME migration locked') GROUP BY e.id`)
		if err != nil {
			return err
		}
		type demand struct {
			id, n  int64
			reason string
		}
		var ds []demand
		for rows.Next() {
			var d demand
			if err = rows.Scan(&d.id, &d.n, &d.reason); err != nil {
				rows.Close()
				return err
			}
			ds = append(ds, d)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, d := range ds {
			var image, template, version, checksum string
			if err = tx.QueryRowContext(ctx, `SELECT ami_id,launch_template_id,launch_template_version,checksum FROM golden_images WHERE profile_id='shared-cpu-v1' AND channel='STABLE'`).Scan(&image, &template, &version, &checksum); err != nil {
				return err
			}

			var live, pending int64
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM environment_instances WHERE environment_id=? AND lifecycle!='TERMINATED'`, d.id).Scan(&live); err != nil {
				return err
			}
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM cloud_operations o WHERE environment_id=? AND kind='PROVISION' AND state NOT IN ('SUCCEEDED','CANCELLED')`, d.id).Scan(&pending); err != nil {
				return err
			}
			// A launching machine has two seats. Do not start another for those requests.
			var globalLive, globalPending int64
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM environment_instances ei JOIN environments e ON e.id=ei.environment_id WHERE e.mode='shared' AND ei.lifecycle!='TERMINATED'`).Scan(&globalLive); err != nil {
				return err
			}
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM cloud_operations WHERE kind='PROVISION' AND state NOT IN ('SUCCEEDED','CANCELLED')`).Scan(&globalPending); err != nil {
				return err
			}
			needed := (d.n+1)/2 - pending
			for needed > 0 && live+pending < 5 && globalLive+globalPending < 5 {
				id, err := operationID()
				if err != nil {
					return err
				}
				input, _ := json.Marshal(map[string]any{"operation_id": id, "environment_id": d.id, "generation": 1, "kind": "PROVISION", "expected_image_id": image, "expected_template_id": template, "expected_template_version": version, "expected_checksum": checksum, "scale_reason": d.reason})
				if _, err = tx.ExecContext(ctx, `INSERT INTO cloud_operations(id,environment_id,generation,kind,input,created_at,updated_at) VALUES(?,?,1,'PROVISION',?,?,?)`, id, d.id, string(input), now.Unix(), now.Unix()); err != nil {
					return err
				}
				pending++
				globalPending++
				needed--
			}
		}
	}
	if terminate {
		rows, err := tx.QueryContext(ctx, `SELECT ei.instance_id,ei.environment_id,ei.generation,i.instance_id FROM environment_instances ei JOIN instances i ON i.id=ei.instance_id JOIN dynamic_instances di ON di.instance_id=i.id JOIN pool_controls p ON p.environment_id=ei.environment_id WHERE p.terminate=1 AND p.acceptance_ref!='' AND ei.lifecycle='READY' AND ei.idle_since>0 AND ei.idle_since<=? AND NOT EXISTS(SELECT 1 FROM volume_operations v WHERE v.instance_id=i.id AND v.state!='SUCCEEDED') AND NOT EXISTS(SELECT 1 FROM cloud_operations o WHERE o.instance_id=i.id AND o.kind='TERMINATE' AND o.state NOT IN ('SUCCEEDED','CANCELLED'))`, now.Add(-15*time.Minute).Unix())
		if err != nil {
			return err
		}
		type candidate struct {
			i, e, g int64
			aws     string
		}
		var cs []candidate
		for rows.Next() {
			var c candidate
			if err = rows.Scan(&c.i, &c.e, &c.g, &c.aws); err != nil {
				rows.Close()
				return err
			}
			cs = append(cs, c)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, c := range cs {
			id, err := operationID()
			if err != nil {
				return err
			}
			input, _ := json.Marshal(map[string]any{"operation_id": id, "environment_id": c.e, "generation": c.g, "instance_id": c.aws, "kind": "TERMINATE"})
			if _, err = tx.ExecContext(ctx, `INSERT INTO cloud_operations(id,environment_id,instance_id,generation,kind,state,input,created_at,updated_at) VALUES(?,?,?,?,'TERMINATE','DRAINING',?,?,?)`, id, c.e, c.i, c.g, string(input), now.Unix(), now.Unix()); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE environment_instances SET lifecycle='DRAINING' WHERE instance_id=?`, c.i); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
func (s *Store) PendingCloudOperations(ctx context.Context) ([]CloudOperation, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,kind,state,execution_arn,input,environment_id,COALESCE(instance_id,0),generation,created_at FROM cloud_operations WHERE state NOT IN ('SUCCEEDED','CANCELLED','QUARANTINED') ORDER BY created_at,id LIMIT 20`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var xs []CloudOperation
	for rows.Next() {
		var x CloudOperation
		if err = rows.Scan(&x.ID, &x.Kind, &x.State, &x.ExecutionARN, &x.Input, &x.EnvironmentID, &x.InstanceID, &x.Generation, &x.CreatedAt); err != nil {
			return nil, err
		}
		xs = append(xs, x)
	}
	return xs, rows.Err()
}
func (s *Store) MarkCloudStarted(ctx context.Context, id, arn string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE cloud_operations SET state='RUNNING',execution_arn=?,updated_at=? WHERE id=? AND state IN ('RESERVED','SUBMITTING')`, arn, time.Now().Unix(), id)
	return err
}
func (s *Store) FinishCloudOperation(ctx context.Context, id string, result CloudResult, failed bool, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var kind, state string
	var eid, gen, iid int64
	if err = tx.QueryRowContext(ctx, `SELECT kind,state,environment_id,generation,COALESCE(instance_id,0) FROM cloud_operations WHERE id=?`, id).Scan(&kind, &state, &eid, &gen, &iid); err != nil {
		return err
	}
	if state == "SUCCEEDED" {
		return nil
	}
	if failed {
		_, err = tx.ExecContext(ctx, `UPDATE cloud_operations SET state='QUARANTINED',error='AWS workflow failed; resource reconciliation required',updated_at=? WHERE id=?`, now.Unix(), id)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	if kind == "PROVISION" {
		if !ec2ResourceID.MatchString(result.InstanceID) || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]{0,252}$`).MatchString(result.Host) || len(result.TokenHash) != 64 {
			return fmt.Errorf("invalid provision result")
		}
		if _, err = hex.DecodeString(result.TokenHash); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO instances(instance_id,name,dcv_host,dcv_connect_mode) VALUES(?,?,?,'native')`, result.InstanceID, "Shared "+id[:8], result.Host)
		if err != nil {
			return err
		}
		iid, err = res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO environment_instances(instance_id,environment_id,generation,lifecycle) VALUES(?,?,?,'READY')`, iid, eid, gen); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO dynamic_instances(instance_id,operation_id) VALUES(?,?)`, iid, id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO dcv_agents(instance_id,token_hash) VALUES(?,?)`, iid, result.TokenHash); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO resource_registry(resource_id,environment_id,group_id,valid_from) SELECT ?,id,group_id,? FROM environments WHERE id=?`, result.InstanceID, now.Unix(), eid); err != nil {
			return err
		}
		for _, volume := range result.VolumeIDs {
			if !regexp.MustCompile(`^vol-[a-f0-9]{8,17}$`).MatchString(volume) {
				return fmt.Errorf("invalid provisioned EBS identity")
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO resource_registry(resource_id,environment_id,group_id,valid_from) SELECT ?,id,group_id,? FROM environments WHERE id=?`, volume, now.Unix(), eid); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO instance_resources VALUES(?,?)`, iid, volume); err != nil {
				return err
			}
		}
	} else {
		if !result.ResourcesGone || !result.CredentialRevoked {
			return fmt.Errorf("cleanup proof required")
		}
		if _, err = tx.ExecContext(ctx, `UPDATE environment_instances SET lifecycle='TERMINATED' WHERE instance_id=? AND generation=?`, iid, gen); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE instances SET enabled=0 WHERE id=?`, iid); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM dcv_tokens WHERE instance_id=?`, iid); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM dcv_agents WHERE instance_id=?`, iid); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE dynamic_instances SET terminated_at=? WHERE instance_id=?`, now.Unix(), iid); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE resource_registry SET valid_to=? WHERE (resource_id=(SELECT instance_id FROM instances WHERE id=?) OR resource_id IN (SELECT resource_id FROM instance_resources WHERE instance_id=?)) AND valid_to IS NULL`, now.Unix(), iid, iid); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE cloud_operations SET state='SUCCEEDED',instance_id=?,updated_at=? WHERE id=?`, iid, now.Unix(), id); err != nil {
		return err
	}
	return tx.Commit()
}

var ec2ResourceID = regexpEC2()

func regexpEC2() *regexp.Regexp { return regexp.MustCompile(`^i-[a-f0-9]{8,17}$`) }

// Fence allocation while draining. Only a fresh, matching final Agent proof can
// authorize deletion. Pending requests or new work cancel before AWS submission.
func (s *Store) ReconcileDrain(ctx context.Context, o CloudOperation, now time.Time) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var raw string
	var received, idle, pending, seats int64
	err = tx.QueryRowContext(ctx, `SELECT ob.payload,ob.received_at,ei.idle_since,((SELECT COUNT(*) FROM connection_requests WHERE environment_id=ei.environment_id AND state='WAITING')+(SELECT COUNT(*) FROM dataset_cache_jobs c WHERE c.instance_id=ei.instance_id AND c.state IN ('PENDING','RUNNING'))+(SELECT COUNT(*) FROM volume_operations v WHERE v.instance_id=ei.instance_id AND v.state!='SUCCEEDED')),(SELECT COUNT(*) FROM environment_assignments WHERE instance_id=ei.instance_id AND state!='RELEASED') FROM environment_instances ei JOIN instance_observations ob ON ob.instance_id=ei.instance_id WHERE ei.instance_id=? AND ei.lifecycle='DRAINING' AND ei.generation=?`, o.InstanceID, o.Generation).Scan(&raw, &received, &idle, &pending, &seats)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var r EnvironmentReport
	err = json.Unmarshal([]byte(raw), &r)
	if err != nil {
		return false, err
	}
	safe := pending == 0 && seats == 0 && idle > 0 && received >= now.Add(-90*time.Second).Unix() && r.Generation == o.Generation && r.Error == "" && r.MetricsValid && r.CPU < 10 && !r.StorageBusy
	for _, w := range r.Work {
		if w.Connected || w.Jobs > 0 || w.Unclassified || w.SessionPresent || w.HomeMounted {
			safe = false
		}
	}
	if !safe {
		if _, err = tx.ExecContext(ctx, `UPDATE cloud_operations SET state='CANCELLED' WHERE id=? AND state='DRAINING'`, o.ID); err != nil {
			return false, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE environment_instances SET lifecycle='READY',idle_since=0 WHERE instance_id=?`, o.InstanceID); err != nil {
			return false, err
		}
		return false, tx.Commit()
	}
	ready := r.DrainOperation == o.ID && r.DrainComplete
	if ready {
		if _, err = tx.ExecContext(ctx, `UPDATE cloud_operations SET state='RESERVED' WHERE id=? AND state='DRAINING'`, o.ID); err != nil {
			return false, err
		}
	}
	return ready, tx.Commit()
}

func (s *Store) CloudSubmissionAllowed(ctx context.Context, o CloudOperation, global bool) (bool, error) {
	var enabled bool
	column := "scale_out"
	if o.Kind == "TERMINATE" {
		column = "terminate"
	}
	err := s.DB.QueryRowContext(ctx, `SELECT `+column+`=1 AND acceptance_ref!='' FROM pool_controls WHERE environment_id=?`, o.EnvironmentID).Scan(&enabled)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return global && enabled, err
}
func (s *Store) CancelUnsubmitted(ctx context.Context, o CloudOperation) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE cloud_operations SET state='CANCELLED' WHERE id=? AND state IN ('RESERVED','DRAINING')`, o.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n > 0 && o.Kind == "TERMINATE" {
		if _, err = tx.ExecContext(ctx, `UPDATE environment_instances SET lifecycle='READY',idle_since=0 WHERE instance_id=? AND generation=?`, o.InstanceID, o.Generation); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) MarkCloudSubmitting(ctx context.Context, id string) (bool, error) {
	res, err := s.DB.ExecContext(ctx, `UPDATE cloud_operations SET state='SUBMITTING',updated_at=? WHERE id=? AND state IN ('RESERVED','SUBMITTING')`, time.Now().Unix(), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// Reconcile with the original AWS ClientToken. Capacity is never released by
// this action. A termination must obtain a fresh root drain proof again.
func (s *Store) RetryCloudOperation(ctx context.Context, u User, id, reason string) error {
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
	var input, kind string
	var eid int64
	if err = tx.QueryRowContext(ctx, `SELECT input,kind,environment_id FROM cloud_operations WHERE id=? AND state='QUARANTINED'`, id).Scan(&input, &kind, &eid); err != nil {
		return err
	}
	var fields map[string]any
	if err = json.Unmarshal([]byte(input), &fields); err != nil {
		return err
	}
	attempt, _ := fields["retry_attempt"].(float64)
	fields["retry_attempt"] = int(attempt) + 1
	raw, _ := json.Marshal(fields)
	state := "RESERVED"
	if kind == "TERMINATE" {
		state = "DRAINING"
	}
	if _, err = tx.ExecContext(ctx, `UPDATE cloud_operations SET input=?,state=?,execution_arn='',error='',updated_at=? WHERE id=?`, string(raw), state, time.Now().Unix(), id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO environment_events(environment_id,kind,detail,at) VALUES(?,'cloud_reconcile',?,?)`, eid, u.Username+" "+id+": "+reason, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) CloudOperations(ctx context.Context) ([]CloudOperation, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,kind,state,execution_arn,input,environment_id,COALESCE(instance_id,0),generation,created_at FROM cloud_operations ORDER BY created_at DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CloudOperation
	for rows.Next() {
		var o CloudOperation
		if err = rows.Scan(&o.ID, &o.Kind, &o.State, &o.ExecutionARN, &o.Input, &o.EnvironmentID, &o.InstanceID, &o.Generation, &o.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
