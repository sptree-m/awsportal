package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sptree-m/awsportal/internal/auth"
)

func TestSecretMigrationAndMissingKeyFailsClosed(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const secret = "JBSWY3DPEHPK3PXP"
	if _, err = s.DB.Exec(`INSERT INTO users(username,password_hash,role,totp_secret) VALUES('legacy','hash','portal_admin',?)`, secret); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	u, err := s.UserByName(ctx, "legacy")
	if err != nil || u.TOTPSecret != secret {
		t.Fatalf("migration read: %v", err)
	}
	var stored string
	s.DB.QueryRow(`SELECT totp_secret FROM users WHERE username='legacy'`).Scan(&stored)
	if !strings.HasPrefix(stored, secretPrefix) || strings.Contains(stored, secret) {
		t.Fatal("secret not encrypted")
	}
	dbBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(dbBytes), secret) {
		t.Fatal("legacy plaintext remains in database")
	}
	info, err := os.Stat(path + ".totp-key")
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("key permissions: %v", err)
	}
	s.Close()
	originalKey, err := os.ReadFile(path + ".totp-key")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path+".totp-key", make([]byte, 32), 0600); err != nil {
		t.Fatal(err)
	}
	wrong, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = wrong.Migrate(ctx); err == nil {
		t.Fatal("wrong key accepted")
	}
	wrong.Close()
	if err = os.WriteFile(path+".totp-key", originalKey, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(path + ".totp-key"); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx); err == nil {
		t.Fatal("missing key silently replaced")
	}
	if _, err = os.Stat(path + ".totp-key"); !os.IsNotExist(err) {
		t.Fatal("replacement key created")
	}
}

func TestSecretEncryptionBindsOwnerAndRejectsWrongKey(t *testing.T) {
	ctx := context.Background()
	s := newSecurityStore(t)
	for _, n := range []string{"one", "two"} {
		if err := s.CreateUser(ctx, n, "hash", "portal_admin", "JBSWY3DPEHPK3PXP"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB.Exec(`UPDATE users SET totp_secret=(SELECT totp_secret FROM users WHERE username='one') WHERE username='two'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserByName(ctx, "two"); err == nil {
		t.Fatal("ciphertext transplanted between accounts")
	}
}

func newSecurityStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err = s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestConcurrentTOTPUseSucceedsOnce(t *testing.T) {
	s := newSecurityStore(t)
	ctx := context.Background()
	now := time.Now()
	secret := "JBSWY3DPEHPK3PXP"
	if err := s.CreateUser(ctx, "admin", "verified-hash", "portal_admin", secret); err != nil {
		t.Fatal(err)
	}
	u, err := s.UserByName(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	code, err := auth.TOTPAt(secret, now, 6)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.RecordPasswordLogin(ctx, u, code, now)
			if err != nil {
				t.Error(err)
			}
			results <- ok
		}()
	}
	wg.Wait()
	close(results)
	n := 0
	for ok := range results {
		if ok {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("successful OTP uses=%d", n)
	}
}

func TestLockRevokesDedicatedCredentialsPermanently(t *testing.T) {
	s := newSecurityStore(t)
	ctx := context.Background()
	s.CreateUser(ctx, "user", "hash", "user", "")
	u, err := s.UserByName(ctx, "user")
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.IssueProxyCredential(ctx, u.ID, "proxy", 1)
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.IssueMirrorToken(ctx, u.ID, "git", 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`UPDATE users SET failed_login_attempts=9 WHERE id=?`, u.ID); err != nil {
		t.Fatal(err)
	}
	detail, err := s.RecordLoginFailure(ctx, u.Username, LoginAuditDetail{Reason: "invalid_passkey"}, time.Now())
	if err != nil || detail.LockedAt == 0 {
		t.Fatalf("lock: %v", err)
	}
	for _, locked := range []bool{true, false} {
		if !locked {
			s.DB.Exec(`UPDATE users SET locked_at=0,failed_login_attempts=0,login_retry_at=0 WHERE id=?`, u.ID)
		}
		if _, err = s.ProxyUser(ctx, u.Username, p); err == nil {
			t.Fatal("old proxy credential survived lock/unlock")
		}
		if _, _, err = s.MirrorTokenUser(ctx, m); err == nil {
			t.Fatal("old Git credential survived lock/unlock")
		}
	}
}
