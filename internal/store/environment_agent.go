package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

type HomeStorage struct {
	EFSID         string `json:"efs_id"`
	AccessPointID string `json:"access_point_id"`
	UID           int64  `json:"uid"`
	GID           int64  `json:"gid"`
	Revision      int64  `json:"revision"`
}
type DesktopMeasurement struct {
	Quality      string `json:"quality"`
	CounterEpoch string `json:"counter_epoch,omitempty"`
	CPUUsec      int64  `json:"cpu_usec,omitempty"`
	MemoryBytes  int64  `json:"memory_bytes,omitempty"`
	ReadBytes    int64  `json:"read_bytes,omitempty"`
	WriteBytes   int64  `json:"write_bytes,omitempty"`
}
type HomeMeasurement struct {
	Quality    string `json:"quality"`
	ObservedAt int64  `json:"observed_at,omitempty"`
	DuBytes    *int64 `json:"du_bytes,omitempty"`
}
type UserWork struct {
	HomeMeasurement    *HomeMeasurement    `json:"home_measurement,omitempty"`
	DesktopMeasurement *DesktopMeasurement `json:"desktop_measurement,omitempty"`
	UserID             int64               `json:"user_id"`
	Jobs               int                 `json:"jobs"`
	Unclassified       bool                `json:"unclassified"`
	SessionPresent     bool                `json:"session_present"`
	HomeMounted        bool                `json:"home_mounted"`
	StorageRevision    int64               `json:"storage_revision"`
	Connected          bool                `json:"connected"`
}
type EnvironmentReport struct {
	CacheResults       []CacheJob       `json:"cache_results,omitempty"`
	DrainOperation     string           `json:"drain_operation,omitempty"`
	DrainComplete      bool             `json:"drain_complete,omitempty"`
	MeasurementVersion int              `json:"measurement_version,omitempty"`
	JobMeasurements    []JobMeasurement `json:"job_measurements,omitempty"`
	AgentVersion       int              `json:"agent_version"`
	Generation         int64            `json:"generation"`
	BootID             string           `json:"boot_id"`
	Sequence           int64            `json:"sequence"`
	ObservedAt         int64            `json:"observed_at"`
	CPU                float64          `json:"cpu"`
	Memory             float64          `json:"memory"`
	MetricsValid       bool             `json:"metrics_valid"`
	StorageBusy        bool             `json:"storage_busy"`
	Work               []UserWork       `json:"work"`
	ClosedAssignments  []int64          `json:"closed_assignments"`
	ReadyUsers         []int64          `json:"ready_users"`
	AppliedRevision    int64            `json:"applied_revision"`
	BrowserBlocked     bool             `json:"browser_blocked"`
	Error              string           `json:"error"`
}
type EnvironmentAgentState struct {
	Groups         []GroupMount `json:"groups,omitempty"`
	CacheJobs      []CacheJob   `json:"cache_jobs,omitempty"`
	DrainOperation string       `json:"drain_operation,omitempty"`
	Personal       bool         `json:"personal,omitempty"`
	Shared         bool         `json:"shared"`
	Generation     int64        `json:"generation"`
	AgentVersion   int          `json:"agent_version"`
	Release        []DCVAccount `json:"release"`
}

func (s *Store) SharedDCVAccounts(ctx context.Context, awsID string, now time.Time) ([]DCVAccount, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT u.id,u.username,ea.id,us.efs_id,us.access_point_id,us.revision FROM users u JOIN instances i ON i.instance_id=? JOIN environment_instances ei ON ei.instance_id=i.id JOIN environments e ON e.id=ei.environment_id JOIN environment_assignments ea ON ea.instance_id=i.id AND ea.user_id=u.id JOIN user_storage us ON us.user_id=u.id WHERE i.enabled=1 AND ea.state NOT IN ('RELEASED','RELEASING') AND ea.generation=ei.generation AND ea.storage_revision=us.revision AND `+environmentAccess+` ORDER BY u.id`, awsID, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DCVAccount{}
	for rows.Next() {
		var a DCVAccount
		h := HomeStorage{}
		if err = rows.Scan(&a.UserID, &a.Username, &a.AssignmentID, &h.EFSID, &h.AccessPointID, &h.Revision); err != nil {
			return nil, err
		}
		a.OSUser = DCVIdentity(a.UserID)
		a.SessionID = a.OSUser
		h.UID = 200000 + a.UserID
		h.GID = h.UID
		a.Home = &h
		a.StorageRevision = h.Revision
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) EnvironmentAgentState(ctx context.Context, awsID string) (EnvironmentAgentState, error) {
	out := EnvironmentAgentState{AgentVersion: 2, Release: []DCVAccount{}}
	var storageErr error
	out.Groups, out.CacheJobs, storageErr = s.AgentStorage(ctx, awsID)
	if storageErr != nil {
		return out, storageErr
	}
	err := s.DB.QueryRowContext(ctx, `SELECT e.mode='shared',e.mode='personal',ei.generation FROM instances i JOIN environment_instances ei ON ei.instance_id=i.id JOIN environments e ON e.id=ei.environment_id WHERE i.instance_id=?`, awsID).Scan(&out.Shared, &out.Personal, &out.Generation)
	if err == sql.ErrNoRows {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	_ = s.DB.QueryRowContext(ctx, `SELECT o.id FROM cloud_operations o JOIN instances i ON i.id=o.instance_id WHERE i.instance_id=? AND o.kind='TERMINATE' AND o.state IN ('DRAINING','RESERVED','SUBMITTING','RUNNING')`, awsID).Scan(&out.DrainOperation)
	if !out.Shared {
		return out, nil
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT ea.id,ea.user_id FROM environment_assignments ea JOIN instances i ON i.id=ea.instance_id WHERE i.instance_id=? AND ea.state='RELEASING'`, awsID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var x DCVAccount
		if err = rows.Scan(&x.AssignmentID, &x.UserID); err != nil {
			return out, err
		}
		x.OSUser = DCVIdentity(x.UserID)
		x.SessionID = x.OSUser
		out.Release = append(out.Release, x)
	}
	return out, rows.Err()
}
func (s *Store) SharedDCVConnection(ctx context.Context, awsID string, uid int64, now time.Time) (managed, ready bool, mode string, err error) {
	mode = "native"
	managed = true
	var raw string
	err = s.DB.QueryRowContext(ctx, `SELECT o.payload FROM instances i JOIN dcv_agents da ON da.instance_id=i.id JOIN instance_observations o ON o.instance_id=i.id JOIN users u ON u.id=? WHERE i.instance_id=? AND i.enabled=1 AND da.error='' AND da.last_seen>=? AND o.received_at>=? AND da.applied_revision=i.dcv_policy_revision AND da.browser_blocked=1 AND `+sharedDCVAccess, uid, awsID, now.Add(-90*time.Second).Unix(), now.Add(-90*time.Second).Unix(), now.Unix()).Scan(&raw)
	if err == sql.ErrNoRows {
		err = nil
		return
	}
	if err != nil {
		return
	}
	var report EnvironmentReport
	err = json.Unmarshal([]byte(raw), &report)
	if err != nil {
		return
	}
	for _, w := range report.Work {
		if w.UserID == uid && w.HomeMounted && w.SessionPresent {
			for _, id := range report.ReadyUsers {
				if id == uid {
					ready = true
				}
			}
		}
	}
	return
}
func (s *Store) EnvironmentHeartbeat(ctx context.Context, awsID string, r EnvironmentReport, now time.Time) error {
	if r.AgentVersion != 2 || r.Generation < 1 || r.Sequence < 1 || len(r.BootID) < 8 || len(r.BootID) > 64 || strings.ContainsAny(r.BootID, "/\r\n") || r.ObservedAt > now.Add(30*time.Second).Unix() || r.ObservedAt < now.Add(-90*time.Second).Unix() || len(r.Work) > 50 || len(r.Error) > 512 {
		return fmt.Errorf("invalid v2 report")
	}
	if !r.MetricsValid || math.IsNaN(r.CPU) || math.IsInf(r.CPU, 0) || math.IsNaN(r.Memory) || math.IsInf(r.Memory, 0) || r.CPU < 0 || r.CPU > 100 || r.Memory < 0 || r.Memory > 100 {
		return fmt.Errorf("invalid metrics")
	}
	seen := map[int64]UserWork{}
	for _, w := range r.Work {
		if w.UserID < 1 || w.Jobs < 0 || w.Jobs > 10000 {
			return fmt.Errorf("invalid work report")
		}
		if _, ok := seen[w.UserID]; ok {
			return fmt.Errorf("duplicate work report")
		}
		seen[w.UserID] = w
	}
	for _, uid := range r.ReadyUsers {
		w, ok := seen[uid]
		if !ok || !w.HomeMounted || !w.SessionPresent {
			return fmt.Errorf("HOME and session proof required")
		}
	}
	if err := validateJobMeasurements(r); err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var iid, eid, gen, policy, owner int64
	var mode string
	if err = tx.QueryRowContext(ctx, `SELECT i.id,ei.environment_id,ei.generation,i.dcv_policy_revision,e.mode,COALESCE(e.owner_user_id,0) FROM instances i JOIN environment_instances ei ON ei.instance_id=i.id JOIN environments e ON e.id=ei.environment_id WHERE i.instance_id=?`, awsID).Scan(&iid, &eid, &gen, &policy, &mode, &owner); err != nil {
		return err
	}
	if gen != r.Generation {
		return fmt.Errorf("stale generation")
	}
	var boot, previousPayload string
	var seq int64
	err = tx.QueryRowContext(ctx, `SELECT boot_id,sequence,payload FROM instance_observations WHERE instance_id=?`, iid).Scan(&boot, &seq, &previousPayload)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	// Identical retries are acknowledged without renewing freshness. Changed retries
	// and out-of-order samples can never overwrite a newer observation.
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT payload FROM instance_samples WHERE instance_id=? AND boot_id=? AND sequence=?`, iid, r.BootID, r.Sequence).Scan(&existing)
	if err == nil {
		if existing != string(raw) {
			return fmt.Errorf("changed replay")
		}
		return tx.Commit()
	}
	if err != sql.ErrNoRows {
		return err
	}
	if boot == r.BootID && r.Sequence <= seq {
		return fmt.Errorf("out-of-order sequence")
	}
	if boot != "" && boot != r.BootID {
		var retired int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM instance_boots WHERE instance_id=? AND boot_id=?`, iid, r.BootID).Scan(&retired); err != nil {
			return err
		}
		if retired > 0 {
			return fmt.Errorf("retired boot")
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,user_id,state,storage_revision FROM environment_assignments WHERE instance_id=? AND state!='RELEASED'`, iid)
	if err != nil {
		return err
	}
	type assignment struct {
		id, uid, storage int64
		state            string
	}
	var assignments []assignment
	for rows.Next() {
		var a assignment
		if err = rows.Scan(&a.id, &a.uid, &a.state, &a.storage); err != nil {
			rows.Close()
			return err
		}
		assignments = append(assignments, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	allowedReady := map[int64]bool{}
	for _, a := range assignments {
		w, has := seen[a.uid]
		if has && w.StorageRevision == a.storage && a.state != "RELEASING" {
			allowedReady[a.uid] = true
		}
	}
	if mode == "personal" {
		var storage int64
		var held bool
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT revision FROM user_storage WHERE user_id=?),0),EXISTS(SELECT 1 FROM personal_home_leases WHERE user_id=? AND instance_id=?)`, owner, owner, iid).Scan(&storage, &held); err != nil {
			return err
		}
		if w, ok := seen[owner]; ok && held && w.StorageRevision == storage && w.HomeMounted && w.SessionPresent {
			allowedReady[owner] = true
		}
		// Unmigrated Personal environments keep the legacy local-HOME protocol.
		if storage == 0 {
			if w, ok := seen[owner]; ok && w.SessionPresent {
				allowedReady[owner] = true
			}
		}
	}
	for _, uid := range r.ReadyUsers {
		if !allowedReady[uid] {
			return fmt.Errorf("user is not prepared for this assignment")
		}
	}
	if err = recordJobMeasurements(ctx, tx, iid, eid, r, now); err != nil {
		return err
	}
	// Reset the continuous idle interval at the instant any unsafe sample is
	// received, including events occurring between controller ticks.
	idleSafe := r.Error == "" && r.BrowserBlocked && r.AppliedRevision == policy && r.CPU < 10 && !r.StorageBusy
	for _, w := range r.Work {
		if w.Connected || w.Jobs > 0 || w.Unclassified || w.SessionPresent {
			idleSafe = false
		}
	}
	var previous EnvironmentReport
	if boot != "" {
		_ = json.Unmarshal([]byte(previousPayload), &previous)
	}
	if !idleSafe || boot != r.BootID || r.ObservedAt-previous.ObservedAt > 90 {
		if _, err = tx.ExecContext(ctx, `UPDATE environment_instances SET idle_since=0 WHERE instance_id=?`, iid); err != nil {
			return err
		}
	}
	if r.AgentVersion == 2 && r.Error == "" && r.MetricsValid && r.BrowserBlocked && r.AppliedRevision == policy {
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO instance_readiness VALUES(?,?)`, iid, now.Unix()); err != nil {
			return err
		}
	}
	if err = recordDesktop(ctx, tx, iid, r, previous); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO instance_boots VALUES(?,?,?)`, iid, r.BootID, now.Unix()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO instance_samples VALUES(?,?,?,?,?,?,?,?,?)`, iid, r.BootID, r.Sequence, r.ObservedAt, now.Unix(), gen, r.CPU, r.Memory, string(raw)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO instance_observations VALUES(?,?,?,?,?) ON CONFLICT(instance_id) DO UPDATE SET boot_id=excluded.boot_id,sequence=excluded.sequence,received_at=excluded.received_at,payload=excluded.payload`, iid, r.BootID, r.Sequence, now.Unix(), string(raw)); err != nil {
		return err
	}
	readyRaw, err := json.Marshal(r.ReadyUsers)
	if err != nil {
		return err
	}
	if r.ReadyUsers == nil {
		readyRaw = []byte("[]")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE dcv_agents SET last_seen=?,ready_users=?,error=?,applied_revision=?,browser_blocked=? WHERE instance_id=?`, now.Unix(), string(readyRaw), r.Error, r.AppliedRevision, r.BrowserBlocked, iid); err != nil {
		return err
	}
	for _, a := range assignments {
		w, has := seen[a.uid]
		if !has {
			continue
		}
		next := "PREPARING"
		requestState := "PREPARING_USER"
		if a.state == "RELEASING" {
			closed := false
			for _, id := range r.ClosedAssignments {
				if id == a.id {
					closed = true
				}
			}
			if !closed || w.SessionPresent || w.Connected || w.Jobs > 0 || w.Unclassified {
				continue
			}
			next = "RELEASED"
			requestState = "RELEASED"
		} else {
			ready := false
			for _, uid := range r.ReadyUsers {
				if uid == a.uid {
					ready = true
				}
			}
			if ready && r.AppliedRevision == policy && r.BrowserBlocked && r.Error == "" {
				next = "READY"
				requestState = "READY"
				if w.Connected {
					next = "CONNECTED"
					requestState = "CONNECTED"
				} else if a.state == "CONNECTED" || a.state == "DISCONNECTED_GRACE" || a.state == "JOB_HELD" {
					next = "DISCONNECTED_GRACE"
					if w.Jobs > 0 || w.Unclassified {
						next = "JOB_HELD"
					}
				}
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE environment_assignments SET state=? WHERE id=?`, next, a.id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE connection_requests SET state=?,reason=? WHERE id=(SELECT request_id FROM environment_assignments WHERE id=?)`, requestState, next, a.id); err != nil {
			return err
		}
		if next != a.state {
			if _, err = tx.ExecContext(ctx, `INSERT INTO environment_events(environment_id,instance_id,user_id,kind,detail,at) VALUES(?,?,?,'assignment_changed',?,?)`, eid, iid, a.uid, a.state+" -> "+next, now.Unix()); err != nil {
				return err
			}
		}
	}
	if json.Unmarshal([]byte(previousPayload), &previous) == nil && previous.BootID == r.BootID && previous.Generation == r.Generation && previous.Sequence+1 == r.Sequence && r.ObservedAt > previous.ObservedAt && r.ObservedAt-previous.ObservedAt <= 90 {
		for _, before := range previous.Work {
			if !before.Connected {
				continue
			}
			for _, after := range r.Work {
				if after.UserID != before.UserID || !after.Connected {
					continue
				}
				if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO connection_intervals(instance_id,user_id,environment_id,group_id,boot_id,sequence,generation,start_at,end_at) SELECT ?,?,e.id,COALESCE(e.group_id,0),?,?,?,?,? FROM environments e WHERE e.id=?`, iid, after.UserID, r.BootID, r.Sequence, r.Generation, previous.ObservedAt, r.ObservedAt, eid); err != nil {
					return err
				}
			}
		}
	}
	key := fmt.Sprintf("usage/v2/%s/%s/%020d.json", awsID, r.BootID, r.Sequence)
	if _, err = tx.ExecContext(ctx, `INSERT INTO usage_export_queue(object_key,payload) VALUES(?,?)`, key, string(raw)); err != nil {
		return err
	}
	return tx.Commit()
}

type IdleDecision struct {
	InstanceID, Reason        string
	Candidate                 bool
	Since                     int64
	Occupied, Connected, Jobs int
}

func (s *Store) RecordIdleDecisions(ctx context.Context, now time.Time) ([]IdleDecision, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT i.id,i.instance_id,ei.environment_id,ei.idle_since,ei.generation,i.dcv_policy_revision,COALESCE(o.received_at,0),COALESCE(o.payload,''),(SELECT COUNT(*) FROM environment_assignments a WHERE a.instance_id=i.id AND a.state!='RELEASED'),((SELECT COUNT(*) FROM connection_requests r WHERE r.environment_id=ei.environment_id AND r.state='WAITING')+(SELECT COUNT(*) FROM dataset_cache_jobs c WHERE c.instance_id=i.id AND c.state IN ('PENDING','RUNNING'))+(SELECT COUNT(*) FROM volume_operations v WHERE v.instance_id=i.id AND v.state!='SUCCEEDED')) FROM environment_instances ei JOIN environments e ON e.id=ei.environment_id JOIN instances i ON i.id=ei.instance_id LEFT JOIN instance_observations o ON o.instance_id=i.id WHERE e.mode='shared'`)
	if err != nil {
		return nil, err
	}
	type item struct {
		iid, eid, received, generation, policy int64
		raw                                    string
		pending                                int
		IdleDecision
	}
	var items []item
	for rows.Next() {
		var x item
		if err = rows.Scan(&x.iid, &x.InstanceID, &x.eid, &x.Since, &x.generation, &x.policy, &x.received, &x.raw, &x.Occupied, &x.pending); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []IdleDecision{}
	for _, x := range items {
		x.Reason = "idle observation only; automatic termination disabled"
		var r EnvironmentReport
		valid := json.Unmarshal([]byte(x.raw), &r) == nil && r.MetricsValid && r.Error == "" && r.BrowserBlocked && r.Generation == x.generation && r.AppliedRevision == x.policy
		safe := valid && x.received >= now.Add(-90*time.Second).Unix() && x.Occupied == 0 && x.pending == 0 && !r.StorageBusy && r.CPU < 10
		for _, w := range r.Work {
			if w.Connected {
				x.Connected++
			}
			x.Jobs += w.Jobs
			if w.Connected || w.Jobs > 0 || w.Unclassified || w.SessionPresent {
				safe = false
			}
		}
		if !valid || x.received < now.Add(-90*time.Second).Unix() {
			x.Reason = "metrics unavailable or stale"
		} else if x.Occupied > 0 {
			x.Reason = "occupied HOME/seat lease"
		} else if x.pending > 0 {
			x.Reason = "pending requests"
		} else if !safe {
			x.Reason = "connection, job, desktop, work, storage or CPU holds instance"
		}
		if !safe {
			x.Since = 0
		} else {
			if x.Since == 0 {
				x.Since = now.Unix()
			}
			x.Candidate = now.Unix()-x.Since >= 15*60
		}
		if _, err = tx.ExecContext(ctx, `UPDATE environment_instances SET idle_since=?,idle_reason=? WHERE instance_id=?`, x.Since, x.Reason, x.iid); err != nil {
			return nil, err
		}
		if valid && x.received >= now.Add(-90*time.Second).Unix() {
			if err = recordLoadMinute(ctx, tx, x.iid, r, now); err != nil {
				return nil, err
			}
		}
		if err = recordOperationObservation(ctx, tx, x.iid, x.Reason, safe, now); err != nil {
			return nil, err
		}
		out = append(out, x.IdleDecision)
	}
	return out, tx.Commit()
}
