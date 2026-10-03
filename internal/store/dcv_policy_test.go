package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDCVPolicyAdministratorValidationAndRevisionGate(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	for _, role := range []string{"portal_admin", "user", "group_admin"} {
		if e = s.CreateUser(ctx, role, "x", role, ""); e != nil {
			t.Fatal(e)
		}
	}
	admin, _ := s.UserByName(ctx, "portal_admin")
	user, _ := s.UserByName(ctx, "user")
	manager, _ := s.UserByName(ctx, "group_admin")
	_, e = s.DB.Exec(`INSERT INTO instances(instance_id,name,dcv_host) VALUES('i-a','A','host'),('i-b','B','host')`)
	if e != nil {
		t.Fatal(e)
	}
	for _, u := range []User{user, manager} {
		if s.SetDCVPolicy(ctx, u, "i-a", []string{"display"}) == nil {
			t.Fatal("non-admin changed DCV policy")
		}
	}
	for _, features := range [][]string{{"builtin"}, {"bad\n%any% allow builtin"}, {"clipboard-copy"}, {"keyboard-sas"}, {"display", "display"}, {"unsupervised-access"}} {
		if s.SetDCVPolicy(ctx, admin, "i-a", features) == nil {
			t.Fatal("unsafe features accepted", features)
		}
	}
	if e = s.ConfigureDCV(ctx, admin, "i-a", "host", "web", strings.Repeat("a", 64)); e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	s.DCVHeartbeat(ctx, "i-a", []int64{admin.ID}, "", now)
	if _, e = s.IssueDCVToken(ctx, admin, "i-a", "old-agent", now.Add(time.Minute)); e == nil {
		t.Fatal("legacy agent bypassed policy gate")
	}
	s.DCVHeartbeat(ctx, "i-a", []int64{admin.ID}, "", now, 1)
	if _, e = s.IssueDCVToken(ctx, admin, "i-a", "before", now.Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	if e = s.SetDCVPolicy(ctx, admin, "i-a", []string{"display"}); e != nil {
		t.Fatal(e)
	}
	if _, ok := s.ConsumeInstanceDCVToken(ctx, "before", DCVIdentity(admin.ID), "i-a", now); ok {
		t.Fatal("old token survived policy change")
	}
	if _, e = s.IssueDCVToken(ctx, admin, "i-a", "pending", now.Add(time.Minute)); e == nil {
		t.Fatal("pending policy connection allowed")
	}
	s.DCVHeartbeat(ctx, "i-a", []int64{admin.ID}, "", now, 1)
	if _, e = s.IssueDCVToken(ctx, admin, "i-a", "stale", now.Add(time.Minute)); e == nil {
		t.Fatal("stale applied revision accepted")
	}
	s.DCVHeartbeat(ctx, "i-a", []int64{admin.ID}, "", now, 2)
	if _, e = s.IssueDCVToken(ctx, admin, "i-a", "ready", now.Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	_ = s.DCVHeartbeat(ctx, "i-a", []int64{admin.ID}, "", now, 1)
	if _, ok := s.ConsumeInstanceDCVToken(ctx, "ready", DCVIdentity(admin.ID), "i-a", now); ok {
		t.Fatal("token accepted after policy acknowledgment became stale")
	}
	_ = s.DCVHeartbeat(ctx, "i-a", []int64{admin.ID}, "", now, 2)
	policy, e := s.DCVPolicy(ctx, "i-b")
	if e != nil || policy.Revision != 1 || len(policy.Allowed) != 5 {
		t.Fatal("other instance changed", policy, e)
	}
	if e = s.SetDCVPolicy(ctx, admin, "i-a", []string{"screenshot", "clipboard-copy", "display"}); e != nil {
		t.Fatal(e)
	}
	xs, e := s.ManagedInstances(ctx)
	if e != nil || len(xs) != 2 || xs[0].DCVPolicy.Revision != 3 {
		t.Fatal(xs, e)
	}
}
