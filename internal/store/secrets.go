package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

const secretPrefix = "aesgcm:v1:"
const securitySchema = `
CREATE TABLE IF NOT EXISTS security_state(id INTEGER PRIMARY KEY CHECK(id=1),needs_vacuum INTEGER NOT NULL DEFAULT 0);
INSERT OR IGNORE INTO security_state(id) VALUES(1);
`

func mfaScope(uid int64) string { return "mfa:" + strconv.FormatInt(uid, 10) }

// A separate 0600 key file protects database-only backups. It is not a defense
// against compromise of the running service account. Back up the key separately.
func (s *Store) initSecrets(ctx context.Context) error {
	if s.secrets != nil {
		return nil
	}
	key := make([]byte, 32)
	if s.path == ":memory:" {
		if _, err := rand.Read(key); err != nil {
			return err
		}
	} else {
		keyPath := os.Getenv("AWSPORTAL_TOTP_KEY_FILE")
		if keyPath == "" {
			keyPath = s.path + ".totp-key"
		}
		info, err := os.Lstat(keyPath)
		if errors.Is(err, os.ErrNotExist) {
			var encrypted int
			if err = s.DB.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM users WHERE totp_secret LIKE 'aesgcm:%')+(SELECT COUNT(*) FROM mfa_devices WHERE secret LIKE 'aesgcm:%')`).Scan(&encrypted); err != nil {
				return err
			}
			if encrypted != 0 {
				return fmt.Errorf("TOTP key file is missing; restore the original key, do not generate a replacement")
			}
			if _, err = rand.Read(key); err != nil {
				return err
			}
			f, e := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if e != nil {
				return fmt.Errorf("create TOTP key file: %w", e)
			}
			_, e = f.Write(key)
			if e == nil {
				e = f.Sync()
			}
			closeErr := f.Close()
			if e != nil {
				return e
			}
			if closeErr != nil {
				return closeErr
			}
		} else {
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
				return fmt.Errorf("TOTP key file must be a regular owner-only file (0600)")
			}
			key, err = os.ReadFile(keyPath)
			if err != nil {
				return err
			}
		}
	}
	if len(key) != 32 {
		return fmt.Errorf("TOTP key file must contain exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	s.secrets, err = cipher.NewGCM(block)
	return err
}

func (s *Store) sealSecret(value, scope string) (string, error) {
	if value == "" {
		return "", nil
	}
	if s.secrets == nil {
		return "", fmt.Errorf("secret store not initialized")
	}
	nonce := make([]byte, s.secrets.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := s.secrets.Seal(nonce, nonce, []byte(value), []byte("awsportal:"+scope))
	return secretPrefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

func (s *Store) openSecret(value, scope string) (string, error) {
	if value == "" {
		return "", nil
	}
	if s.secrets == nil || !strings.HasPrefix(value, secretPrefix) {
		return "", fmt.Errorf("TOTP secret is not protected; migration required")
	}
	b, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, secretPrefix))
	if err != nil || len(b) < s.secrets.NonceSize()+s.secrets.Overhead() {
		return "", fmt.Errorf("invalid encrypted TOTP secret")
	}
	n := s.secrets.NonceSize()
	plain, err := s.secrets.Open(nil, b[:n], b[n:], []byte("awsportal:"+scope))
	if err != nil {
		return "", fmt.Errorf("cannot decrypt TOTP secret: check the original key file")
	}
	return string(plain), nil
}

func (s *Store) migrateSecrets(ctx context.Context) error {
	if err := s.initSecrets(ctx); err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	changed := false
	for _, table := range []string{"users", "mfa_devices"} {
		column, scopeColumn := "totp_secret", "username"
		if table == "mfa_devices" {
			column, scopeColumn = "secret", "user_id"
		}
		rows, e := tx.QueryContext(ctx, "SELECT id,"+scopeColumn+","+column+" FROM "+table+" WHERE "+column+"!=''")
		if e != nil {
			return e
		}
		type update struct {
			id    int64
			value string
		}
		var updates []update
		for rows.Next() {
			var id int64
			var scope, value string
			if e = rows.Scan(&id, &scope, &value); e != nil {
				rows.Close()
				return e
			}
			if table == "mfa_devices" {
				scope = "mfa:" + scope
			} else {
				scope = "user:" + scope
			}
			if strings.HasPrefix(value, "aesgcm:") {
				if _, e = s.openSecret(value, scope); e != nil {
					rows.Close()
					return e
				}
			} else {
				value, e = s.sealSecret(value, scope)
				if e != nil {
					rows.Close()
					return e
				}
				updates = append(updates, update{id, value})
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, u := range updates {
			if _, e = tx.ExecContext(ctx, "UPDATE "+table+" SET "+column+"=? WHERE id=?", u.value, u.id); e != nil {
				return e
			}
			changed = true
		}
	}
	if changed {
		if _, err = tx.ExecContext(ctx, `UPDATE security_state SET needs_vacuum=1 WHERE id=1`); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	var pending int
	if err = s.DB.QueryRowContext(ctx, `SELECT needs_vacuum FROM security_state WHERE id=1`).Scan(&pending); err != nil {
		return err
	}
	if pending == 1 {
		// Remove old plaintext pages as well as WAL frames. A failed migration
		// retains the pending marker and fails closed; startup retries the cleanup.
		if _, err = s.DB.ExecContext(ctx, `VACUUM`); err != nil {
			return err
		}
		if _, err = s.DB.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			return err
		}
		_, err = s.DB.ExecContext(ctx, `UPDATE security_state SET needs_vacuum=0 WHERE id=1`)
	}
	return err
}
