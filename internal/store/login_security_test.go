package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func loginSecurityStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "security.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err = s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"user", "group_admin", "portal_admin"} {
		if err = s.CreateUser(context.Background(), role, "hash", role, ""); err != nil {
			t.Fatal(err)
		}
	}
	return s, path
}

func TestLoginIPhoneScheduleAndPersistentLock(t *testing.T) {
	ctx := context.Background()
	s, path := loginSecurityStore(t)
	for _, role := range []string{"user", "group_admin", "portal_admin"} {
		t.Run(role, func(t *testing.T) {
			now := time.Now().Truncate(time.Second)
			delays := []time.Duration{0, 0, 0, time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 3 * time.Hour, 8 * time.Hour, 0}
			for i, delay := range delays {
				d, err := s.RecordLoginFailure(ctx, role, LoginAuditDetail{Reason: "invalid_password"}, now)
				if err != nil || d.FailedAttempts != i+1 {
					t.Fatalf("attempt %d: %+v %v", i+1, d, err)
				}
				if i == 9 {
					if d.LockedAt != now.Unix() || d.RetryAt != 0 {
						t.Fatalf("final lock invalid: %+v", d)
					}
				} else if d.LockedAt != 0 {
					t.Fatalf("premature permanent lock: %+v", d)
				}
				if delay > 0 {
					if d.RetryAt != now.Add(delay).Unix() {
						t.Fatalf("attempt %d delay=%d want=%v", i+1, d.RetryAt-now.Unix(), delay)
					}
					blocked, err := s.RecordLoginFailure(ctx, role, LoginAuditDetail{Reason: "invalid_mfa"}, now.Add(delay-time.Second))
					if err != nil || blocked.Reason != "login_throttled" || blocked.FailedAttempts != i+1 || blocked.RetryAt != d.RetryAt {
						t.Fatalf("request changed cooldown: %+v %v", blocked, err)
					}
					u, _ := s.UserByName(ctx, role)
					if err = s.RecordLogin(ctx, u.ID, now.Add(delay-time.Second)); err == nil {
						t.Fatal("login during cooldown succeeded")
					}
					now = now.Add(delay)
				}
			}
			u, _ := s.UserByName(ctx, role)
			if err := s.RecordLogin(ctx, u.ID, now.Add(365*24*time.Hour)); err == nil {
				t.Fatal("permanent lock expired automatically")
			}
		})
	}
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err = other.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	u, err := other.UserByName(ctx, "user")
	if err != nil || u.LockedAt == 0 || u.FailedLoginAttempts != 10 {
		t.Fatalf("lock lost after reopen/migration: %+v %v", u, err)
	}
}

func TestLoginFailureResetAndAtomicAudit(t *testing.T) {
	ctx := context.Background()
	s, _ := loginSecurityStore(t)
	now := time.Now().Truncate(time.Second)
	for i := 0; i < 4; i++ {
		if _, err := s.RecordLoginFailure(ctx, "user", LoginAuditDetail{Reason: "invalid_password"}, now); err != nil {
			t.Fatal(err)
		}
	}
	u, _ := s.UserByName(ctx, "user")
	if err := s.RecordLogin(ctx, u.ID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	u, _ = s.UserByName(ctx, "user")
	if u.FailedLoginAttempts != 0 || u.LoginRetryAt != 0 {
		t.Fatalf("success did not reset counter/cooldown: %+v", u)
	}
	if _, err := s.DB.ExecContext(ctx, `CREATE TRIGGER reject_login_audit BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordLoginFailure(ctx, "user", LoginAuditDetail{Reason: "invalid_mfa"}, now); err == nil {
		t.Fatal("failed audit did not reject transaction")
	}
	u, _ = s.UserByName(ctx, "user")
	if u.FailedLoginAttempts != 0 {
		t.Fatal("counter committed without audit")
	}
}

func TestLoginSecurityMigratesExistingUsers(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// The original schema has no lockout columns.
	if _, err = s.DB.ExecContext(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.ExecContext(ctx, `INSERT INTO users(username,password_hash,role,totp_secret) VALUES('existing','original-hash','portal_admin','ORIGINAL')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	u, err := s.UserByName(ctx, "existing")
	if err != nil || !u.Enabled || u.PasswordHash != "original-hash" || u.TOTPSecret != "ORIGINAL" || u.FailedLoginAttempts != 0 || u.LoginRetryAt != 0 || u.LockedAt != 0 {
		t.Fatalf("existing account changed on upgrade: %+v %v", u, err)
	}
}

func TestLoginAuditExportKeepsUTCAndDetails(t *testing.T) {
	ctx := context.Background()
	s, _ := loginSecurityStore(t)
	now := time.Date(2026, 10, 12, 23, 59, 59, 123456789, time.FixedZone("JST", 9*3600))
	if _, err := s.RecordLoginFailure(ctx, "user", LoginAuditDetail{Reason: "invalid_mfa", RemoteIP: "192.0.2.1"}, now); err != nil {
		t.Fatal(err)
	}
	batch, err := s.PrepareAuditExport(ctx, "bucket/audit", "audit/")
	if err != nil || !strings.HasPrefix(batch.Key, "audit/date=2026-10-12/") {
		t.Fatalf("invalid export date: %s %v", batch.Key, err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(batch.Payload))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var entry struct{ At, Detail string }
	if err = json.NewDecoder(reader).Decode(&entry); err != nil {
		t.Fatal(err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, entry.At)
	if err != nil || !parsed.Equal(now) {
		t.Fatalf("export time corrupt: %s %v", entry.At, err)
	}
	var detail LoginAuditDetail
	if err = json.Unmarshal([]byte(entry.Detail), &detail); err != nil || detail.Reason != "invalid_mfa" || detail.RemoteIP != "192.0.2.1" || detail.FailedAttempts != 1 {
		t.Fatalf("export details lost: %+v %v", detail, err)
	}
}

func TestConcurrentFailuresCannotSkipDelay(t *testing.T) {
	ctx := context.Background()
	s, path := loginSecurityStore(t)
	// Two connections model a portal and a maintenance process sharing SQLite.
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	for _, db := range []*Store{s, other} {
		if _, err = db.DB.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	now := time.Now()
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// SQLite can reject a concurrent deferred transaction upgrade; callers
			// fail closed. Successful writes must still respect the first cooldown.
			db := []*Store{s, other}[i%2]
			_, _ = db.RecordLoginFailure(ctx, "user", LoginAuditDetail{Reason: "invalid_password"}, now)
		}(i)
	}
	wg.Wait()
	u, _ := s.UserByName(ctx, "user")
	if u.FailedLoginAttempts > 4 || u.LockedAt != 0 {
		t.Fatalf("concurrent requests skipped cooldown: %+v", u)
	}
	entries, err := s.AuditEntries(ctx, 200)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		var d LoginAuditDetail
		if err = json.Unmarshal([]byte(entry.Detail), &d); err != nil {
			t.Fatal(err)
		}
		if entry.Action == "login" && d.Reason == "invalid_password" {
			count++
		}
	}
	if count != u.FailedLoginAttempts {
		t.Fatalf("audit/counter mismatch: %d vs %d", count, u.FailedLoginAttempts)
	}
}

func TestUnlockAuthorizationAndAccountState(t *testing.T) {
	ctx := context.Background()
	s, _ := loginSecurityStore(t)
	now := time.Now()
	if _, err := s.DB.ExecContext(ctx, `UPDATE users SET failed_login_attempts=10,locked_at=?,enabled=0 WHERE username='user'`, now.Unix()); err != nil {
		t.Fatal(err)
	}
	user, _ := s.UserByName(ctx, "user")
	group, _ := s.UserByName(ctx, "group_admin")
	admin, _ := s.UserByName(ctx, "portal_admin")
	for _, actor := range []User{user, group, {ID: group.ID, Role: "portal_admin"}, {ID: 9999, Role: "portal_admin"}} {
		if err := s.UnlockUser(ctx, actor, "user", "本人確認済み", LoginAuditDetail{}, now); err == nil {
			t.Fatal("non-administrator unlocked account")
		}
	}
	if err := s.UnlockUser(ctx, admin, "user", " ", LoginAuditDetail{}, now); err == nil {
		t.Fatal("empty reason accepted")
	}
	if err := s.ReactivateUser(ctx, "user", now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTemporaryPassword(ctx, "user", "new", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	user, _ = s.UserByName(ctx, "user")
	if user.LockedAt == 0 || user.FailedLoginAttempts != 10 {
		t.Fatal("reactivation/password reset cleared the lock")
	}
	if err := s.SetUserEnabled(ctx, "user", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `CREATE TRIGGER reject_unlock_audit BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.UnlockUser(ctx, admin, "user", "本人確認済み", LoginAuditDetail{}, now); err == nil {
		t.Fatal("unlock succeeded without audit")
	}
	user, _ = s.UserByName(ctx, "user")
	if user.LockedAt == 0 || user.FailedLoginAttempts != 10 {
		t.Fatal("unlock committed without audit")
	}
	if _, err := s.DB.ExecContext(ctx, `DROP TRIGGER reject_unlock_audit`); err != nil {
		t.Fatal(err)
	}
	// Unlock does not reactivate a disabled account or change its password.
	if err := s.UnlockUser(ctx, admin, "user", "本人確認済み", LoginAuditDetail{RemoteIP: "192.0.2.1"}, now); err != nil {
		t.Fatal(err)
	}
	user, _ = s.UserByName(ctx, "user")
	if user.LockedAt != 0 || user.FailedLoginAttempts != 0 || user.Enabled || user.PasswordHash != "new" || !user.MustChangePassword {
		t.Fatalf("invalid unlocked state: %+v", user)
	}
	entries, _ := s.AuditEntries(ctx, 200)
	var d LoginAuditDetail
	if len(entries) != 1 {
		t.Fatalf("unlock audit count: %d", len(entries))
	}
	if err := json.Unmarshal([]byte(entries[0].Detail), &d); err != nil || entries[0].Actor != admin.Username || entries[0].Action != "account.unlock" || d.Reason != "本人確認済み" || d.FailedAttempts != 10 || d.LockedAt == 0 {
		t.Fatalf("invalid unlock audit: %+v %+v %v", entries[0], d, err)
	}
}

func TestBreakGlassClearsAdminLockWithAudit(t *testing.T) {
	ctx := context.Background()
	s, _ := loginSecurityStore(t)
	admin, _ := s.UserByName(ctx, "portal_admin")
	if _, err := s.AddMFADevice(ctx, admin.ID, "old", "OLD"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE users SET failed_login_attempts=10,locked_at=1 WHERE id=?`, admin.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.BreakGlassResetAdmin(ctx, admin.Username, "new", "NEW"); err != nil {
		t.Fatal(err)
	}
	admin, _ = s.UserByName(ctx, admin.Username)
	devices, _ := s.MFADevices(ctx, admin.ID)
	entries, _ := s.AuditEntries(ctx, 200)
	var recoveries, unlocks int
	for _, entry := range entries {
		if entry.Action == "admin.recovery" {
			recoveries++
		}
		if entry.Action == "account.unlock" {
			unlocks++
		}
	}
	if admin.LockedAt != 0 || admin.FailedLoginAttempts != 0 || !admin.MustChangePassword || admin.TOTPSecret != "NEW" || len(devices) != 0 || recoveries != 1 || unlocks != 1 {
		t.Fatalf("invalid recovery: %+v devices=%d events=%d", admin, len(devices), len(entries))
	}
}
