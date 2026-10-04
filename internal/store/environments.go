package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Stage two capabilities remain off until operator flags and acceptance evidence
// authorize the separately scoped AWS workflows.
const ImplementationStage = 2
const environmentSchema = `
CREATE TABLE IF NOT EXISTS environment_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS environments(
 id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT UNIQUE NOT NULL, mode TEXT NOT NULL CHECK(mode IN ('personal','shared')),
 group_id INTEGER REFERENCES groups(id), owner_user_id INTEGER REFERENCES users(id), profile_id TEXT NOT NULL,
 enabled INTEGER NOT NULL DEFAULT 1, revision INTEGER NOT NULL DEFAULT 1,
 CHECK((mode='personal' AND owner_user_id IS NOT NULL) OR (mode='shared' AND owner_user_id IS NULL AND group_id IS NOT NULL)));
CREATE TABLE IF NOT EXISTS environment_acl(environment_id INTEGER NOT NULL REFERENCES environments(id), kind TEXT NOT NULL CHECK(kind IN ('user','group')), subject_id INTEGER NOT NULL, permission TEXT NOT NULL CHECK(permission IN ('environment.connect','environment.manage','dataset.import')), PRIMARY KEY(environment_id,kind,subject_id,permission));
CREATE TABLE IF NOT EXISTS environment_instances(instance_id INTEGER PRIMARY KEY REFERENCES instances(id), environment_id INTEGER NOT NULL REFERENCES environments(id), generation INTEGER NOT NULL DEFAULT 1, lifecycle TEXT NOT NULL DEFAULT 'READY', idle_since INTEGER NOT NULL DEFAULT 0, idle_reason TEXT NOT NULL DEFAULT 'unknown');
CREATE TABLE IF NOT EXISTS user_storage(user_id INTEGER PRIMARY KEY REFERENCES users(id), efs_id TEXT UNIQUE NOT NULL, access_point_id TEXT UNIQUE NOT NULL, revision INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS connection_requests(id INTEGER PRIMARY KEY AUTOINCREMENT, environment_id INTEGER NOT NULL REFERENCES environments(id), user_id INTEGER NOT NULL REFERENCES users(id), idempotency_key TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'WAITING', reason TEXT NOT NULL DEFAULT 'pending', created_at INTEGER NOT NULL, UNIQUE(environment_id,user_id,idempotency_key));
CREATE UNIQUE INDEX IF NOT EXISTS active_environment_request ON connection_requests(environment_id,user_id) WHERE state NOT IN ('RELEASED','CANCELLED','FAILED','TIMED_OUT');
CREATE TABLE IF NOT EXISTS environment_assignments(id INTEGER PRIMARY KEY AUTOINCREMENT, request_id INTEGER UNIQUE NOT NULL REFERENCES connection_requests(id), instance_id INTEGER NOT NULL REFERENCES instances(id), user_id INTEGER NOT NULL REFERENCES users(id), state TEXT NOT NULL DEFAULT 'PREPARING', generation INTEGER NOT NULL, storage_revision INTEGER NOT NULL, created_at INTEGER NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS interactive_home_lease ON environment_assignments(user_id) WHERE state!='RELEASED';
CREATE TABLE IF NOT EXISTS instance_samples(instance_id INTEGER NOT NULL REFERENCES instances(id), boot_id TEXT NOT NULL, sequence INTEGER NOT NULL, observed_at INTEGER NOT NULL, received_at INTEGER NOT NULL, generation INTEGER NOT NULL, cpu REAL NOT NULL, memory REAL NOT NULL, payload TEXT NOT NULL, PRIMARY KEY(instance_id,boot_id,sequence));
CREATE TABLE IF NOT EXISTS instance_observations(instance_id INTEGER PRIMARY KEY REFERENCES instances(id), boot_id TEXT NOT NULL, sequence INTEGER NOT NULL, received_at INTEGER NOT NULL, payload TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS usage_export_queue(id INTEGER PRIMARY KEY AUTOINCREMENT, object_key TEXT UNIQUE NOT NULL, payload TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'PENDING', retry_count INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS environment_events(id INTEGER PRIMARY KEY AUTOINCREMENT, environment_id INTEGER REFERENCES environments(id), instance_id INTEGER REFERENCES instances(id), user_id INTEGER REFERENCES users(id), kind TEXT NOT NULL, detail TEXT NOT NULL, at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS resource_registry(id INTEGER PRIMARY KEY AUTOINCREMENT, resource_id TEXT NOT NULL, environment_id INTEGER REFERENCES environments(id), owner_user_id INTEGER REFERENCES users(id), group_id INTEGER REFERENCES groups(id), valid_from INTEGER NOT NULL, valid_to INTEGER);
CREATE UNIQUE INDEX IF NOT EXISTS current_resource_owner ON resource_registry(resource_id) WHERE valid_to IS NULL;
INSERT OR IGNORE INTO environment_migrations(version) VALUES(1);
`

var ErrEnvironmentDenied = errors.New("environment access denied")
var awsStorageID = regexp.MustCompile(`^(fs|fsap)-[a-f0-9]{8,17}$`)

type Environment struct {
	ID                    int64
	Name, Mode, ProfileID string
	GroupID, OwnerUserID  int64
	Enabled               bool
	Revision              int64
}
type ConnectionRequest struct {
	ID, EnvironmentID, UserID, AssignmentID int64
	State, Reason, InstanceID               string
}

const environmentAccess = `(u.enabled=1 AND u.must_change_password=0 AND (u.role='portal_admin' OR u.expires_at=0 OR u.expires_at>=?) AND e.enabled=1 AND (u.role='portal_admin' OR (e.mode='personal' AND e.owner_user_id=u.id) OR EXISTS(SELECT 1 FROM environment_acl acl WHERE acl.environment_id=e.id AND acl.permission='environment.connect' AND ((acl.kind='user' AND acl.subject_id=u.id) OR (acl.kind='group' AND EXISTS(SELECT 1 FROM group_members gm WHERE gm.group_id=acl.subject_id AND gm.user_id=u.id))))))`

// Applied inside instance/DCV queries with aliases i/u. A Shared instance never
// inherits legacy instance grants or administrator automatic sessions.
const sharedDCVAccess = `EXISTS(SELECT 1 FROM environment_instances ei JOIN environments e ON e.id=ei.environment_id JOIN environment_assignments ea ON ea.instance_id=ei.instance_id WHERE ei.instance_id=i.id AND e.mode='shared' AND ea.user_id=u.id AND ea.state NOT IN ('RELEASED','RELEASING') AND ea.generation=ei.generation AND ` + environmentAccess + `)`
const personalDCVAccess = `NOT EXISTS(SELECT 1 FROM environment_instances pe JOIN environments p ON p.id=pe.environment_id WHERE pe.instance_id=i.id AND p.mode='personal' AND p.owner_user_id!=u.id) AND NOT EXISTS(SELECT 1 FROM home_migrations hm WHERE hm.user_id=u.id AND hm.state='LOCKED')`

const nonSharedInstance = `NOT EXISTS(SELECT 1 FROM environment_instances ei JOIN environments e ON e.id=ei.environment_id WHERE ei.instance_id=i.id AND e.mode='shared')`

func (s *Store) CreateEnvironment(ctx context.Context, admin User, x Environment, reason string) (int64, error) {
	if err := adminOnly(admin); err != nil {
		return 0, err
	}
	x.Name = strings.TrimSpace(x.Name)
	reason = strings.TrimSpace(reason)
	if x.Name == "" || len(x.Name) > 100 || reason == "" || len(reason) > 512 || x.ProfileID != "shared-cpu-v1" && x.ProfileID != "personal-v1" {
		return 0, fmt.Errorf("name, approved profile and reason required")
	}
	if x.Mode == "shared" && x.ProfileID != "shared-cpu-v1" || x.Mode == "personal" && x.ProfileID != "personal-v1" {
		return 0, fmt.Errorf("profile does not match mode")
	}
	var group, owner any
	if x.GroupID > 0 {
		group = x.GroupID
	}
	if x.OwnerUserID > 0 {
		owner = x.OwnerUserID
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO environments(name,mode,group_id,owner_user_id,profile_id) VALUES(?,?,?,?,?)`, x.Name, x.Mode, group, owner, x.ProfileID)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO environment_events(environment_id,kind,detail,at) VALUES(?,'created',?,?)`, id, admin.Username+": "+reason, time.Now().Unix()); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}
func (s *Store) SetEnvironmentACL(ctx context.Context, admin User, eid int64, kind string, subject int64, permission string, remove bool, reason string) error {
	if err := adminOnly(admin); err != nil {
		return err
	}
	if kind != "user" && kind != "group" || subject < 1 || strings.TrimSpace(reason) == "" || len(reason) > 512 {
		return fmt.Errorf("invalid ACL")
	}
	if permission != "environment.connect" && permission != "environment.manage" && permission != "dataset.import" {
		return fmt.Errorf("invalid permission")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	table := "users"
	if kind == "group" {
		table = "groups"
	}
	var exists int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE id=?", subject).Scan(&exists); err != nil || exists != 1 {
		return fmt.Errorf("unknown ACL subject")
	}
	query := `INSERT OR IGNORE INTO environment_acl VALUES(?,?,?,?)`
	if remove {
		query = `DELETE FROM environment_acl WHERE environment_id=? AND kind=? AND subject_id=? AND permission=?`
	}
	if _, err = tx.ExecContext(ctx, query, eid, kind, subject, permission); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE environments SET revision=revision+1 WHERE id=?`, eid); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO environment_events(environment_id,kind,detail,at) VALUES(?,'acl_changed',?,?)`, eid, fmt.Sprintf("%s %s %d %s remove=%v: %s", admin.Username, kind, subject, permission, remove, reason), time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) RegisterEnvironmentInstance(ctx context.Context, admin User, eid int64, awsID, reason string) error {
	if err := adminOnly(admin); err != nil {
		return err
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("reason required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var iid, owner, group int64
	var mode string
	if err = tx.QueryRowContext(ctx, `SELECT i.id,e.mode,COALESCE(e.owner_user_id,0),COALESCE(e.group_id,0) FROM instances i,environments e WHERE i.instance_id=? AND e.id=?`, awsID, eid).Scan(&iid, &mode, &owner, &group); err != nil {
		return err
	}
	// Fixed pilot: exactly one registered instance. Stage two will lift this.
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM environment_instances WHERE environment_id=?`, eid).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("stage one allows one fixed instance")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO environment_instances(instance_id,environment_id) VALUES(?,?)`, iid, eid); err != nil {
		return err
	}
	if mode == "personal" {
		if _, err = tx.ExecContext(ctx, `DELETE FROM instance_users WHERE instance_id=?`, iid); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM instance_groups WHERE instance_id=?`, iid); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO instance_users(instance_id,user_id,can_control) VALUES(?,?,1)`, iid, owner); err != nil {
			return err
		}
	}
	if mode == "shared" {
		if _, err = tx.ExecContext(ctx, `UPDATE schedules SET enabled=0 WHERE instance_id=?`, iid); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM dcv_tokens WHERE instance_id=?`, iid); err != nil {
			return err
		}
	}
	var o, g any
	if owner > 0 {
		o = owner
	}
	if group > 0 {
		g = group
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO resource_registry(resource_id,environment_id,owner_user_id,group_id,valid_from) VALUES(?,?,?,?,?)`, awsID, eid, o, g, time.Now().Unix()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO environment_events(environment_id,instance_id,kind,detail,at) VALUES(?,?,'registered',?,?)`, eid, iid, admin.Username+": "+reason, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) SetUserStorage(ctx context.Context, admin User, uid int64, efs, ap, reason string) error {
	if err := adminOnly(admin); err != nil {
		return err
	}
	if !awsStorageID.MatchString(efs) || !strings.HasPrefix(efs, "fs-") || !awsStorageID.MatchString(ap) || !strings.HasPrefix(ap, "fsap-") || strings.TrimSpace(reason) == "" {
		return fmt.Errorf("valid EFS/access point and reason required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var held int
	if err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM environment_assignments WHERE user_id=? AND state!='RELEASED')+(SELECT COUNT(*) FROM personal_home_leases WHERE user_id=?)`, uid, uid).Scan(&held); err != nil {
		return err
	}
	if held > 0 {
		return fmt.Errorf("HOME is leased; release it before migration")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE resource_registry SET valid_to=? WHERE owner_user_id=? AND valid_to IS NULL AND resource_id=(SELECT efs_id FROM user_storage WHERE user_id=?) AND resource_id!=?`, time.Now().Unix(), uid, uid, efs); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO resource_registry(resource_id,owner_user_id,valid_from) SELECT ?,?,? WHERE NOT EXISTS(SELECT 1 FROM resource_registry WHERE resource_id=? AND valid_to IS NULL)`, efs, uid, time.Now().Unix(), efs); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO user_storage(user_id,efs_id,access_point_id) VALUES(?,?,?) ON CONFLICT(user_id) DO UPDATE SET efs_id=excluded.efs_id,access_point_id=excluded.access_point_id,revision=revision+1`, uid, efs, ap); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO environment_events(user_id,kind,detail,at) VALUES(?,'storage_changed',?,?)`, uid, admin.Username+": "+reason, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) VisibleEnvironments(ctx context.Context, user User) ([]Environment, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT e.id,e.name,e.mode,e.profile_id,COALESCE(e.group_id,0),COALESCE(e.owner_user_id,0),e.enabled,e.revision FROM environments e JOIN users u ON u.id=? WHERE `+environmentAccess+` ORDER BY e.name`, user.ID, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Environment{}
	for rows.Next() {
		var x Environment
		if err = rows.Scan(&x.ID, &x.Name, &x.Mode, &x.ProfileID, &x.GroupID, &x.OwnerUserID, &x.Enabled, &x.Revision); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func environmentAllowed(ctx context.Context, tx *sql.Tx, uid, eid int64, now time.Time) bool {
	var ok bool
	err := tx.QueryRowContext(ctx, `SELECT `+environmentAccess+` FROM environments e JOIN users u ON u.id=? WHERE e.id=?`, now.Unix(), uid, eid).Scan(&ok)
	return err == nil && ok
}
func requestRow(ctx context.Context, tx *sql.Tx, id int64) (ConnectionRequest, error) {
	var x ConnectionRequest
	err := tx.QueryRowContext(ctx, `SELECT r.id,r.environment_id,r.user_id,r.state,r.reason,COALESCE(a.id,0),COALESCE(i.instance_id,'') FROM connection_requests r LEFT JOIN environment_assignments a ON a.request_id=r.id LEFT JOIN instances i ON i.id=a.instance_id WHERE r.id=?`, id).Scan(&x.ID, &x.EnvironmentID, &x.UserID, &x.State, &x.Reason, &x.AssignmentID, &x.InstanceID)
	return x, err
}
func (s *Store) RequestEnvironment(ctx context.Context, u User, eid int64, key string, now time.Time) (ConnectionRequest, error) {
	if len(key) < 8 || len(key) > 128 || strings.ContainsAny(key, "\r\n") {
		return ConnectionRequest{}, fmt.Errorf("idempotency key required (8..128 characters)")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ConnectionRequest{}, err
	}
	defer tx.Rollback()
	if !environmentAllowed(ctx, tx, u.ID, eid, now) {
		return ConnectionRequest{}, ErrEnvironmentDenied
	}
	var mode string
	if err = tx.QueryRowContext(ctx, `SELECT mode FROM environments WHERE id=?`, eid).Scan(&mode); err != nil {
		return ConnectionRequest{}, err
	}
	if mode != "shared" {
		return ConnectionRequest{}, fmt.Errorf("use existing Personal controls")
	}
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM connection_requests WHERE environment_id=? AND user_id=? AND (idempotency_key=? OR state NOT IN ('RELEASED','CANCELLED','FAILED','TIMED_OUT')) ORDER BY idempotency_key=? DESC LIMIT 1`, eid, u.ID, key, key).Scan(&id)
	if err == sql.ErrNoRows {
		var res sql.Result
		res, err = tx.ExecContext(ctx, `INSERT INTO connection_requests(environment_id,user_id,idempotency_key,created_at) VALUES(?,?,?,?)`, eid, u.ID, key, now.Unix())
		if err == nil {
			id, err = res.LastInsertId()
		}
	}
	if err != nil {
		return ConnectionRequest{}, err
	}
	x, err := requestRow(ctx, tx, id)
	if err != nil {
		return x, err
	}
	if x.State == "WAITING" {
		if _, err = tx.ExecContext(ctx, `UPDATE cloud_operations SET state='CANCELLED' WHERE environment_id=? AND kind='TERMINATE' AND state IN ('DRAINING','RESERVED')`, eid); err != nil {
			return x, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE environment_instances SET lifecycle='READY',idle_since=0 WHERE environment_id=? AND lifecycle='DRAINING' AND EXISTS(SELECT 1 FROM cloud_operations o WHERE o.instance_id=environment_instances.instance_id AND o.generation=environment_instances.generation AND o.state='CANCELLED') AND NOT EXISTS(SELECT 1 FROM cloud_operations live WHERE live.instance_id=environment_instances.instance_id AND live.generation=environment_instances.generation AND live.kind='TERMINATE' AND live.state NOT IN ('SUCCEEDED','CANCELLED'))`, eid); err != nil {
			return x, err
		}

		if err = assignWaiting(ctx, tx, x, now); err != nil {
			return x, err
		}
		x, err = requestRow(ctx, tx, id)
		if err != nil {
			return x, err
		}
	}
	return x, tx.Commit()
}
func assignWaiting(ctx context.Context, tx *sql.Tx, r ConnectionRequest, now time.Time) error {
	reason := "fixed instance unavailable"
	var migrating bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM home_migrations WHERE user_id=? AND state='LOCKED')`, r.UserID).Scan(&migrating); err != nil {
		return err
	}
	if migrating {
		_, err := tx.ExecContext(ctx, `UPDATE connection_requests SET reason='HOME migration locked' WHERE id=?`, r.ID)
		return err
	}
	var iid, gen, storageRevision int64
	var storage bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM user_storage WHERE user_id=?)`, r.UserID).Scan(&storage); err != nil {
		return err
	}
	if !storage {
		reason = "HOME storage not configured"
	} else {
		var held bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM environment_assignments WHERE user_id=? AND state!='RELEASED') OR EXISTS(SELECT 1 FROM personal_home_leases WHERE user_id=?) OR EXISTS(SELECT 1 FROM dcv_agents da,json_each(da.ready_users) j WHERE j.value=? AND da.last_seen>=? AND NOT EXISTS(SELECT 1 FROM environment_instances ei JOIN environments e ON e.id=ei.environment_id WHERE ei.instance_id=da.instance_id AND e.mode='shared'))`, r.UserID, r.UserID, r.UserID, now.Add(-90*time.Second).Unix()).Scan(&held); err != nil {
			return err
		}
		if held {
			reason = "HOME held by another environment"
		} else {
			rows, err := tx.QueryContext(ctx, `SELECT ei.instance_id,ei.generation,us.revision FROM environment_instances ei JOIN instances i ON i.id=ei.instance_id JOIN dcv_agents da ON da.instance_id=i.id JOIN instance_observations o ON o.instance_id=i.id JOIN user_storage us ON us.user_id=? WHERE ei.environment_id=? AND ei.lifecycle='READY' AND i.enabled=1 AND da.last_seen>=? AND da.error='' AND da.applied_revision=i.dcv_policy_revision AND da.browser_blocked=1 AND o.received_at>=? AND json_extract(o.payload,'$.generation')=ei.generation AND json_extract(o.payload,'$.agent_version')=2 AND (SELECT COUNT(*) FROM environment_assignments a WHERE a.instance_id=i.id AND a.state!='RELEASED')<2 ORDER BY (SELECT COUNT(*) FROM environment_assignments a WHERE a.instance_id=i.id AND a.state!='RELEASED') DESC,(SELECT AVG(cpu) FROM instance_samples WHERE instance_id=i.id AND boot_id=o.boot_id AND observed_at>=json_extract(o.payload,'$.observed_at')-300),(SELECT AVG(memory) FROM instance_samples WHERE instance_id=i.id AND boot_id=o.boot_id AND observed_at>=json_extract(o.payload,'$.observed_at')-300),i.instance_id`, r.UserID, r.EnvironmentID, now.Add(-90*time.Second).Unix(), now.Add(-90*time.Second).Unix())
			if err != nil {
				return err
			}
			type candidate struct{ id, generation, revision int64 }
			var candidates []candidate
			for rows.Next() {
				var c candidate
				if err = rows.Scan(&c.id, &c.generation, &c.revision); err != nil {
					rows.Close()
					return err
				}
				candidates = append(candidates, c)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if len(candidates) == 0 {
				err = sql.ErrNoRows
			}
			if err != nil && err != sql.ErrNoRows {
				return err
			}
			if err == sql.ErrNoRows {
				var full bool
				if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM environment_instances ei WHERE ei.environment_id=? AND (SELECT COUNT(*) FROM environment_assignments a WHERE a.instance_id=ei.instance_id AND a.state!='RELEASED')>=2)`, r.EnvironmentID).Scan(&full); err != nil {
					return err
				}
				if full {
					reason = "fixed instance full (2 seats occupied)"
				} else {
					reason = "fixed instance or Agent/policy/metrics unavailable"
				}
				iid = 0
			}
			for _, candidate := range candidates {
				iid, gen, storageRevision = candidate.id, candidate.generation, candidate.revision
				var avgCPU, avgMem float64
				var count int
				var first, last int64
				var boot string
				if err = tx.QueryRowContext(ctx, `SELECT boot_id FROM instance_observations WHERE instance_id=?`, iid).Scan(&boot); err != nil {
					return err
				}
				if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(AVG(cpu),100),COALESCE(AVG(memory),100),COALESCE(MIN(observed_at),0),COALESCE(MAX(observed_at),0) FROM instance_samples WHERE instance_id=? AND boot_id=? AND generation=? AND observed_at>=?`, iid, boot, gen, now.Add(-5*time.Minute).Unix()).Scan(&count, &avgCPU, &avgMem, &first, &last); err != nil {
					return err
				}
				// At least 5 one-minute samples spanning the five-minute window; no silent
				// substitution of missing samples with zero. An audited startup warmup
				// exception requires at least 60 seconds of contiguous samples.
				var gaps int
				if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT observed_at-LAG(observed_at) OVER(ORDER BY sequence) gap FROM instance_samples WHERE instance_id=? AND boot_id=? AND generation=? AND observed_at>=?) WHERE gap>90 OR gap<0`, iid, boot, gen, now.Add(-5*time.Minute).Unix()).Scan(&gaps); err != nil {
					return err
				}
				var bootStart int64
				if err = tx.QueryRowContext(ctx, `SELECT MIN(observed_at) FROM instance_samples WHERE instance_id=? AND boot_id=?`, iid, boot).Scan(&bootStart); err != nil {
					return err
				}
				warmup := count >= 2 && last-first >= 60 && now.Unix()-bootStart < 300 && gaps == 0 && last >= now.Add(-90*time.Second).Unix()
				if (count < 5 || first > now.Add(-4*time.Minute).Unix()) && !warmup || last < now.Add(-90*time.Second).Unix() || gaps > 0 {
					reason = "metrics missing or incomplete five-minute window"
				} else if avgCPU >= 70 || avgMem >= 70 {
					reason = "CPU or memory five-minute average is at least 70%"
				} else {
					if _, err = tx.ExecContext(ctx, `INSERT INTO environment_assignments(request_id,instance_id,user_id,generation,storage_revision,created_at) VALUES(?,?,?,?,?,?)`, r.ID, iid, r.UserID, gen, storageRevision, now.Unix()); err != nil {
						return err
					}
					if _, err = tx.ExecContext(ctx, `UPDATE connection_requests SET state='PREPARING_USER',reason='HOME and native DCV preparation' WHERE id=?`, r.ID); err != nil {
						return err
					}
					if _, err = tx.ExecContext(ctx, `UPDATE environment_instances SET idle_since=0 WHERE instance_id=?`, iid); err != nil {
						return err
					}
					_, err = tx.ExecContext(ctx, `INSERT INTO environment_events(environment_id,instance_id,user_id,kind,detail,at) VALUES(?,?,?,'reserved',?,?)`, r.EnvironmentID, iid, r.UserID, fmt.Sprintf("request=%d warmup=%v", r.ID, warmup), now.Unix())
					return err
				}
			}
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE connection_requests SET reason=? WHERE id=?`, reason, r.ID)
	return err
}
func (s *Store) EnvironmentRequest(ctx context.Context, u User, id int64) (ConnectionRequest, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ConnectionRequest{}, err
	}
	defer tx.Rollback()
	x, err := requestRow(ctx, tx, id)
	if err != nil {
		return x, err
	}
	if x.UserID != u.ID || !environmentAllowed(ctx, tx, u.ID, x.EnvironmentID, time.Now()) {
		return x, ErrEnvironmentDenied
	}
	return x, tx.Commit()
}
func (s *Store) EndEnvironmentRequest(ctx context.Context, u User, id int64, cancel bool, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	x, err := requestRow(ctx, tx, id)
	if err != nil {
		return err
	}
	if x.UserID != u.ID {
		return ErrEnvironmentDenied
	}
	if x.State == "RELEASED" || x.State == "CANCELLED" {
		return tx.Commit()
	}
	if x.AssignmentID == 0 {
		_, err = tx.ExecContext(ctx, `UPDATE connection_requests SET state='CANCELLED',reason='cancelled by user' WHERE id=?`, id)
	} else {
		// Releasing remains an occupied seat and HOME lease until the root agent has
		// confirmed session/job/process cleanup. This also prevents a token race.
		if cancel && x.State != "PREPARING_USER" {
			return fmt.Errorf("use explicit release after preparation")
		}
		var raw string
		var received int64
		if err = tx.QueryRowContext(ctx, `SELECT payload,received_at FROM instance_observations WHERE instance_id=(SELECT instance_id FROM environment_assignments WHERE id=?)`, x.AssignmentID).Scan(&raw, &received); err != nil {
			return err
		}
		var report EnvironmentReport
		if err = json.Unmarshal([]byte(raw), &report); err != nil {
			return err
		}
		if received < now.Add(-90*time.Second).Unix() {
			return fmt.Errorf("agent unavailable; HOME lease retained")
		}
		for _, work := range report.Work {
			if work.UserID == u.ID && (work.Jobs > 0 || work.Unclassified) {
				return fmt.Errorf("job or unclassified work holds this seat")
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE environment_assignments SET state='RELEASING' WHERE id=?`, x.AssignmentID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE connection_requests SET state='RELEASING',reason='waiting for agent session cleanup' WHERE id=?`, id)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Reconciliation is a worker operation. GET/poll handlers are strictly read-only.
func (s *Store) ReconcileEnvironments(ctx context.Context, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id,environment_id,user_id FROM connection_requests WHERE state='WAITING' ORDER BY id`)
	if err != nil {
		return err
	}
	var pending []ConnectionRequest
	for rows.Next() {
		var x ConnectionRequest
		if err = rows.Scan(&x.ID, &x.EnvironmentID, &x.UserID); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, x := range pending {
		if !environmentAllowed(ctx, tx, x.UserID, x.EnvironmentID, now) {
			if _, err = tx.ExecContext(ctx, `UPDATE connection_requests SET state='FAILED',reason='access revoked' WHERE id=?`, x.ID); err != nil {
				return err
			}
			continue
		}
		if err = assignWaiting(ctx, tx, x, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) IsSharedInstance(ctx context.Context, awsID string) bool {
	var shared bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM instances i JOIN environment_instances ei ON ei.instance_id=i.id JOIN environments e ON e.id=ei.environment_id WHERE i.instance_id=? AND e.mode='shared')`, awsID).Scan(&shared)
	return err != nil || shared
}

func (s *Store) IsEnvironmentInstance(ctx context.Context, aws string) bool {
	var yes bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM environment_instances ei JOIN instances i ON i.id=ei.instance_id WHERE i.instance_id=?)`, aws).Scan(&yes)
	return err != nil || yes
}
