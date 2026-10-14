package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const LoginFailureLimit = 10

// Match Apple's published iOS/iPadOS lock-screen schedule, starting at attempt 4.
// https://support.apple.com/ja-jp/guide/security/sec20230a10d/web
func LoginFailureDelay(attempts int) time.Duration {
	switch attempts {
	case 4:
		return time.Minute
	case 5:
		return 5 * time.Minute
	case 6:
		return 15 * time.Minute
	case 7:
		return time.Hour
	case 8:
		return 3 * time.Hour
	case 9:
		return 8 * time.Hour
	default:
		return 0
	}
}

// LoginAuditDetail never contains passwords, OTPs, cookies or request URLs.
// ForwardedFor is client-supplied metadata, not a verified source address.
type LoginAuditDetail struct {
	Reason         string `json:"reason"`
	RemoteIP       string `json:"remote_ip"`
	UserAgent      string `json:"user_agent"`
	ForwardedFor   string `json:"forwarded_for,omitempty"`
	FailedAttempts int    `json:"failed_attempts"`
	LockedAt       int64  `json:"locked_at"`
	FailureLimit   int    `json:"failure_limit"`
	RetryAt        int64  `json:"retry_at"`
}

func insertLoginAudit(ctx context.Context, tx *sql.Tx, actor, action, target, result string, detail LoginAuditDetail, now time.Time) error {
	b, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_log(at,actor,action,target,result,detail) VALUES(?,?,?,?,?,?)`, now.UTC().Format("2006-01-02 15:04:05.999999999"), actor, action, target, result, string(b))
	return err
}

// RecordLoginFailure commits the counter, lock and audit entries together.
// Only incorrect credentials count; disabled/expired/unknown accounts are logged.
func (s *Store) RecordLoginFailure(ctx context.Context, username string, detail LoginAuditDetail, now time.Time) (LoginAuditDetail, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return detail, err
	}
	defer tx.Rollback()
	var id, before int64
	err = tx.QueryRowContext(ctx, `SELECT id,failed_login_attempts,locked_at,login_retry_at FROM users WHERE username=?`, username).Scan(&id, &detail.FailedAttempts, &before, &detail.RetryAt)
	if err != nil && err != sql.ErrNoRows {
		return detail, err
	}
	if err == nil {
		detail.LockedAt = before
		if before != 0 {
			detail.Reason = "account_locked"
		} else if detail.RetryAt > now.Unix() {
			detail.Reason = "login_throttled"
		} else if detail.Reason == "invalid_password" || detail.Reason == "invalid_mfa" || detail.Reason == "invalid_passkey" || detail.Reason == "invalid_enrollment_code" {
			next := detail.FailedAttempts + 1
			retryAt := int64(0)
			if delay := LoginFailureDelay(next); delay > 0 {
				retryAt = now.Add(delay).Unix()
			}
			err = tx.QueryRowContext(ctx, `UPDATE users SET failed_login_attempts=failed_login_attempts+1,locked_at=CASE WHEN failed_login_attempts+1>=? THEN ? ELSE 0 END,login_retry_at=? WHERE id=? AND enabled=1 AND locked_at=0 RETURNING failed_login_attempts,locked_at,login_retry_at`, LoginFailureLimit, now.Unix(), retryAt, id).Scan(&detail.FailedAttempts, &detail.LockedAt, &detail.RetryAt)
			if err != nil && err != sql.ErrNoRows {
				return detail, err
			}
		}
	}
	detail.FailureLimit = LoginFailureLimit
	// Bound unrecognized usernames without altering the account lookup.
	actor := username
	if id == 0 {
		runes := []rune(username)
		actor = string(runes[:min(len(runes), 256)])
	}
	if err = insertLoginAudit(ctx, tx, actor, "login", actor, "deny", detail, now); err != nil {
		return detail, err
	}
	if before == 0 && detail.LockedAt != 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE users SET auth_generation=auth_generation+1 WHERE id=?`, id); err != nil {
			return detail, err
		}
		if err = revokeCredentialsTx(ctx, tx, id); err != nil {
			return detail, err
		}
		if err = insertLoginAudit(ctx, tx, actor, "account.lock", actor, "ok", detail, now); err != nil {
			return detail, err
		}
	} else if detail.Reason != "login_throttled" && detail.RetryAt > now.Unix() {
		if err = insertLoginAudit(ctx, tx, actor, "account.login_delay", actor, "ok", detail, now); err != nil {
			return detail, err
		}
	}
	return detail, tx.Commit()
}

// UnlockUser verifies the acting administrator in the database and records the
// previous lock state and operator's reason in the same transaction as the reset.
func (s *Store) UnlockUser(ctx context.Context, admin User, username, reason string, detail LoginAuditDetail, now time.Time) error {
	reason = strings.TrimSpace(reason)
	if reason == "" || len([]rune(reason)) > 500 {
		return fmt.Errorf("unlock reason must contain 1 to 500 characters")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var actor string
	if err = tx.QueryRowContext(ctx, `SELECT username FROM users WHERE id=? AND role='portal_admin' AND enabled=1 AND locked_at=0 AND must_change_password=0`, admin.ID).Scan(&actor); err != nil {
		return fmt.Errorf("active portal administrator required: %w", err)
	}
	if err = tx.QueryRowContext(ctx, `SELECT failed_login_attempts,locked_at,login_retry_at FROM users WHERE username=? AND locked_at!=0`, username).Scan(&detail.FailedAttempts, &detail.LockedAt, &detail.RetryAt); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET failed_login_attempts=0,locked_at=0,login_retry_at=0 WHERE username=?`, username); err != nil {
		return err
	}
	detail.Reason = reason
	detail.FailureLimit = LoginFailureLimit
	if err = insertLoginAudit(ctx, tx, actor, "account.unlock", username, "ok", detail, now); err != nil {
		return err
	}
	return tx.Commit()
}
