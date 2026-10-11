package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const storageSchema = `
CREATE TABLE IF NOT EXISTS personal_home_leases(user_id INTEGER PRIMARY KEY REFERENCES users(id),instance_id INTEGER NOT NULL REFERENCES instances(id),created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS group_storage(group_id INTEGER PRIMARY KEY REFERENCES groups(id),efs_id TEXT NOT NULL,access_point_id TEXT NOT NULL,revision INTEGER NOT NULL DEFAULT 1,scratch_gib INTEGER NOT NULL DEFAULT 50);
CREATE TABLE IF NOT EXISTS dataset_manifests(id TEXT PRIMARY KEY,environment_id INTEGER NOT NULL REFERENCES environments(id),bucket TEXT NOT NULL,prefix TEXT NOT NULL,manifest TEXT NOT NULL,created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS dataset_cache_jobs(id TEXT PRIMARY KEY,dataset_id TEXT NOT NULL REFERENCES dataset_manifests(id),instance_id INTEGER NOT NULL REFERENCES instances(id),state TEXT NOT NULL DEFAULT 'PENDING',error TEXT NOT NULL DEFAULT '',UNIQUE(dataset_id,instance_id));
CREATE TABLE IF NOT EXISTS home_migrations(user_id INTEGER PRIMARY KEY REFERENCES users(id),state TEXT NOT NULL CHECK(state IN ('LOCKED','VERIFIED')),source TEXT NOT NULL,proof TEXT NOT NULL DEFAULT '',created_at INTEGER NOT NULL);
`

type GroupMount struct {
	GroupID       int64   `json:"group_id"`
	GID           int64   `json:"gid"`
	EFSID         string  `json:"efs_id"`
	AccessPointID string  `json:"access_point_id"`
	Revision      int64   `json:"revision"`
	ScratchGiB    int64   `json:"scratch_gib"`
	Members       []int64 `json:"members"`
}
type CacheJob struct {
	ID        string          `json:"id"`
	DatasetID string          `json:"dataset_id"`
	Bucket    string          `json:"bucket"`
	Prefix    string          `json:"prefix"`
	Manifest  json.RawMessage `json:"manifest"`
	State     string          `json:"state,omitempty"`
	Error     string          `json:"error,omitempty"`
}

func (s *Store) SetGroupStorage(ctx context.Context, u User, gid int64, efs, ap string, scratch int64, reason string) error {
	if err := adminOnly(u); err != nil {
		return err
	}
	if gid < 1 || gid > 1000000 || !awsStorageID.MatchString(efs) || !strings.HasPrefix(efs, "fs-") || !awsStorageID.MatchString(ap) || !strings.HasPrefix(ap, "fsap-") || scratch < 1 || scratch > 4096 || reason == "" {
		return fmt.Errorf("approved group EFS, scratch quota and reason required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var held bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM environment_assignments a JOIN environment_instances ei ON ei.instance_id=a.instance_id JOIN environments e ON e.id=ei.environment_id WHERE e.group_id=? AND a.state!='RELEASED')`, gid).Scan(&held); err != nil {
		return err
	}
	if held {
		return fmt.Errorf("group has active HOME leases")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE resource_registry SET valid_to=? WHERE group_id=? AND resource_id=(SELECT efs_id FROM group_storage WHERE group_id=?) AND resource_id!=? AND valid_to IS NULL`, time.Now().Unix(), gid, gid, efs); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO group_storage(group_id,efs_id,access_point_id,scratch_gib) VALUES(?,?,?,?) ON CONFLICT(group_id) DO UPDATE SET efs_id=excluded.efs_id,access_point_id=excluded.access_point_id,scratch_gib=excluded.scratch_gib,revision=revision+1`, gid, efs, ap, scratch); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO resource_registry(resource_id,group_id,valid_from) VALUES(?,?,?) ON CONFLICT DO NOTHING`, efs, gid, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) AgentStorage(ctx context.Context, aws string) ([]GroupMount, []CacheJob, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT gs.group_id,gs.efs_id,gs.access_point_id,gs.revision,gs.scratch_gib FROM group_storage gs JOIN environments e ON e.group_id=gs.group_id JOIN environment_instances ei ON ei.environment_id=e.id JOIN instances i ON i.id=ei.instance_id WHERE i.instance_id=?`, aws)
	if err != nil {
		return nil, nil, err
	}
	var groups []GroupMount
	for rows.Next() {
		var g GroupMount
		if err = rows.Scan(&g.GroupID, &g.EFSID, &g.AccessPointID, &g.Revision, &g.ScratchGiB); err != nil {
			rows.Close()
			return nil, nil, err
		}
		g.GID = 2000000 + g.GroupID
		g.Members = []int64{}
		groups = append(groups, g)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	for n := range groups {
		rs, err := s.DB.QueryContext(ctx, `SELECT gm.user_id FROM group_members gm WHERE gm.group_id=? AND (EXISTS(SELECT 1 FROM environment_assignments a JOIN instances i ON i.id=a.instance_id WHERE a.user_id=gm.user_id AND i.instance_id=? AND a.state NOT IN ('RELEASED','RELEASING')) OR EXISTS(SELECT 1 FROM personal_home_leases p JOIN instances i ON i.id=p.instance_id WHERE p.user_id=gm.user_id AND i.instance_id=?)) ORDER BY gm.user_id`, groups[n].GroupID, aws, aws)
		if err != nil {
			return nil, nil, err
		}
		for rs.Next() {
			var uid int64
			if err = rs.Scan(&uid); err != nil {
				rs.Close()
				return nil, nil, err
			}
			groups[n].Members = append(groups[n].Members, uid)
		}
		err = rs.Err()
		rs.Close()
		if err != nil {
			return nil, nil, err
		}
	}
	rows, err = s.DB.QueryContext(ctx, `SELECT c.id,m.id,m.bucket,m.prefix,m.manifest,c.state,c.error FROM dataset_cache_jobs c JOIN dataset_manifests m ON m.id=c.dataset_id JOIN instances i ON i.id=c.instance_id WHERE i.instance_id=? AND c.state IN ('PENDING','RUNNING') ORDER BY c.id`, aws)
	if err != nil {
		return nil, nil, err
	}
	var jobs []CacheJob
	for rows.Next() {
		var j CacheJob
		var m string
		if err = rows.Scan(&j.ID, &j.DatasetID, &j.Bucket, &j.Prefix, &m, &j.State, &j.Error); err != nil {
			rows.Close()
			return nil, nil, err
		}
		j.Manifest = json.RawMessage(m)
		jobs = append(jobs, j)
	}
	err = rows.Err()
	rows.Close()
	return groups, jobs, err
}
func (s *Store) RequestDatasetCache(ctx context.Context, u User, eid int64, aws, id, bucket, prefix, manifest string) error {
	if err := adminOnly(u); err != nil {
		return err
	}
	var parsed any
	if err := json.Unmarshal([]byte(manifest), &parsed); err != nil {
		return err
	}
	var canonical bytes.Buffer
	encoder := json.NewEncoder(&canonical)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(parsed); err != nil {
		return err
	}
	manifest = strings.TrimSpace(canonical.String())
	digest := sha256.Sum256([]byte(manifest))
	want := hex.EncodeToString(digest[:])
	if id == "" {
		id = want
	}
	if id != want {
		return fmt.Errorf("dataset manifest digest mismatch")
	}
	if len(id) != 64 || bucket == "" || prefix == "" || !json.Valid([]byte(manifest)) || len(manifest) > 4*1024*1024 {
		return fmt.Errorf("approved immutable dataset manifest required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var iid int64
	if err = tx.QueryRowContext(ctx, `SELECT i.id FROM instances i JOIN environment_instances ei ON ei.instance_id=i.id WHERE i.instance_id=? AND ei.environment_id=? AND ei.lifecycle='READY'`, aws, eid).Scan(&iid); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO dataset_manifests VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING`, id, eid, bucket, prefix, manifest, time.Now().Unix()); err != nil {
		return err
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM dataset_cache_jobs WHERE instance_id=? AND state IN ('PENDING','RUNNING')`, iid).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return fmt.Errorf("one active cache preparation per instance")
	}
	key, err := operationID()
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO dataset_cache_jobs(id,dataset_id,instance_id) VALUES(?,?,?) ON CONFLICT DO NOTHING`, key, id, iid); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE environment_instances SET idle_since=0 WHERE instance_id=?`, iid); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) CacheResults(ctx context.Context, aws string, xs []CacheJob) error {
	for _, j := range xs {
		if j.State != "AVAILABLE" && j.State != "FAILED" && j.State != "RUNNING" {
			return fmt.Errorf("invalid cache state")
		}
		_, err := s.DB.ExecContext(ctx, `UPDATE dataset_cache_jobs SET state=?,error=? WHERE id=? AND instance_id=(SELECT id FROM instances WHERE instance_id=?)`, j.State, j.Error, j.ID, aws)
		if err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) BeginHomeMigration(ctx context.Context, u User, uid int64, source string) error {
	if err := adminOnly(u); err != nil {
		return err
	}
	if source == "" {
		return fmt.Errorf("source required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var busy bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM environment_assignments WHERE user_id=? AND state!='RELEASED') OR EXISTS(SELECT 1 FROM personal_home_leases WHERE user_id=?) OR EXISTS(SELECT 1 FROM dcv_agents,json_each(ready_users) j WHERE j.value=? AND last_seen>=?)`, uid, uid, uid, time.Now().Add(-90*time.Second).Unix()).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return fmt.Errorf("offline HOME required")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO home_migrations(user_id,state,source,created_at) VALUES(?,'LOCKED',?,?) ON CONFLICT(user_id) DO UPDATE SET state='LOCKED',source=excluded.source,proof=''`, uid, source, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) FinishHomeMigration(ctx context.Context, u User, uid int64, proof string) error {
	if err := adminOnly(u); err != nil {
		return err
	}
	var p struct {
		Verified bool   `json:"verified"`
		Retained bool   `json:"source_retained"`
		Source   string `json:"source"`
	}
	if json.Unmarshal([]byte(proof), &p) != nil || !p.Verified || !p.Retained {
		return fmt.Errorf("verified source-retained proof required")
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE home_migrations SET state='VERIFIED',proof=? WHERE user_id=? AND state='LOCKED' AND source=?`, proof, uid, p.Source)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fmt.Errorf("migration lease missing")
	}
	return nil
}

// Personal remains compatible with legacy power control, but a mounted EFS HOME
// acquires the same exclusive durable lease as Shared. Stale Agent data never
// frees it: confirmed EC2 stopped/terminated is required.
func (s *Store) ClaimPersonalHome(ctx context.Context, aws string, uid int64) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var busy bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM environment_assignments WHERE user_id=? AND state!='RELEASED') OR EXISTS(SELECT 1 FROM personal_home_leases WHERE user_id=? AND instance_id!=(SELECT id FROM instances WHERE instance_id=?)) OR EXISTS(SELECT 1 FROM home_migrations WHERE user_id=? AND state='LOCKED')`, uid, uid, aws, uid).Scan(&busy); err != nil {
		return false, err
	}
	if busy {
		return false, nil
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO personal_home_leases(user_id,instance_id,created_at) SELECT ?,id,? FROM instances WHERE instance_id=?`, uid, time.Now().Unix(), aws); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
func (s *Store) PersonalLeaseInstances(ctx context.Context) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT i.instance_id FROM personal_home_leases l JOIN instances i ON i.id=l.instance_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var xs []string
	for rows.Next() {
		var x string
		if err = rows.Scan(&x); err != nil {
			return nil, err
		}
		xs = append(xs, x)
	}
	return xs, rows.Err()
}
func (s *Store) ConfirmPersonalStopped(ctx context.Context, aws, state string) error {
	if state != "stopped" && state != "terminated" {
		return fmt.Errorf("confirmed stopped/terminated state required")
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM personal_home_leases WHERE instance_id=(SELECT id FROM instances WHERE instance_id=?)`, aws)
	return err
}
