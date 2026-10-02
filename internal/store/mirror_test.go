package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestMirrorPermissionsTokensQueueAndRecovery(t *testing.T) {
	ctx := context.Background()
	s, e := Open(filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	s.CreateUser(ctx, "alice", "x", "user", "")
	u, _ := s.UserByName(ctx, "alice")
	r := MirrorRepo{Name: "code", Upstream: "https://gitlab.example/team/repo.git", Branch: "main", Enabled: true}
	if e = s.SaveMirror(ctx, r); e != nil {
		t.Fatal(e)
	}
	if s.MirrorAccess(ctx, u, 1, false) {
		t.Fatal("unassigned read")
	}
	s.SetMirrorGrant(ctx, 1, "user", u.ID, false, false)
	if !s.MirrorAccess(ctx, u, 1, false) || s.MirrorAccess(ctx, u, 1, true) {
		t.Fatal("read-only scope")
	}
	s.DB.Exec(`INSERT INTO groups(id,name) VALUES(1,'dev');INSERT INTO group_members(user_id,group_id) VALUES(1,1)`)
	s.SetMirrorGrant(ctx, 1, "group", 1, true, false)
	if !s.MirrorAccess(ctx, u, 1, true) {
		t.Fatal("group sync")
	}
	token, e := s.IssueMirrorToken(ctx, u.ID, "build", 1, true)
	if e != nil {
		t.Fatal(e)
	}
	if user, sync, e := s.MirrorTokenUser(ctx, token); e != nil || user.ID != u.ID || !sync {
		t.Fatal(e)
	}
	var hash string
	s.DB.QueryRow("SELECT token_hash FROM mirror_tokens").Scan(&hash)
	if hash == token {
		t.Fatal("plaintext")
	}
	s.RevokeMirrorToken(ctx, 1)
	if _, _, e = s.MirrorTokenUser(ctx, token); e == nil {
		t.Fatal("revoked")
	}
	j, e := s.EnqueueMirror(ctx, 1, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	same, e := s.EnqueueMirror(ctx, 1, u.ID)
	if e != nil || same.ID != j.ID {
		t.Fatal("not coalesced")
	}
	j, e = s.ClaimMirrorJob(ctx)
	if e != nil || j.State != "running" {
		t.Fatal(e)
	}
	s.RecoverMirrorJobs(ctx)
	j, _ = s.MirrorJob(ctx, j.ID)
	if j.State != "error" {
		t.Fatal("restart")
	}
	if _, e = s.EnqueueMirror(ctx, 1, u.ID); e != ErrMirrorCooldown {
		t.Fatal("cooldown", e)
	}
	s.DB.Exec("UPDATE mirror_jobs SET created=?", time.Now().Add(-time.Minute).Unix())
	j, e = s.EnqueueMirror(ctx, 1, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	s.ClaimMirrorJob(ctx)
	s.FinishMirrorJob(ctx, j, 1, nil)
	repo, _ := s.Mirror(ctx, 1)
	if repo.LastSuccess == 0 {
		t.Fatal("success timestamp")
	}
	repo.Upstream = "https://other.example/a.git"
	if s.SaveMirror(ctx, repo) == nil {
		t.Fatal("source changed")
	}
	s.DB.Exec("DELETE FROM group_members")
	if s.MirrorAccess(ctx, u, 1, true) {
		t.Fatal("stale sync grant")
	}
	s.DB.Exec("UPDATE mirror_repos SET enabled=0")
	if s.MirrorAccess(ctx, u, 1, false) {
		t.Fatal("disabled repo")
	}
}

func TestMirrorAutomationActivityAndExpiry(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(filepath.Join(t.TempDir(), "db"))
	defer s.Close()
	s.Migrate(ctx)
	s.CreateUser(ctx, "task", "x", "user", "")
	u, _ := s.UserByName(ctx, "task")
	s.DB.Exec("UPDATE users SET expires_at=? WHERE id=?", time.Now().Add(time.Hour).Unix(), u.ID)
	u, _ = s.UserByName(ctx, "task")
	if e := s.RecordMirrorAccess(ctx, u); e != nil {
		t.Fatal(e)
	}
	fresh, _ := s.UserByName(ctx, "task")
	if fresh.ExpiresAt < time.Now().Add(29*24*time.Hour).Unix() || fresh.LastLoginAt != 0 {
		t.Fatal("activity was not kept separate from web login")
	}
	s.DB.Exec("UPDATE users SET expires_at=1 WHERE id=?", u.ID)
	u, _ = s.UserByName(ctx, "task")
	if s.RecordMirrorAccess(ctx, u) == nil {
		t.Fatal("expired account revived")
	}
	token, _ := s.IssueMirrorToken(ctx, u.ID, "task", 1, true)
	if _, _, e := s.MirrorTokenUser(ctx, token); e == nil {
		t.Fatal("expired account authenticated")
	}
}
