package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestInstanceAdministrationRevocation(t *testing.T) {
	ctx := context.Background()
	s, e := Open(filepath.Join(t.TempDir(), "portal.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	_, e = s.DB.Exec(`INSERT INTO users(id,username,password_hash,role) VALUES(1,'alice','x','user'),(2,'admin','x','portal_admin'); INSERT INTO instances(id,instance_id,name,dcv_host) VALUES(1,'i-dev','dev','host'); INSERT INTO groups(id,name) VALUES(1,'readers'),(2,'operators'); INSERT INTO group_members VALUES(1,1),(1,2); INSERT INTO instance_groups VALUES(1,1,0),(1,2,1);`)
	if e != nil {
		t.Fatal(e)
	}
	alice, _ := s.UserByName(ctx, "alice")
	admin, _ := s.UserByName(ctx, "admin")
	xs, e := s.VisibleInstances(ctx, alice)
	if e != nil || len(xs) != 1 || !xs[0].CanControl {
		t.Fatalf("multiple grants must combine without duplicate rows: %v %+v", e, xs)
	}
	if e = s.SetInstanceEnabled(ctx, alice, "i-dev", false); e == nil {
		t.Fatal("user disabled instance")
	}
	if e = s.AddSchedule(ctx, alice, "i-dev", "start", "09:00", "1,2,3,4,5", "Asia/Tokyo"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.IssueDCVToken(ctx, alice, "i-dev", "token", time.Now().Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	if e = s.SetInstanceEnabled(ctx, admin, "i-dev", false); e != nil {
		t.Fatal(e)
	}
	for _, u := range []User{alice, admin} {
		if s.CanControl(ctx, u, "i-dev") {
			t.Fatal("disabled instance controllable")
		}
		xs, e = s.VisibleInstances(ctx, u)
		if e != nil || len(xs) != 0 {
			t.Fatal("disabled instance visible")
		}
	}
	if _, ok := s.ConsumeDCVToken(ctx, "token", "console", time.Now()); ok {
		t.Fatal("disabled token accepted")
	}
	due, e := s.DueSchedules(ctx, time.Now())
	if e != nil || len(due) != 0 {
		t.Fatalf("disabled instance scheduled: %v", e)
	}
	all, e := s.ManagedInstances(ctx)
	if e != nil || len(all) != 1 || all[0].Enabled {
		t.Fatal("admin cannot inspect disabled instance")
	}
	if e = s.SetInstanceEnabled(ctx, admin, "i-dev", true); e != nil {
		t.Fatal(e)
	}
	if _, e = s.IssueDCVToken(ctx, alice, "i-dev", "revoked", time.Now().Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	for _, gid := range []int64{1, 2} {
		if e = s.SetGroupMember(ctx, admin, gid, alice.ID, true); e != nil {
			t.Fatal(e)
		}
	}
	if _, ok := s.ConsumeDCVToken(ctx, "revoked", "console", time.Now()); ok {
		t.Fatal("assignment revoked after issuance, token accepted")
	}
	due, e = s.DueSchedules(ctx, time.Now())
	if e != nil || len(due) != 0 {
		t.Fatal("removed member schedule allowed")
	}
	if e = s.SetAssignment(ctx, admin, "i-dev", "user", alice.ID, false, false); e != nil {
		t.Fatal(e)
	}
	xs, e = s.VisibleInstances(ctx, alice)
	if e != nil || len(xs) != 1 || xs[0].CanControl {
		t.Fatal("view grant invalid")
	}
	if e = s.SetAssignment(ctx, admin, "i-dev", "user", alice.ID, true, false); e != nil || !s.CanControl(ctx, alice, "i-dev") {
		t.Fatal("grant update failed")
	}
	if e = s.SetAssignment(ctx, admin, "i-dev", "user", 999, true, false); e == nil {
		t.Fatal("nonexistent subject accepted")
	}
	if e = s.SetAssignment(ctx, alice, "i-dev", "group", 1, true, false); e == nil {
		t.Fatal("user assigned group")
	}
	if e = s.CreateGroup(ctx, alice, "illegal"); e == nil {
		t.Fatal("user created group")
	}
	if e = s.CreateGroup(ctx, admin, "New Team"); e != nil {
		t.Fatal(e)
	}
}
