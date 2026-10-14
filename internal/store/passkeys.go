package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/sptree-m/awsportal/internal/auth"
)

const passkeySchema = `
CREATE TABLE IF NOT EXISTS passkeys(id INTEGER PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,credential_id BLOB NOT NULL UNIQUE,name TEXT NOT NULL,credential TEXT NOT NULL,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,UNIQUE(user_id,name));
CREATE TABLE IF NOT EXISTS passkey_invites(user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,token_hash TEXT NOT NULL UNIQUE,expires_at INTEGER NOT NULL,generation INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS passkey_challenges(token_hash TEXT PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,purpose TEXT NOT NULL,payload TEXT NOT NULL,expires_at INTEGER NOT NULL);
`

type Passkey struct {
	ID              int64
	Name, CreatedAt string
	Credential      webauthn.Credential
}

func (s *Store) CreatePasskeyUser(ctx context.Context, admin User, username, role, code string, now time.Time) error {
	return s.createPasskeyUser(ctx, &admin, username, role, code, now)
}

func (s *Store) CreatePasskeyUserCLI(ctx context.Context, username, role, code string, now time.Time) error {
	return s.createPasskeyUser(ctx, nil, username, role, code, now)
}

func (s *Store) createPasskeyUser(ctx context.Context, admin *User, username, role, code string, now time.Time) error {
	if strings.TrimSpace(username) == "" || (role != "user" && role != "group_admin") || len(code) < 32 {
		return fmt.Errorf("valid user, role and enrollment code required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor := "local-maintenance"
	if admin != nil {
		if err = tx.QueryRowContext(ctx, `SELECT username FROM users WHERE id=? AND role='portal_admin' AND enabled=1 AND locked_at=0 AND must_change_password=0 AND auth_generation=?`, admin.ID, admin.AuthGeneration).Scan(&actor); err != nil {
			return err
		}
	}
	// The existing completion flag also gates machine credentials until enrollment.
	res, err := tx.ExecContext(ctx, `INSERT INTO users(username,password_hash,role,enabled,must_change_password,expires_at) VALUES(?,'!passkey-only',?,1,1,?)`, username, role, now.Add(30*24*time.Hour).Unix())
	if err != nil {
		return err
	}
	uid, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO passkey_invites VALUES(?,?,?,0)`, uid, tokenHash(code), now.Add(30*time.Minute).Unix()); err != nil {
		return err
	}
	if err = passkeyAudit(ctx, tx, actor, "user.create", username, "role="+role); err != nil {
		return err
	}
	if err = passkeyAudit(ctx, tx, actor, "passkey.invite", username, "initial account registration"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Passkeys(ctx context.Context, uid int64) ([]Passkey, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,name,created_at,credential FROM passkeys WHERE user_id=? ORDER BY id`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []Passkey{}
	for rows.Next() {
		var k Passkey
		var data string
		if err = rows.Scan(&k.ID, &k.Name, &k.CreatedAt, &data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(data), &k.Credential); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func passkeyAudit(ctx context.Context, tx *sql.Tx, actor, action, target, reason string) error {
	b, _ := json.Marshal(map[string]string{"reason": reason})
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_log(actor,action,target,result,detail) VALUES(?,?,?,'ok',?)`, actor, action, target, string(b))
	return err
}

// IssuePasskeyInvite is an audited, registration-only grant. Reset also revokes
// credentials, outstanding challenges, and all sessions via auth_generation.
// It deliberately does not unlock or reactivate the account.
func (s *Store) IssuePasskeyInvite(ctx context.Context, admin User, username, reason, token string, reset bool, now time.Time) error {
	return s.issuePasskeyInvite(ctx, &admin, username, reason, token, reset, now)
}

// IssuePasskeyInviteCLI is only for the local maintenance CLI, with Portal stopped.
func (s *Store) IssuePasskeyInviteCLI(ctx context.Context, username, reason, token string, reset bool, now time.Time) error {
	return s.issuePasskeyInvite(ctx, nil, username, reason, token, reset, now)
}

func (s *Store) issuePasskeyInvite(ctx context.Context, admin *User, username, reason, token string, reset bool, now time.Time) error {
	reason = strings.TrimSpace(reason)
	if reason == "" || len([]rune(reason)) > 500 || len(token) < 32 {
		return fmt.Errorf("reason and random token required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor := "local-maintenance"
	if admin != nil {
		if err = tx.QueryRowContext(ctx, `SELECT username FROM users WHERE id=? AND role='portal_admin' AND enabled=1 AND locked_at=0 AND must_change_password=0 AND auth_generation=?`, admin.ID, admin.AuthGeneration).Scan(&actor); err != nil {
			return err
		}
	}
	var uid, generation int64
	if err = tx.QueryRowContext(ctx, `SELECT id,auth_generation FROM users WHERE username=? AND enabled=1`, username).Scan(&uid, &generation); err != nil {
		return err
	}
	action := "passkey.invite"
	if reset {
		action = "passkey.reset"
		generation++
		if _, err = tx.ExecContext(ctx, `UPDATE users SET auth_generation=auth_generation+1 WHERE id=?`, uid); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM passkeys WHERE user_id=?`, uid); err != nil {
			return err
		}
		// All dedicated credentials must be reissued after a security reset.
		if err = revokeCredentialsTx(ctx, tx, uid); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM passkey_challenges WHERE user_id=?`, uid); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO passkey_invites VALUES(?,?,?,?) ON CONFLICT(user_id) DO UPDATE SET token_hash=excluded.token_hash,expires_at=excluded.expires_at,generation=excluded.generation`, uid, tokenHash(token), now.Add(30*time.Minute).Unix(), generation); err != nil {
		return err
	}
	if err = passkeyAudit(ctx, tx, actor, action, username, reason); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PasskeyInvite(ctx context.Context, uid int64, hash string, generation int64, now time.Time) error {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM passkey_invites WHERE user_id=? AND token_hash=? AND generation=? AND expires_at>?`, uid, hash, generation, now.Unix()).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) SavePasskeyChallenge(ctx context.Context, token string, uid int64, purpose, payload string, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM passkey_challenges WHERE expires_at<=?`, now.Unix()); err != nil {
		return err
	}
	var total, perUser int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(user_id=?),0) FROM passkey_challenges`, uid).Scan(&total, &perUser); err != nil {
		return err
	}
	if total >= 4096 {
		return fmt.Errorf("too many outstanding authentication attempts")
	}
	if perUser >= 16 {
		// Public login starts cannot reserve every account slot for two minutes.
		// Rotate the oldest login challenge; never evict a registration grant.
		res, err := tx.ExecContext(ctx, `DELETE FROM passkey_challenges WHERE token_hash=(SELECT token_hash FROM passkey_challenges WHERE user_id=? AND purpose='login' ORDER BY expires_at, rowid LIMIT 1)`, uid)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("too many outstanding registration attempts")
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO passkey_challenges VALUES(?,?,?,?,?)`, tokenHash(token), uid, purpose, payload, now.Add(2*time.Minute).Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ConsumePasskeyChallenge(ctx context.Context, token, purpose string, now time.Time) (string, error) {
	var payload string
	err := s.DB.QueryRowContext(ctx, `DELETE FROM passkey_challenges WHERE token_hash=? AND purpose=? AND expires_at>? RETURNING payload`, tokenHash(token), purpose, now.Unix()).Scan(&payload)
	return payload, err
}

// CompletePasskeyRegistration atomically rechecks the account and invite. A
// simultaneous reset, second finish, or enrollment-code replay cannot register.
func (s *Store) CompletePasskeyRegistration(ctx context.Context, u User, name string, c *webauthn.Credential, inviteHash string, now time.Time) error {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 80 {
		return fmt.Errorf("PC name required (1-80 characters)")
	}
	if err := auth.CheckDevicePasskey(c); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id=? AND auth_generation=? AND enabled=1 AND locked_at=0 AND login_retry_at<=? AND (role='portal_admin' OR expires_at=0 OR expires_at>=?) AND (?!='' OR must_change_password=0)`, u.ID, u.AuthGeneration, now.Unix(), now.Unix(), inviteHash).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return sql.ErrNoRows
	}
	if inviteHash != "" {
		res, e := tx.ExecContext(ctx, `DELETE FROM passkey_invites WHERE user_id=? AND token_hash=? AND generation=? AND expires_at>?`, u.ID, inviteHash, u.AuthGeneration, now.Unix())
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return sql.ErrNoRows
		}
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM passkeys WHERE user_id=?`, u.ID).Scan(&n); err != nil {
		return err
	}
	if n >= 10 {
		return fmt.Errorf("maximum 10 PCs")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO passkeys(user_id,credential_id,name,credential) VALUES(?,?,?,?)`, u.ID, c.ID, name, string(b)); err != nil {
		return err
	}
	// General users no longer have a password-change step. Admin recovery still does.
	if _, err = tx.ExecContext(ctx, `UPDATE users SET must_change_password=0,temp_password_expires=0 WHERE id=? AND role!='portal_admin'`, u.ID); err != nil {
		return err
	}
	if err = passkeyAudit(ctx, tx, u.Username, "passkey.register", u.Username, name); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordPasskeyLogin persists the counter and account login in one transaction.
func (s *Store) RecordPasskeyLogin(ctx context.Context, u User, c *webauthn.Credential, now time.Time) error {
	return s.recordPasskeyAuthentication(ctx, u, c, now, "login")
}
func (s *Store) RecordPasskeyReauthentication(ctx context.Context, u User, c *webauthn.Credential, now time.Time) error {
	return s.recordPasskeyAuthentication(ctx, u, c, now, "reauth.passkey")
}
func (s *Store) recordPasskeyAuthentication(ctx context.Context, u User, c *webauthn.Credential, now time.Time, action string) error {
	if err := auth.CheckDevicePasskey(c); err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldJSON string
	if err = tx.QueryRowContext(ctx, `SELECT credential FROM passkeys WHERE user_id=? AND credential_id=?`, u.ID, c.ID).Scan(&oldJSON); err != nil {
		return err
	}
	var old webauthn.Credential
	if err = json.Unmarshal([]byte(oldJSON), &old); err != nil {
		return err
	}
	if old.Authenticator.SignCount != 0 && c.Authenticator.SignCount <= old.Authenticator.SignCount {
		return fmt.Errorf("passkey counter replay")
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE passkeys SET credential=? WHERE user_id=? AND credential_id=?`, string(b), u.ID, c.ID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE users SET last_login_at=?,expires_at=?,disabled_at=0,disabled_reason='',failed_login_attempts=0,login_retry_at=0 WHERE id=? AND auth_generation=? AND enabled=1 AND locked_at=0 AND login_retry_at<=? AND (role='portal_admin' OR expires_at=0 OR expires_at>=?) AND (must_change_password=0 OR role='portal_admin') AND (temp_password_expires=0 OR temp_password_expires>=? OR role!='portal_admin')`, now.Unix(), now.Add(30*24*time.Hour).Unix(), u.ID, u.AuthGeneration, now.Unix(), now.Unix(), now.Unix())
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	if err = passkeyAudit(ctx, tx, u.Username, action, u.Username, "passkey"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeletePasskey(ctx context.Context, u User, id int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id=? AND enabled=1 AND locked_at=0 AND auth_generation=?`, u.ID, u.AuthGeneration).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return sql.ErrNoRows
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM passkeys WHERE user_id=?`, u.ID).Scan(&n); err != nil {
		return err
	}
	if n <= 1 {
		return fmt.Errorf("last passkey cannot be deleted; contact administrator")
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM passkeys WHERE user_id=? AND id=?`, u.ID, id)
	if err != nil {
		return err
	}
	n64, _ := res.RowsAffected()
	if n64 != 1 {
		return sql.ErrNoRows
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET auth_generation=auth_generation+1 WHERE id=?`, u.ID); err != nil {
		return err
	}
	if err = passkeyAudit(ctx, tx, u.Username, "passkey.delete", u.Username, "PC credential revoked; sessions invalidated"); err != nil {
		return err
	}
	return tx.Commit()
}
