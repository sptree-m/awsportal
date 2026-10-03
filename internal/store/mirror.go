package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type MirrorRepo struct {
	ID, IntervalMinutes, LastSuccess, Revision int64
	Name, Upstream, CredentialRef, Branch      string
	Enabled                                    bool
}
type MirrorJob struct {
	ID, RepoID, RequestedBy, Created, Finished int64
	State, Message                             string
}
type MirrorToken struct {
	ID, UserID, Expires int64
	Username, Label     string
	CanSync             bool
}
type MirrorGrant struct {
	ID, RepoID, SubjectID int64
	Scope, Subject        string
	CanSync               bool
}

const mirrorSchema = `CREATE TABLE IF NOT EXISTS mirror_repos(id INTEGER PRIMARY KEY,name TEXT NOT NULL,upstream TEXT NOT NULL,credential_ref TEXT NOT NULL DEFAULT '',branch TEXT NOT NULL DEFAULT 'main',interval_minutes INTEGER NOT NULL DEFAULT 0,enabled INTEGER NOT NULL DEFAULT 1,last_success INTEGER NOT NULL DEFAULT 0,revision INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS mirror_grants(id INTEGER PRIMARY KEY,repo_id INTEGER NOT NULL REFERENCES mirror_repos(id),scope TEXT NOT NULL,subject_id INTEGER NOT NULL,can_sync INTEGER NOT NULL DEFAULT 0,UNIQUE(repo_id,scope,subject_id));
CREATE TABLE IF NOT EXISTS mirror_tokens(id INTEGER PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users(id),label TEXT NOT NULL,token_hash TEXT UNIQUE NOT NULL,can_sync INTEGER NOT NULL DEFAULT 0,expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS mirror_jobs(id INTEGER PRIMARY KEY,repo_id INTEGER NOT NULL REFERENCES mirror_repos(id),requested_by INTEGER NOT NULL DEFAULT 0,state TEXT NOT NULL DEFAULT 'queued',created INTEGER NOT NULL,finished INTEGER NOT NULL DEFAULT 0,message TEXT NOT NULL DEFAULT '');
CREATE UNIQUE INDEX IF NOT EXISTS mirror_one_active ON mirror_jobs(repo_id) WHERE state IN ('queued','running');`

var credentialName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
var branchName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]{0,100}$`)

func ValidateMirror(r MirrorRepo) error {
	u, e := url.Parse(r.Upstream)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path == "" || !strings.HasSuffix(u.Path, ".git") {
		return fmt.Errorf("HTTPS repository URL ending .git required")
	}
	if r.CredentialRef != "" && !credentialName.MatchString(r.CredentialRef) {
		return fmt.Errorf("credential name")
	}
	if !branchName.MatchString(r.Branch) || strings.Contains(r.Branch, "..") || strings.Contains(r.Branch, "//") || strings.HasSuffix(r.Branch, "/") || strings.HasSuffix(r.Branch, ".lock") {
		return fmt.Errorf("branch")
	}
	if len(strings.TrimSpace(r.Name)) < 1 || len(r.Name) > 80 || r.IntervalMinutes < 0 || r.IntervalMinutes > 10080 {
		return fmt.Errorf("name / interval")
	}
	return nil
}
func (s *Store) SaveMirror(ctx context.Context, r MirrorRepo) error {
	if e := ValidateMirror(r); e != nil {
		return e
	}
	if r.ID == 0 {
		_, e := s.DB.ExecContext(ctx, `INSERT INTO mirror_repos(name,upstream,credential_ref,branch,interval_minutes,enabled) VALUES(?,?,?,?,?,?)`, r.Name, r.Upstream, r.CredentialRef, r.Branch, r.IntervalMinutes, r.Enabled)
		return e
	}
	var original string
	if e := s.DB.QueryRowContext(ctx, "SELECT upstream FROM mirror_repos WHERE id=?", r.ID).Scan(&original); e != nil {
		return e
	}
	if original != r.Upstream {
		return fmt.Errorf("upstream URL is immutable; create another mirror")
	}
	_, e := s.DB.ExecContext(ctx, `UPDATE mirror_repos SET name=?,credential_ref=?,branch=?,interval_minutes=?,enabled=?,revision=revision+1 WHERE id=?`, r.Name, r.CredentialRef, r.Branch, r.IntervalMinutes, r.Enabled, r.ID)
	return e
}
func scanMirror(row interface{ Scan(...any) error }) (MirrorRepo, error) {
	var r MirrorRepo
	e := row.Scan(&r.ID, &r.Name, &r.Upstream, &r.CredentialRef, &r.Branch, &r.IntervalMinutes, &r.Enabled, &r.LastSuccess, &r.Revision)
	return r, e
}

const mirrorColumns = "id,name,upstream,credential_ref,branch,interval_minutes,enabled,last_success,revision"

func (s *Store) Mirror(ctx context.Context, id int64) (MirrorRepo, error) {
	return scanMirror(s.DB.QueryRowContext(ctx, "SELECT "+mirrorColumns+" FROM mirror_repos WHERE id=?", id))
}
func (s *Store) Mirrors(ctx context.Context) ([]MirrorRepo, error) {
	rows, e := s.DB.QueryContext(ctx, "SELECT "+mirrorColumns+" FROM mirror_repos ORDER BY id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []MirrorRepo{}
	for rows.Next() {
		r, e := scanMirror(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) MirrorAccess(ctx context.Context, u User, id int64, sync bool) bool {
	if !u.Enabled || u.MustChangePassword || s.AccountExpired(u, time.Now()) {
		return false
	}
	var allowed bool
	e := s.DB.QueryRowContext(ctx, `SELECT enabled=1 AND (?='portal_admin' OR EXISTS(SELECT 1 FROM mirror_grants g WHERE g.repo_id=r.id AND (?=0 OR g.can_sync=1) AND ((g.scope='user' AND g.subject_id=?) OR (g.scope='group' AND g.subject_id IN(SELECT group_id FROM group_members WHERE user_id=?))))) FROM mirror_repos r WHERE id=?`, u.Role, sync, u.ID, u.ID, id).Scan(&allowed)
	return e == nil && allowed
}
func (s *Store) SetMirrorGrant(ctx context.Context, repo int64, scope string, subject int64, canSync, remove bool) error {
	if scope != "user" && scope != "group" {
		return fmt.Errorf("scope")
	}
	table := "users"
	if scope == "group" {
		table = "groups"
	}
	var n int
	if e := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE id=?", subject).Scan(&n); e != nil || n != 1 {
		return fmt.Errorf("subject")
	}
	if remove {
		_, e := s.DB.ExecContext(ctx, "DELETE FROM mirror_grants WHERE repo_id=? AND scope=? AND subject_id=?", repo, scope, subject)
		return e
	}
	_, e := s.DB.ExecContext(ctx, `INSERT INTO mirror_grants(repo_id,scope,subject_id,can_sync) VALUES(?,?,?,?) ON CONFLICT(repo_id,scope,subject_id) DO UPDATE SET can_sync=excluded.can_sync`, repo, scope, subject, canSync)
	return e
}
func (s *Store) MirrorGrants(ctx context.Context) ([]MirrorGrant, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT g.id,g.repo_id,g.scope,g.subject_id,CASE g.scope WHEN 'user' THEN u.username ELSE gr.name END,g.can_sync FROM mirror_grants g LEFT JOIN users u ON g.scope='user' AND u.id=g.subject_id LEFT JOIN groups gr ON g.scope='group' AND gr.id=g.subject_id ORDER BY g.id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []MirrorGrant{}
	for rows.Next() {
		var g MirrorGrant
		if e = rows.Scan(&g.ID, &g.RepoID, &g.Scope, &g.SubjectID, &g.Subject, &g.CanSync); e != nil {
			return nil, e
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
func (s *Store) IssueMirrorToken(ctx context.Context, uid int64, label string, days int, canSync bool) (string, error) {
	if len(strings.TrimSpace(label)) < 1 || len(label) > 80 || days < 1 || days > 90 {
		return "", fmt.Errorf("label / expiry")
	}
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	token := hex.EncodeToString(b)
	_, e := s.DB.ExecContext(ctx, "INSERT INTO mirror_tokens(user_id,label,token_hash,can_sync,expires) VALUES(?,?,?,?,?)", uid, label, tokenHash(token), canSync, time.Now().Add(time.Duration(days)*24*time.Hour).Unix())
	return token, e
}
func (s *Store) MirrorTokenUser(ctx context.Context, token string) (User, bool, error) {
	var name string
	var canSync bool
	e := s.DB.QueryRowContext(ctx, `SELECT u.username,t.can_sync FROM mirror_tokens t JOIN users u ON u.id=t.user_id WHERE t.token_hash=? AND t.expires>?`, tokenHash(token), time.Now().Unix()).Scan(&name, &canSync)
	if e != nil {
		return User{}, false, e
	}
	u, e := s.UserByName(ctx, name)
	if e != nil || !u.Enabled || u.MustChangePassword || s.AccountExpired(u, time.Now()) {
		return User{}, false, fmt.Errorf("account disabled")
	}
	return u, canSync, nil
}
func (s *Store) MirrorTokens(ctx context.Context) ([]MirrorToken, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT t.id,t.user_id,u.username,t.label,t.can_sync,t.expires FROM mirror_tokens t JOIN users u ON u.id=t.user_id ORDER BY t.id DESC`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []MirrorToken{}
	for rows.Next() {
		var t MirrorToken
		if e = rows.Scan(&t.ID, &t.UserID, &t.Username, &t.Label, &t.CanSync, &t.Expires); e != nil {
			return nil, e
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (s *Store) RevokeMirrorToken(ctx context.Context, id int64) error {
	_, e := s.DB.ExecContext(ctx, "DELETE FROM mirror_tokens WHERE id=?", id)
	return e
}
func (s *Store) EnqueueMirror(ctx context.Context, repo, actor int64) (MirrorJob, error) {
	tx, beginErr := s.DB.BeginTx(ctx, nil)
	if beginErr != nil {
		return MirrorJob{}, beginErr
	}
	defer tx.Rollback()
	existing, err := scanJob(tx.QueryRowContext(ctx, "SELECT id,repo_id,requested_by,state,created,finished,message FROM mirror_jobs WHERE repo_id=? AND state IN ('queued','running')", repo))
	if err == nil {
		return existing, tx.Commit()
	}
	if err != sql.ErrNoRows {
		return MirrorJob{}, err
	}
	var last int64
	if e := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(created),0) FROM mirror_jobs WHERE repo_id=?", repo).Scan(&last); e != nil {
		return MirrorJob{}, e
	}
	if time.Now().Unix()-last < 30 {
		return MirrorJob{}, ErrMirrorCooldown
	}
	_, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO mirror_jobs(repo_id,requested_by,state,created) VALUES(?,?,'queued',?)", repo, actor, time.Now().Unix())
	if e != nil {
		return MirrorJob{}, e
	}
	j, e := scanJob(tx.QueryRowContext(ctx, "SELECT id,repo_id,requested_by,state,created,finished,message FROM mirror_jobs WHERE repo_id=? AND state IN ('queued','running')", repo))
	if e != nil {
		return j, e
	}
	return j, tx.Commit()
}
func scanJob(row interface{ Scan(...any) error }) (MirrorJob, error) {
	var j MirrorJob
	e := row.Scan(&j.ID, &j.RepoID, &j.RequestedBy, &j.State, &j.Created, &j.Finished, &j.Message)
	return j, e
}
func (s *Store) MirrorJob(ctx context.Context, id int64) (MirrorJob, error) {
	return scanJob(s.DB.QueryRowContext(ctx, "SELECT id,repo_id,requested_by,state,created,finished,message FROM mirror_jobs WHERE id=?", id))
}
func (s *Store) MirrorJobs(ctx context.Context) ([]MirrorJob, error) {
	rows, e := s.DB.QueryContext(ctx, "SELECT id,repo_id,requested_by,state,created,finished,message FROM mirror_jobs ORDER BY id DESC LIMIT 30")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []MirrorJob{}
	for rows.Next() {
		j, e := scanJob(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
func (s *Store) ClaimMirrorJob(ctx context.Context) (MirrorJob, error) {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return MirrorJob{}, e
	}
	defer tx.Rollback()
	j, e := scanJob(tx.QueryRowContext(ctx, "SELECT id,repo_id,requested_by,state,created,finished,message FROM mirror_jobs WHERE state='queued' ORDER BY id LIMIT 1"))
	if e != nil {
		return j, e
	}
	if _, e = tx.ExecContext(ctx, "UPDATE mirror_jobs SET state='running' WHERE id=?", j.ID); e != nil {
		return j, e
	}
	j.State = "running"
	return j, tx.Commit()
}
func (s *Store) FinishMirrorJob(ctx context.Context, j MirrorJob, revision int64, err error) error {
	state, msg := "success", ""
	if err != nil {
		state = "error"
		msg = "同期に失敗しました。上流接続・認証・ブランチ・空き容量を確認してください"
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	_, e = tx.ExecContext(ctx, "UPDATE mirror_jobs SET state=?,message=?,finished=? WHERE id=?", state, msg, time.Now().Unix(), j.ID)
	if e != nil {
		return e
	}
	if err == nil {
		_, e = tx.ExecContext(ctx, "UPDATE mirror_repos SET last_success=? WHERE id=? AND revision=?", time.Now().Unix(), j.RepoID, revision)
		if e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (s *Store) RecoverMirrorJobs(ctx context.Context) error {
	_, e := s.DB.ExecContext(ctx, "UPDATE mirror_jobs SET state='error',finished=?,message='サーバー再起動により中断。再実行してください' WHERE state='running'", time.Now().Unix())
	return e
}

var ErrMirrorCooldown = fmt.Errorf("sync cooldown")

// Authorized mirror API use is account activity for unattended tasks; disabled/expired accounts are never revived.
func (s *Store) RecordMirrorAccess(ctx context.Context, u User) error {
	now := time.Now()
	if s.AccountExpired(u, now) || !u.Enabled {
		return fmt.Errorf("inactive account")
	}
	_, e := s.DB.ExecContext(ctx, "UPDATE users SET expires_at=? WHERE id=? AND enabled=1 AND role!='portal_admin' AND expires_at>? AND expires_at<?", now.Add(30*24*time.Hour).Unix(), u.ID, now.Unix(), now.Add(29*24*time.Hour).Unix())
	return e
}
