package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tokenFixture(t *testing.T) (*Store, User) {
	t.Helper()
	ctx := context.Background()
	s, e := Open(filepath.Join(t.TempDir(), "t.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	_, e = s.DB.Exec(`INSERT INTO users(id,username,password_hash,role) VALUES(1,'user01','x','user');INSERT INTO instances(id,instance_id,name,dcv_host,dcv_session_id) VALUES(1,'i-1','dev','dev.local','console');INSERT INTO instance_users VALUES(1,1,1);`)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.ConfigureDCV(ctx, User{Role: "portal_admin"}, "i-1", "dev.local", "native", strings.Repeat("a", 64)); e != nil {
		t.Fatal(e)
	}
	if e = s.DCVHeartbeat(ctx, "i-1", []int64{1}, "", time.Now(), 1, 1); e != nil {
		t.Fatal(e)
	}
	return s, User{ID: 1, Username: "user01", Role: "user", Enabled: true}
}
func TestDCVTokenOneTime(t *testing.T) {
	s, u := tokenFixture(t)
	ctx := context.Background()
	if _, e := s.IssueDCVToken(ctx, u, "i-1", "token", time.Now().Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	if _, ok := s.ConsumeDCVToken(ctx, "token", DCVIdentity(1), time.Now()); ok {
		t.Fatal("legacy authentication bypass")
	}
	name, ok := s.ConsumeInstanceDCVToken(ctx, "token", DCVIdentity(1), "i-1", time.Now())
	if !ok || name != DCVIdentity(1) {
		t.Fatal("first use rejected")
	}
	if _, ok = s.ConsumeInstanceDCVToken(ctx, "token", DCVIdentity(1), "i-1", time.Now()); ok {
		t.Fatal("token reused")
	}
}
func TestDCVTokenExpired(t *testing.T) {
	s, u := tokenFixture(t)
	ctx := context.Background()
	if _, e := s.IssueDCVToken(ctx, u, "i-1", "expired", time.Now().Add(-time.Second)); e != nil {
		t.Fatal(e)
	}
	if _, ok := s.ConsumeInstanceDCVToken(ctx, "expired", DCVIdentity(1), "i-1", time.Now()); ok {
		t.Fatal("expired token accepted")
	}
}
func TestDCVBrowserEnforcementRequired(t *testing.T) {
	s, u := tokenFixture(t)
	ctx := context.Background()
	if e := s.ConfigureDCV(ctx, User{Role: "portal_admin"}, "i-1", "dev.local", "web", strings.Repeat("b", 64)); e == nil {
		t.Fatal("web mode accepted")
	}
	if e := s.DCVHeartbeat(ctx, "i-1", []int64{1}, "", time.Now(), 1); e != nil {
		t.Fatal(e)
	}
	if _, e := s.IssueDCVToken(ctx, u, "i-1", "legacy-agent", time.Now().Add(time.Minute)); e == nil {
		t.Fatal("old agent allowed")
	}
	if e := s.DCVHeartbeat(ctx, "i-1", []int64{1}, "", time.Now(), 1, 1); e != nil {
		t.Fatal(e)
	}
	if _, e := s.IssueDCVToken(ctx, u, "i-1", "ready", time.Now().Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	if e := s.DCVHeartbeat(ctx, "i-1", []int64{1}, "", time.Now(), 1, 0); e != nil {
		t.Fatal(e)
	}
	if _, ok := s.ConsumeInstanceDCVToken(ctx, "ready", DCVIdentity(1), "i-1", time.Now()); ok {
		t.Fatal("browser protection lost but token accepted")
	}
}
