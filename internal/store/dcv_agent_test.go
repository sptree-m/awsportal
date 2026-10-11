package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDCVAgentIsolationReadinessAndLifecycle(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	for _, v := range []struct{ name, role string }{{"admin", "portal_admin"}, {"alice", "user"}, {"bob", "user"}} {
		if e = s.CreateUser(ctx, v.name, "x", v.role, ""); e != nil {
			t.Fatal(e)
		}
	}
	admin, _ := s.UserByName(ctx, "admin")
	alice, _ := s.UserByName(ctx, "alice")
	bob, _ := s.UserByName(ctx, "bob")
	_, e = s.DB.Exec(`INSERT INTO instances(instance_id,name,dcv_host) VALUES('i-a','A','a.example'),('i-b','B','b.example');INSERT INTO groups(id,name) VALUES(1,'team');`)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.SetAssignment(ctx, admin, "i-a", "user", alice.ID, true, false); e != nil {
		t.Fatal(e)
	}
	if e = s.SetAssignment(ctx, admin, "i-a", "group", 1, false, false); e != nil {
		t.Fatal(e)
	}
	if e = s.SetGroupMember(ctx, admin, 1, bob.ID, false); e != nil {
		t.Fatal(e)
	}
	token := strings.Repeat("a", 64)
	if e = s.ConfigureDCV(ctx, alice, "i-a", "a.example", "native", token); e == nil {
		t.Fatal("user configured DCV")
	}
	if e = s.ConfigureDCV(ctx, admin, "i-a", "bad/host", "native", token); e == nil {
		t.Fatal("unsafe host accepted")
	}
	if e = s.ConfigureDCV(ctx, admin, "i-a", "a.example", "native", token); e != nil {
		t.Fatal(e)
	}
	if id, e := s.DCVAgentInstance(ctx, token); e != nil || id != "i-a" {
		t.Fatal(id, e)
	}
	now := time.Now()
	accounts, e := s.DCVAccounts(ctx, "i-a", now)
	if e != nil || len(accounts) != 3 {
		t.Fatal(accounts, e)
	}
	if _, e = s.IssueDCVToken(ctx, alice, "i-a", "not-ready", now.Add(time.Minute)); e == nil {
		t.Fatal("unready session accepted")
	}
	if e = s.DCVHeartbeat(ctx, "i-a", []int64{alice.ID, bob.ID}, "", now, 1, 1); e != nil {
		t.Fatal(e)
	}
	issued, e := s.IssueDCVToken(ctx, alice, "i-a", "alice-token", now.Add(time.Minute))
	if e != nil || issued.DCVSessionID != DCVIdentity(alice.ID) {
		t.Fatal(issued, e)
	}
	if _, ok := s.ConsumeDCVToken(ctx, "alice-token", issued.DCVSessionID, now); ok {
		t.Fatal("legacy endpoint consumed managed token")
	}
	if _, ok := s.ConsumeInstanceDCVToken(ctx, "alice-token", issued.DCVSessionID, "i-b", now); ok {
		t.Fatal("wrong instance consumed token")
	}
	if _, ok := s.ConsumeInstanceDCVToken(ctx, "alice-token", DCVIdentity(bob.ID), "i-a", now); ok {
		t.Fatal("wrong user session consumed token")
	}
	if name, ok := s.ConsumeInstanceDCVToken(ctx, "alice-token", issued.DCVSessionID, "i-a", now); !ok || name != DCVIdentity(alice.ID) {
		t.Fatal(name, ok)
	}
	if _, ok := s.ConsumeInstanceDCVToken(ctx, "alice-token", issued.DCVSessionID, "i-a", now); ok {
		t.Fatal("token reused")
	}
	if _, e = s.IssueDCVToken(ctx, bob, "i-a", "bob-token", now.Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	if e = s.SetGroupMember(ctx, admin, 1, bob.ID, true); e != nil {
		t.Fatal(e)
	}
	if _, ok := s.ConsumeInstanceDCVToken(ctx, "bob-token", DCVIdentity(bob.ID), "i-a", now); ok {
		t.Fatal("revoked group member allowed")
	}
	_, _ = s.DB.Exec("UPDATE users SET must_change_password=1 WHERE id=?", alice.ID)
	accounts, _ = s.DCVAccounts(ctx, "i-a", now)
	if len(accounts) != 1 {
		t.Fatal("password-reset user remained active", accounts)
	}
	_, _ = s.DB.Exec("UPDATE users SET must_change_password=0,expires_at=? WHERE id=?", now.Add(-time.Second).Unix(), alice.ID)
	accounts, _ = s.DCVAccounts(ctx, "i-a", now)
	if len(accounts) != 1 {
		t.Fatal("expired user remained active", accounts)
	}
	if e = s.SetInstanceEnabled(ctx, admin, "i-a", false); e != nil {
		t.Fatal(e)
	}
	accounts, _ = s.DCVAccounts(ctx, "i-a", now)
	if len(accounts) != 0 {
		t.Fatal("disabled instance has accounts")
	}
	if e = s.ConfigureDCV(ctx, admin, "i-a", "a.example", "native", strings.Repeat("b", 64)); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DCVAgentInstance(ctx, token); e == nil {
		t.Fatal("rotated credential accepted")
	}
	if _, e = s.DB.Exec("UPDATE users SET enabled=0 WHERE id=?", bob.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.SetInstanceEnabled(ctx, admin, "i-a", true); e != nil {
		t.Fatal(e)
	}
	accounts, _ = s.DCVAccounts(ctx, "i-a", now)
	if len(accounts) != 1 {
		t.Fatal("disabled user remained active", accounts)
	}
}

func TestDCVStaleHeartbeatDeniesIssueAndConsume(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.CreateUser(ctx, "admin", "x", "portal_admin", ""); e != nil {
		t.Fatal(e)
	}
	admin, _ := s.UserByName(ctx, "admin")
	_, _ = s.DB.Exec(`INSERT INTO instances(instance_id,name,dcv_host) VALUES('i-a','A','a.example')`)
	if e = s.ConfigureDCV(ctx, admin, "i-a", "a.example", "native", strings.Repeat("a", 64)); e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	_ = s.DCVHeartbeat(ctx, "i-a", []int64{admin.ID}, "", now, 1, 1)
	if _, e = s.IssueDCVToken(ctx, admin, "i-a", "token", now.Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	_ = s.DCVHeartbeat(ctx, "i-a", []int64{admin.ID}, "", now.Add(-2*time.Minute), 1, 1)
	if _, e = s.IssueDCVToken(ctx, admin, "i-a", "token2", now.Add(time.Minute)); e == nil {
		t.Fatal("stale heartbeat permitted token")
	}
	if _, ok := s.ConsumeInstanceDCVToken(ctx, "token", DCVIdentity(admin.ID), "i-a", now); ok {
		t.Fatal("stale heartbeat permitted login")
	}
}
