package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"
)

const dcvAgentSchema = `CREATE TABLE IF NOT EXISTS dcv_agents(instance_id INTEGER PRIMARY KEY REFERENCES instances(id),token_hash TEXT NOT NULL UNIQUE,last_seen INTEGER NOT NULL DEFAULT 0,ready_users TEXT NOT NULL DEFAULT '[]',error TEXT NOT NULL DEFAULT '');`

type DCVAccount struct {
	ReservationExpired bool         `json:"reservation_expired,omitempty"`
	UserID             int64        `json:"user_id"`
	Username           string       `json:"username"`
	OSUser             string       `json:"os_user"`
	SessionID          string       `json:"session_id"`
	Home               *HomeStorage `json:"home,omitempty"`
	AssignmentID       int64        `json:"assignment_id,omitempty"`
	StorageRevision    int64        `json:"storage_revision,omitempty"`
}

func DCVIdentity(id int64) string { return fmt.Sprintf("awp-u%d", id) }
func DCVTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ConfigureDCV enables per-user sessions and rotates the instance's machine credential.
// OS usernames use database IDs, never administrator-supplied shell fragments.
func (s *Store) ConfigureDCV(ctx context.Context, admin User, awsID, host, mode, token string) error {
	if e := adminOnly(admin); e != nil {
		return e
	}
	if mode != "native" {
		return fmt.Errorf("invalid connection mode")
	}
	if host == "" || len(host) > 253 || strings.ContainsAny(host, "/:?#@\\ \t\r\n") {
		return fmt.Errorf("host must be a hostname or IPv4 address")
	}
	if net.ParseIP(host) == nil {
		for _, label := range strings.Split(host, ".") {
			if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return fmt.Errorf("invalid hostname")
			}
			for _, c := range label {
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
					return fmt.Errorf("invalid hostname")
				}
			}
		}
	}
	if len(token) != 64 {
		return fmt.Errorf("machine token must contain 32 random bytes encoded as hex")
	}
	if _, e := hex.DecodeString(token); e != nil {
		return e
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	res, e := tx.ExecContext(ctx, "UPDATE instances SET dcv_host=?,dcv_connect_mode=? WHERE instance_id=?", host, mode, awsID)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fmt.Errorf("instance not found")
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO dcv_agents(instance_id,token_hash) SELECT id,? FROM instances WHERE instance_id=? ON CONFLICT(instance_id) DO UPDATE SET token_hash=excluded.token_hash,last_seen=0,ready_users='[]',error='',browser_blocked=0`, DCVTokenHash(token), awsID)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, "DELETE FROM dcv_tokens WHERE instance_id=(SELECT id FROM instances WHERE instance_id=?)", awsID)
	if e != nil {
		return e
	}
	return tx.Commit()
}

func (s *Store) DCVAgentInstance(ctx context.Context, token string) (string, error) {
	if len(token) != 64 {
		return "", fmt.Errorf("invalid credential")
	}
	var id string
	e := s.DB.QueryRowContext(ctx, "SELECT i.instance_id FROM dcv_agents a JOIN instances i ON i.id=a.instance_id WHERE a.token_hash=?", DCVTokenHash(token)).Scan(&id)
	return id, e
}

// The manifest includes administrators, individual grants and expanded group grants.
// Disabled instances deliberately return an empty manifest so the agent revokes sessions.
func (s *Store) DCVAccounts(ctx context.Context, awsID string, now time.Time) ([]DCVAccount, error) {
	if s.IsSharedInstance(ctx, awsID) {
		return s.SharedDCVAccounts(ctx, awsID, now)
	}
	rs, e := s.DB.QueryContext(ctx, `SELECT u.id,u.username,COALESCE(us.efs_id,''),COALESCE(us.access_point_id,''),COALESCE(us.revision,0) FROM users u JOIN instances i ON i.instance_id=? LEFT JOIN environment_instances pei ON pei.instance_id=i.id LEFT JOIN environments pe ON pe.id=pei.environment_id AND pe.mode='personal' LEFT JOIN user_storage us ON us.user_id=pe.owner_user_id AND us.user_id=u.id WHERE i.enabled=1 AND u.enabled=1 AND u.must_change_password=0 AND (u.role='portal_admin' OR u.expires_at=0 OR u.expires_at>=?) AND (u.role='portal_admin' OR EXISTS(SELECT 1 FROM instance_users g WHERE g.instance_id=i.id AND g.user_id=u.id) OR EXISTS(SELECT 1 FROM instance_groups g JOIN group_members m ON m.group_id=g.group_id WHERE g.instance_id=i.id AND m.user_id=u.id)) AND NOT EXISTS(SELECT 1 FROM environment_assignments ea WHERE ea.user_id=u.id AND ea.state!='RELEASED') AND `+personalDCVAccess+` ORDER BY u.id`, awsID, now.Unix())
	if e != nil {
		return nil, e
	}
	defer rs.Close()
	out := []DCVAccount{}
	for rs.Next() {
		var a DCVAccount
		h := HomeStorage{}
		if e = rs.Scan(&a.UserID, &a.Username, &h.EFSID, &h.AccessPointID, &h.Revision); e != nil {
			return nil, e
		}
		if h.EFSID != "" {
			h.UID = 200000 + a.UserID
			h.GID = h.UID
			a.Home = &h
			a.StorageRevision = h.Revision
		}
		a.OSUser = DCVIdentity(a.UserID)
		a.SessionID = a.OSUser
		out = append(out, a)
	}
	if e = rs.Err(); e != nil {
		return nil, e
	}
	rs.Close()
	allowed := out[:0]
	for _, a := range out {
		if a.Home != nil {
			ok, err := s.ClaimPersonalHome(ctx, awsID, a.UserID)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
		}
		allowed = append(allowed, a)
	}
	return allowed, nil
}
func (s *Store) DCVHeartbeat(ctx context.Context, awsID string, ready []int64, detail string, now time.Time, appliedRevision ...int64) error {
	revision := int64(0)
	browserBlocked := false
	if len(appliedRevision) >= 1 {
		revision = appliedRevision[0]
	}
	if len(appliedRevision) == 2 && appliedRevision[1] == 1 {
		browserBlocked = true
	}
	if len(appliedRevision) > 2 || revision < 0 {
		return fmt.Errorf("invalid policy revision")
	}
	if len(ready) > 10000 || len(detail) > 512 {
		return fmt.Errorf("heartbeat too large")
	}
	for _, id := range ready {
		if id < 1 {
			return fmt.Errorf("invalid user ID")
		}
	}
	if ready == nil {
		ready = []int64{}
	}
	raw, e := json.Marshal(ready)
	if e != nil {
		return e
	}
	_, e = s.DB.ExecContext(ctx, "UPDATE dcv_agents SET last_seen=?,ready_users=?,error=?,applied_revision=?,browser_blocked=? WHERE instance_id=(SELECT id FROM instances WHERE instance_id=?)", now.Unix(), string(raw), detail, revision, browserBlocked, awsID)
	return e
}
func (s *Store) DCVConnection(ctx context.Context, awsID string, uid int64, now time.Time) (managed, ready bool, mode string, err error) {
	if s.IsSharedInstance(ctx, awsID) {
		return s.SharedDCVConnection(ctx, awsID, uid, now)
	}
	var denied bool
	err = s.DB.QueryRowContext(ctx, `SELECT NOT (`+personalDCVAccess+`) FROM instances i JOIN users u ON u.id=? WHERE i.instance_id=?`, uid, awsID).Scan(&denied)
	if err != nil || denied {
		managed = true
		return
	}
	var seen int64
	var raw string
	err = s.DB.QueryRowContext(ctx, `SELECT 'native',CASE WHEN a.applied_revision=i.dcv_policy_revision AND a.browser_blocked=1 THEN COALESCE(a.last_seen,0) ELSE 0 END,COALESCE(a.ready_users,''),a.instance_id IS NOT NULL FROM instances i LEFT JOIN dcv_agents a ON a.instance_id=i.id WHERE i.instance_id=?`, awsID).Scan(&mode, &seen, &raw, &managed)
	if err != nil || !managed {
		return
	}
	if seen < now.Add(-90*time.Second).Unix() {
		return
	}
	var ids []int64
	if err = json.Unmarshal([]byte(raw), &ids); err != nil {
		return
	}
	for _, id := range ids {
		if id == uid {
			ready = true
		}
	}
	return
}
