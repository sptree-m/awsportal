package store

import (
	"context"
	"database/sql"
	"fmt"
	_ "modernc.org/sqlite"
	"strings"
	"time"
)

type Store struct{ DB *sql.DB }
type User struct {
	ID                                                      int64
	Username, PasswordHash, Role, TOTPSecret                string
	Enabled, MustChangePassword                             bool
	TempPasswordExpires, LastLoginAt, ExpiresAt, DisabledAt int64
	DisabledReason                                          string
}
type Instance struct {
	ID                                      int64
	InstanceID, Name, DCVHost, DCVSessionID string
	CanControl                              bool
}
type Schedule struct {
	ID, InstanceDBID, OwnerUserID        int64
	Action, TimeHHMM, Weekdays, Timezone string
	Enabled                              bool
}
type MFADevice struct {
	ID           int64
	Name, Secret string
	Verified     bool
	CreatedAt    string
}

func Open(path string) (*Store, error) {
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	return &Store{DB: db}, nil
}
func (s *Store) Close() error { return s.DB.Close() }
func (s *Store) Migrate(ctx context.Context) error {
	if _, e := s.DB.ExecContext(ctx, schema+proxySchema+egressSchema+mirrorSchema+siteSchema+dcvAgentSchema+environmentSchema+jobMetricsSchema); e != nil {
		return e
	}
	for _, q := range []string{"ALTER TABLE dcv_agents ADD COLUMN browser_blocked INTEGER NOT NULL DEFAULT 0", "ALTER TABLE instances ADD COLUMN dcv_policy_json TEXT NOT NULL DEFAULT ''", "ALTER TABLE instances ADD COLUMN dcv_policy_revision INTEGER NOT NULL DEFAULT 1", "ALTER TABLE dcv_agents ADD COLUMN applied_revision INTEGER NOT NULL DEFAULT 0", "ALTER TABLE instances ADD COLUMN dcv_connect_mode TEXT NOT NULL DEFAULT 'native'", "ALTER TABLE proxy_rules ADD COLUMN kind TEXT NOT NULL DEFAULT 'domain'", "ALTER TABLE users ADD COLUMN last_login_at INTEGER NOT NULL DEFAULT 0", "ALTER TABLE users ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0", "ALTER TABLE users ADD COLUMN disabled_at INTEGER NOT NULL DEFAULT 0", "ALTER TABLE users ADD COLUMN disabled_reason TEXT NOT NULL DEFAULT ''"} {
		if _, e := s.DB.ExecContext(ctx, q); e != nil && !strings.Contains(e.Error(), "duplicate column name") {
			return e
		}
	}
	return nil
}

func (s *Store) UserByName(ctx context.Context, n string) (User, error) {
	var u User
	var en, must int
	err := s.DB.QueryRowContext(ctx, "SELECT id,username,password_hash,role,COALESCE(totp_secret,''),enabled,must_change_password,temp_password_expires,last_login_at,expires_at,disabled_at,disabled_reason FROM users WHERE username=?", n).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.TOTPSecret, &en, &must, &u.TempPasswordExpires, &u.LastLoginAt, &u.ExpiresAt, &u.DisabledAt, &u.DisabledReason)
	u.Enabled = en == 1
	u.MustChangePassword = must == 1
	return u, err
}
func (s *Store) CreateUser(ctx context.Context, n, h, role, totp string) error {
	_, e := s.DB.ExecContext(ctx, "INSERT INTO users(username,password_hash,role,totp_secret,enabled,expires_at) VALUES(?,?,?,?,1,?)", n, h, role, totp, time.Now().Add(30*24*time.Hour).Unix())
	return e
}

const accessSQL = `(?='portal_admin' OR EXISTS(SELECT 1 FROM instance_users iu WHERE iu.instance_id=i.id AND iu.user_id=?) OR EXISTS(SELECT 1 FROM instance_groups ig JOIN group_members gm ON gm.group_id=ig.group_id WHERE ig.instance_id=i.id AND gm.user_id=?))`

func (s *Store) VisibleInstances(ctx context.Context, u User) ([]Instance, error) {
	q := `SELECT i.id,i.instance_id,i.name,i.dcv_host,i.dcv_session_id, (?='portal_admin' OR EXISTS(SELECT 1 FROM instance_users iu WHERE iu.instance_id=i.id AND iu.user_id=? AND iu.can_control=1) OR EXISTS(SELECT 1 FROM instance_groups ig JOIN group_members gm ON gm.group_id=ig.group_id WHERE ig.instance_id=i.id AND gm.user_id=? AND ig.can_control=1)) FROM instances i WHERE i.enabled=1 AND ` + accessSQL + ` AND ` + nonSharedInstance + ` ORDER BY i.name`
	rows, e := s.DB.QueryContext(ctx, q, u.Role, u.ID, u.ID, u.Role, u.ID, u.ID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Instance
	for rows.Next() {
		var x Instance
		if e = rows.Scan(&x.ID, &x.InstanceID, &x.Name, &x.DCVHost, &x.DCVSessionID, &x.CanControl); e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *Store) CanControl(ctx context.Context, u User, awsID string) bool {
	var allowed bool
	e := s.DB.QueryRowContext(ctx, `SELECT (?='portal_admin' OR EXISTS(SELECT 1 FROM instance_users iu WHERE iu.instance_id=i.id AND iu.user_id=? AND iu.can_control=1) OR EXISTS(SELECT 1 FROM instance_groups ig JOIN group_members gm ON gm.group_id=ig.group_id WHERE ig.instance_id=i.id AND gm.user_id=? AND ig.can_control=1)) FROM instances i WHERE i.instance_id=? AND i.enabled=1 AND `+nonSharedInstance, u.Role, u.ID, u.ID, awsID).Scan(&allowed)
	return e == nil && allowed
}
func (s *Store) AddSchedule(ctx context.Context, u User, awsID, action, hhmm, weekdays, tz string) error {
	if s.IsSharedInstance(ctx, awsID) {
		return fmt.Errorf("Shared power schedules are forbidden")
	}
	var iid int64
	if e := s.DB.QueryRowContext(ctx, "SELECT id FROM instances WHERE instance_id=?", awsID).Scan(&iid); e != nil {
		return e
	}
	_, e := s.DB.ExecContext(ctx, "INSERT INTO schedules(instance_id,owner_user_id,action,time_hhmm,weekdays,timezone,enabled) VALUES(?,?,?,?,?,?,1)", iid, u.ID, action, hhmm, weekdays, tz)
	return e
}
func (s *Store) DueSchedules(ctx context.Context, t time.Time) ([]struct {
	Schedule
	InstanceID string
}, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT s.id,s.instance_id,s.owner_user_id,s.action,s.time_hhmm,s.weekdays,s.timezone,s.enabled,i.instance_id FROM schedules s JOIN instances i ON i.id=s.instance_id WHERE s.enabled=1 AND i.enabled=1 AND `+nonSharedInstance+` AND EXISTS(SELECT 1 FROM users u WHERE u.id=s.owner_user_id AND u.enabled=1 AND (u.role='portal_admin' OR u.expires_at=0 OR u.expires_at>=?) AND (u.role='portal_admin' OR EXISTS(SELECT 1 FROM instance_users iu WHERE iu.instance_id=i.id AND iu.user_id=u.id AND iu.can_control=1) OR EXISTS(SELECT 1 FROM instance_groups ig JOIN group_members gm ON gm.group_id=ig.group_id WHERE ig.instance_id=i.id AND gm.user_id=u.id AND ig.can_control=1)))`, t.Unix())
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []struct {
		Schedule
		InstanceID string
	}
	for rows.Next() {
		var x struct {
			Schedule
			InstanceID string
		}
		var en int
		if e = rows.Scan(&x.ID, &x.InstanceDBID, &x.OwnerUserID, &x.Action, &x.TimeHHMM, &x.Weekdays, &x.Timezone, &en, &x.InstanceID); e != nil {
			return nil, e
		}
		x.Enabled = en == 1
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *Store) Audit(ctx context.Context, actor, action, target, result, detail string) {
	_, _ = s.DB.ExecContext(ctx, "INSERT INTO audit_log(actor,action,target,result,detail) VALUES(?,?,?,?,?)", actor, action, target, result, detail)
}

const schema = `PRAGMA foreign_keys=ON; PRAGMA journal_mode=WAL;
CREATE TABLE IF NOT EXISTS users(id INTEGER PRIMARY KEY,username TEXT UNIQUE NOT NULL,password_hash TEXT NOT NULL,role TEXT NOT NULL CHECK(role IN ('user','group_admin','portal_admin')),totp_secret TEXT,enabled INTEGER NOT NULL DEFAULT 1,must_change_password INTEGER NOT NULL DEFAULT 0,temp_password_expires INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS groups(id INTEGER PRIMARY KEY,name TEXT UNIQUE NOT NULL);
CREATE TABLE IF NOT EXISTS group_members(user_id INTEGER NOT NULL REFERENCES users(id),group_id INTEGER NOT NULL REFERENCES groups(id),PRIMARY KEY(user_id,group_id));
CREATE TABLE IF NOT EXISTS instances(id INTEGER PRIMARY KEY,instance_id TEXT UNIQUE NOT NULL,name TEXT NOT NULL,dcv_host TEXT NOT NULL,dcv_session_id TEXT NOT NULL DEFAULT 'console',enabled INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS instance_users(instance_id INTEGER NOT NULL REFERENCES instances(id),user_id INTEGER NOT NULL REFERENCES users(id),can_control INTEGER NOT NULL DEFAULT 1,PRIMARY KEY(instance_id,user_id));
CREATE TABLE IF NOT EXISTS instance_groups(instance_id INTEGER NOT NULL REFERENCES instances(id),group_id INTEGER NOT NULL REFERENCES groups(id),can_control INTEGER NOT NULL DEFAULT 1,PRIMARY KEY(instance_id,group_id));
CREATE TABLE IF NOT EXISTS schedules(id INTEGER PRIMARY KEY,instance_id INTEGER NOT NULL REFERENCES instances(id),owner_user_id INTEGER NOT NULL REFERENCES users(id),action TEXT NOT NULL CHECK(action IN ('start','stop')),time_hhmm TEXT NOT NULL,weekdays TEXT NOT NULL DEFAULT '1,2,3,4,5',timezone TEXT NOT NULL DEFAULT 'Asia/Tokyo',enabled INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS dcv_tokens(id INTEGER PRIMARY KEY,token_hash TEXT UNIQUE NOT NULL,user_id INTEGER NOT NULL REFERENCES users(id),instance_id INTEGER NOT NULL REFERENCES instances(id),session_id TEXT NOT NULL,expires_at INTEGER NOT NULL,used_at INTEGER);
CREATE TABLE IF NOT EXISTS mfa_devices(id INTEGER PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,name TEXT NOT NULL,secret TEXT NOT NULL,verified INTEGER NOT NULL DEFAULT 0,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,UNIQUE(user_id,name));
CREATE TABLE IF NOT EXISTS audit_log(id INTEGER PRIMARY KEY,at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,actor TEXT NOT NULL,action TEXT NOT NULL,target TEXT,result TEXT NOT NULL,detail TEXT);`

func (s *Store) IssueDCVToken(ctx context.Context, u User, awsID, tokenHash string, expires time.Time) (Instance, error) {
	var x Instance
	err := s.DB.QueryRowContext(ctx, "SELECT id,instance_id,name,dcv_host,dcv_session_id FROM instances WHERE instance_id=? AND enabled=1", awsID).Scan(&x.ID, &x.InstanceID, &x.Name, &x.DCVHost, &x.DCVSessionID)
	if err != nil {
		return x, err
	}
	managed, ready, _, e := s.DCVConnection(ctx, awsID, u.ID, time.Now())
	if e != nil {
		return x, e
	}
	if !managed || !ready {
		return x, fmt.Errorf("native-only DCV enforcement is not ready")
	}
	x.DCVSessionID = DCVIdentity(u.ID)
	accounts, e := s.DCVAccounts(ctx, awsID, time.Now())
	if e != nil {
		return x, e
	}
	allowed := false
	for _, account := range accounts {
		if account.UserID == u.ID {
			allowed = true
		}
	}
	if !allowed {
		return x, fmt.Errorf("DCV access denied")
	}
	_, err = s.DB.ExecContext(ctx, "INSERT INTO dcv_tokens(token_hash,user_id,instance_id,session_id,expires_at) VALUES(?,?,?,?,?)", tokenHash, u.ID, x.ID, x.DCVSessionID, expires.Unix())
	return x, err
}
func (s *Store) ConsumeDCVToken(ctx context.Context, tokenHash, sessionID string, now time.Time) (string, bool) {
	// Legacy authentication has no instance-bound agent enforcement.
	return "", false
}
func (s *Store) ConsumeInstanceDCVToken(ctx context.Context, tokenHash, sessionID, awsID string, now time.Time) (string, bool) {
	if awsID == "" {
		return "", false
	}
	return s.consumeDCVToken(ctx, tokenHash, sessionID, awsID, now)
}
func (s *Store) consumeDCVToken(ctx context.Context, tokenHash, sessionID, awsID string, now time.Time) (string, bool) {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return "", false
	}
	defer tx.Rollback()
	var id int64
	var username string
	var uid int64
	var managed bool
	var expires int64
	var used sql.NullInt64
	e = tx.QueryRowContext(ctx, `SELECT t.id,u.username,t.expires_at,t.used_at,u.id,EXISTS(SELECT 1 FROM dcv_agents a WHERE a.instance_id=i.id) FROM dcv_tokens t JOIN users u ON u.id=t.user_id JOIN instances i ON i.id=t.instance_id WHERE t.token_hash=? AND t.session_id=? AND i.instance_id=? AND EXISTS(SELECT 1 FROM dcv_agents a,json_each(a.ready_users) j WHERE a.instance_id=i.id AND a.last_seen>=? AND a.applied_revision=i.dcv_policy_revision AND a.browser_blocked=1 AND j.value=u.id) AND u.enabled=1 AND i.enabled=1 AND u.must_change_password=0 AND (u.role='portal_admin' OR u.expires_at=0 OR u.expires_at>=?) AND ((`+nonSharedInstance+` AND (u.role='portal_admin' OR EXISTS(SELECT 1 FROM instance_users iu WHERE iu.instance_id=i.id AND iu.user_id=u.id) OR EXISTS(SELECT 1 FROM instance_groups ig JOIN group_members gm ON gm.group_id=ig.group_id WHERE ig.instance_id=i.id AND gm.user_id=u.id))) OR `+sharedDCVAccess+`)`, tokenHash, sessionID, awsID, now.Add(-90*time.Second).Unix(), now.Unix(), now.Unix()).Scan(&id, &username, &expires, &used, &uid, &managed)
	if e != nil || used.Valid || now.Unix() > expires {
		return "", false
	}
	if managed {
		username = DCVIdentity(uid)
		if sessionID != username {
			return "", false
		}
	}
	res, e := tx.ExecContext(ctx, "UPDATE dcv_tokens SET used_at=? WHERE id=? AND used_at IS NULL", now.Unix(), id)
	if e != nil {
		return "", false
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return "", false
	}
	if e = tx.Commit(); e != nil {
		return "", false
	}
	return username, true
}

func (s *Store) ListResettableUsers(ctx context.Context) ([]User, error) {
	rows, e := s.DB.QueryContext(ctx, "SELECT id,username,password_hash,role,COALESCE(totp_secret,''),enabled,must_change_password,temp_password_expires FROM users WHERE enabled=1 AND role!='portal_admin' ORDER BY username")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var en, must int
		if e = rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.TOTPSecret, &en, &must, &u.TempPasswordExpires); e != nil {
			return nil, e
		}
		u.Enabled = en == 1
		u.MustChangePassword = must == 1
		out = append(out, u)
	}
	return out, rows.Err()
}
func (s *Store) SetTemporaryPassword(ctx context.Context, username, hash string, expires time.Time) error {
	res, e := s.DB.ExecContext(ctx, "UPDATE users SET password_hash=?,must_change_password=1,temp_password_expires=? WHERE username=? AND enabled=1 AND role!='portal_admin'", hash, expires.Unix(), username)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}
func (s *Store) ChangePassword(ctx context.Context, userID int64, hash string) error {
	_, e := s.DB.ExecContext(ctx, "UPDATE users SET password_hash=?,must_change_password=0,temp_password_expires=0 WHERE id=?", hash, userID)
	return e
}
func (s *Store) BreakGlassResetAdmin(ctx context.Context, username, hash, totpSecret string) error {
	res, e := s.DB.ExecContext(ctx, "UPDATE users SET password_hash=?,totp_secret=?,must_change_password=1,temp_password_expires=0 WHERE username=? AND role='portal_admin'", hash, totpSecret, username)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) MFADevices(ctx context.Context, userID int64) ([]MFADevice, error) {
	rows, e := s.DB.QueryContext(ctx, "SELECT id,name,secret,verified,created_at FROM mfa_devices WHERE user_id=? ORDER BY created_at", userID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []MFADevice
	for rows.Next() {
		var d MFADevice
		var v int
		if e = rows.Scan(&d.ID, &d.Name, &d.Secret, &v, &d.CreatedAt); e != nil {
			return nil, e
		}
		d.Verified = v == 1
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *Store) AddMFADevice(ctx context.Context, userID int64, name, secret string) (int64, error) {
	var n int
	if e := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM mfa_devices WHERE user_id=?", userID).Scan(&n); e != nil {
		return 0, e
	}
	if n >= 3 {
		return 0, fmt.Errorf("MFA device limit reached")
	}
	res, e := s.DB.ExecContext(ctx, "INSERT INTO mfa_devices(user_id,name,secret,verified) VALUES(?,?,?,0)", userID, name, secret)
	if e != nil {
		return 0, e
	}
	return res.LastInsertId()
}
func (s *Store) MFADevice(ctx context.Context, userID, id int64) (MFADevice, error) {
	var d MFADevice
	var v int
	e := s.DB.QueryRowContext(ctx, "SELECT id,name,secret,verified,created_at FROM mfa_devices WHERE id=? AND user_id=?", id, userID).Scan(&d.ID, &d.Name, &d.Secret, &v, &d.CreatedAt)
	d.Verified = v == 1
	return d, e
}
func (s *Store) VerifyMFADevice(ctx context.Context, userID, id int64) error {
	res, e := s.DB.ExecContext(ctx, "UPDATE mfa_devices SET verified=1 WHERE id=? AND user_id=?", id, userID)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}
func (s *Store) DeleteMFADevice(ctx context.Context, userID, id int64) error {
	res, e := s.DB.ExecContext(ctx, "DELETE FROM mfa_devices WHERE id=? AND user_id=?", id, userID)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}
func (s *Store) VerifyMFASecrets(ctx context.Context, userID int64) ([]string, error) {
	rows, e := s.DB.QueryContext(ctx, "SELECT secret FROM mfa_devices WHERE user_id=? AND verified=1", userID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var x string
		if e = rows.Scan(&x); e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) AccountExpired(u User, now time.Time) bool {
	return u.Role != "portal_admin" && u.Enabled && u.ExpiresAt > 0 && now.Unix() > u.ExpiresAt
}
func (s *Store) RecordLogin(ctx context.Context, id int64, now time.Time) error {
	_, e := s.DB.ExecContext(ctx, "UPDATE users SET last_login_at=?,expires_at=?,disabled_at=0,disabled_reason='' WHERE id=?", now.Unix(), now.Add(30*24*time.Hour).Unix(), id)
	return e
}
func (s *Store) DisableInactive(ctx context.Context, id int64, now time.Time) error {
	_, e := s.DB.ExecContext(ctx, "UPDATE users SET enabled=0,disabled_at=?,disabled_reason='inactive_30_days' WHERE id=? AND role!='portal_admin'", now.Unix(), id)
	return e
}
func (s *Store) ReactivateUser(ctx context.Context, n string, now time.Time) error {
	res, e := s.DB.ExecContext(ctx, "UPDATE users SET enabled=1,disabled_at=0,disabled_reason='',expires_at=? WHERE username=? AND role!='portal_admin'", now.Add(30*24*time.Hour).Unix(), n)
	if e != nil {
		return e
	}
	x, _ := res.RowsAffected()
	if x != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, e := s.DB.QueryContext(ctx, "SELECT id,username,password_hash,role,COALESCE(totp_secret,''),enabled,must_change_password,temp_password_expires,last_login_at,expires_at,disabled_at,disabled_reason FROM users ORDER BY username")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var en, must int
		if e = rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.TOTPSecret, &en, &must, &u.TempPasswordExpires, &u.LastLoginAt, &u.ExpiresAt, &u.DisabledAt, &u.DisabledReason); e != nil {
			return nil, e
		}
		u.Enabled = en == 1
		u.MustChangePassword = must == 1
		out = append(out, u)
	}
	return out, rows.Err()
}

type AuditEntry struct {
	ID                                        int64
	At, Actor, Action, Target, Result, Detail string
}

func (s *Store) AuditEntries(ctx context.Context, limit int) ([]AuditEntry, error) {
	if limit < 1 || limit > 500 {
		limit = 200
	}
	rows, e := s.DB.QueryContext(ctx, "SELECT id,at,actor,action,target,result,detail FROM audit_log ORDER BY id DESC LIMIT ?", limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var x AuditEntry
		if e = rows.Scan(&x.ID, &x.At, &x.Actor, &x.Action, &x.Target, &x.Result, &x.Detail); e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *Store) SetUserEnabled(ctx context.Context, username string, enabled bool) error {
	v := 0
	if enabled {
		v = 1
	}
	res, e := s.DB.ExecContext(ctx, "UPDATE users SET enabled=? WHERE username=? AND role!='portal_admin'", v, username)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}
