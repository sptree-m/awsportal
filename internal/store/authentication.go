package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/sptree-m/awsportal/internal/auth"
)

// Credential revocation is committed with the security state change. Unlocking
// later must not revive credentials that existed before the lock/reset.
func revokeCredentialsTx(ctx context.Context, tx *sql.Tx, uid int64) error {
	for _, table := range []string{"proxy_credentials", "mirror_tokens", "dcv_tokens", "passkey_challenges", "passkey_invites"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE user_id=?", uid); err != nil {
			return err
		}
	}
	return nil
}

// RecordPasswordLogin rechecks the password version, consumes one TOTP step,
// records login and updates account state in one transaction. Only the hash is
// compared here; bcrypt verification happens before entering the transaction.
func (s *Store) RecordPasswordLogin(ctx context.Context, u User, code string, now time.Time) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var fallback string
	var last int64
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(totp_secret,''),last_totp_counter FROM users WHERE id=? AND auth_generation=? AND password_hash=? AND role='portal_admin' AND enabled=1 AND locked_at=0 AND login_retry_at<=? AND (temp_password_expires=0 OR temp_password_expires>=?)`, u.ID, u.AuthGeneration, u.PasswordHash, now.Unix(), now.Unix()).Scan(&fallback, &last)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,secret,last_totp_counter FROM mfa_devices WHERE user_id=? AND verified=1`, u.ID)
	if err != nil {
		return false, err
	}
	type device struct {
		id, last int64
		secret   string
	}
	var devices []device
	for rows.Next() {
		var d device
		if err = rows.Scan(&d.id, &d.secret, &d.last); err != nil {
			rows.Close()
			return false, err
		}
		devices = append(devices, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	if len(devices) == 0 {
		devices = append(devices, device{last: last, secret: fallback})
	}
	for _, d := range devices {
		scope := mfaScope(u.ID)
		if d.id == 0 {
			scope = "user:" + u.Username
		}
		secret, err := s.openSecret(d.secret, scope)
		if err != nil {
			return false, err
		}
		counter, valid := auth.MatchTOTP(secret, code, now)
		if !valid || secret == "" || counter <= d.last {
			continue
		}
		var result sql.Result
		if d.id == 0 {
			result, err = tx.ExecContext(ctx, `UPDATE users SET last_totp_counter=? WHERE id=? AND last_totp_counter<?`, counter, u.ID, counter)
		} else {
			result, err = tx.ExecContext(ctx, `UPDATE mfa_devices SET last_totp_counter=? WHERE id=? AND user_id=? AND last_totp_counter<?`, counter, d.id, u.ID, counter)
		}
		if err != nil {
			return false, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return false, err
		}
		if n != 1 {
			return false, nil
		}
		if _, err = tx.ExecContext(ctx, `UPDATE users SET last_login_at=?,expires_at=?,failed_login_attempts=0,login_retry_at=0 WHERE id=?`, now.Unix(), now.Add(30*24*time.Hour).Unix(), u.ID); err != nil {
			return false, err
		}
		if err = insertLoginAudit(ctx, tx, u.Username, "login", u.Username, "ok", LoginAuditDetail{Reason: "password_totp"}, now); err != nil {
			return false, err
		}
		return true, tx.Commit()
	}
	return false, nil
}

func (s *Store) CompleteMFAVerification(ctx context.Context, u User, id int64, code string, now time.Time) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var encrypted string
	err = tx.QueryRowContext(ctx, `SELECT m.secret FROM mfa_devices m JOIN users u ON u.id=m.user_id WHERE m.id=? AND m.user_id=? AND m.verified=0 AND u.role='portal_admin' AND u.enabled=1 AND u.locked_at=0 AND u.auth_generation=?`, id, u.ID, u.AuthGeneration).Scan(&encrypted)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	secret, err := s.openSecret(encrypted, mfaScope(u.ID))
	if err != nil {
		return false, err
	}
	counter, valid := auth.MatchTOTP(secret, code, now)
	if !valid {
		return false, nil
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mfa_devices SET verified=1,last_totp_counter=? WHERE id=?`, counter, id); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET auth_generation=auth_generation+1 WHERE id=?`, u.ID); err != nil {
		return false, err
	}
	if err = revokeCredentialsTx(ctx, tx, u.ID); err != nil {
		return false, err
	}
	if err = passkeyAudit(ctx, tx, u.Username, "mfa.enroll.verify", u.Username, "MFA confirmed; sessions and machine tokens revoked"); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
