package store

import (
	"context"
	"testing"

	"github.com/sptree-m/awsportal/internal/auth"
)

func TestPasswordAndMFAChangesRollBackWhenAuditFails(t *testing.T) {
	s := newSecurityStore(t)
	ctx := context.Background()
	hash, _ := auth.HashPassword("Original-Password-123")
	if err := s.CreateUser(ctx, "admin", hash, "portal_admin", "JBSWY3DPEHPK3PXP"); err != nil {
		t.Fatal(err)
	}
	u, _ := s.UserByName(ctx, "admin")
	id, err := s.AddMFADevice(ctx, u.ID, "Existing", "JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.IssueProxyCredential(ctx, u.ID, "Test", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("CREATE TRIGGER reject_audit BEFORE INSERT ON audit_log BEGIN SELECT RAISE(FAIL,'audit unavailable'); END;"); err != nil {
		t.Fatal(err)
	}
	replacement, _ := auth.HashPassword("Replacement-Password-123")
	if err = s.ChangePasswordFor(ctx, u, replacement); err == nil {
		t.Fatal("password change did not fail")
	}
	fresh, _ := s.UserByName(ctx, u.Username)
	if fresh.PasswordHash != hash || fresh.AuthGeneration != u.AuthGeneration {
		t.Fatal("password state was committed")
	}
	if _, err = s.ProxyUser(ctx, u.Username, token); err != nil {
		t.Fatal("token revocation was committed")
	}
	if _, err = s.AddMFADevice(ctx, u.ID, "New", "JBSWY3DPEHPK3PXP"); err == nil {
		t.Fatal("MFA add did not fail")
	}
	if err = s.DeleteMFADevice(ctx, u.ID, id); err == nil {
		t.Fatal("MFA delete did not fail")
	}
	devices, _ := s.MFADevices(ctx, u.ID)
	if len(devices) != 1 || devices[0].ID != id {
		t.Fatal("MFA state committed")
	}
}

func TestAuditedEffectRequiresIntentAndRetainsUnrecordedOutcome(t *testing.T) {
	s := newSecurityStore(t)
	ctx := context.Background()
	calls := 0
	if _, err := s.DB.Exec("CREATE TRIGGER reject_audit BEFORE INSERT ON audit_log BEGIN SELECT RAISE(FAIL,'audit unavailable'); END;"); err != nil {
		t.Fatal(err)
	}
	if err := s.AuditedEffect(ctx, "scheduler", "ec2.start", "i-test", func() error { calls++; return nil }); err == nil || calls != 0 {
		t.Fatal("unaudited AWS effect executed")
	}
	s.DB.Exec("DROP TRIGGER reject_audit")
	if err := s.AuditedEffect(ctx, "scheduler", "ec2.start", "i-test", func() error {
		calls++
		_, err := s.DB.Exec("CREATE TRIGGER reject_audit BEFORE INSERT ON audit_log BEGIN SELECT RAISE(FAIL,'audit unavailable'); END;")
		return err
	}); err != nil || calls != 1 {
		t.Fatal("effect outcome misreported")
	}
	var pending int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM audit_operations WHERE completed IS NULL").Scan(&pending); err != nil || pending != 1 {
		t.Fatal("missing durable pending intent")
	}
}
