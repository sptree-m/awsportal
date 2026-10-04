package store

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func sharedFixture(t *testing.T) (*Store, User, []User, int64, time.Time) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateUser(ctx, "admin", "x", "portal_admin", ""); err != nil {
		t.Fatal(err)
	}
	admin, _ := s.UserByName(ctx, "admin")
	users := []User{}
	for _, n := range []string{"alice", "bob", "carol"} {
		if err = s.CreateUser(ctx, n, "x", "user", ""); err != nil {
			t.Fatal(err)
		}
		u, _ := s.UserByName(ctx, n)
		users = append(users, u)
	}
	_, err = s.DB.Exec(`INSERT INTO groups(id,name) VALUES(1,'team');INSERT INTO instances(instance_id,name,dcv_host) VALUES('i-shared','Shared','shared.example');`)
	if err != nil {
		t.Fatal(err)
	}
	eid, err := s.CreateEnvironment(ctx, admin, Environment{Name: "Analysis", Mode: "shared", GroupID: 1, ProfileID: "shared-cpu-v1"}, "pilot")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RegisterEnvironmentInstance(ctx, admin, eid, "i-shared", "pilot"); err != nil {
		t.Fatal(err)
	}
	if err = s.ConfigureDCV(ctx, admin, "i-shared", "shared.example", "native", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		if err = s.SetEnvironmentACL(ctx, admin, eid, "user", u.ID, "environment.connect", false, "pilot"); err != nil {
			t.Fatal(err)
		}
		if err = s.SetUserStorage(ctx, admin, u.ID, "fs-"+strings.Repeat(string('a'+rune(u.ID)), 8), "fsap-"+strings.Repeat(string('a'+rune(u.ID)), 8), "migration verified"); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().Truncate(time.Second)
	for i := int64(1); i <= 6; i++ {
		r := sample(i, now.Add(time.Duration(i-6)*time.Minute))
		if err = s.EnvironmentHeartbeat(ctx, "i-shared", r, now.Add(time.Duration(i-6)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	return s, admin, users, eid, now
}
func sample(seq int64, now time.Time) EnvironmentReport {
	return EnvironmentReport{AgentVersion: 2, Generation: 1, BootID: "boot-pilot-0001", Sequence: seq, ObservedAt: now.Unix(), CPU: 10, Memory: 20, MetricsValid: true, AppliedRevision: 1, BrowserBlocked: true, Work: []UserWork{}, ReadyUsers: []int64{}}
}
func TestSharedConcurrentReservationIdempotencyAndRestart(t *testing.T) {
	s, _, users, eid, now := sharedFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	out := make(chan ConnectionRequest, 3)
	errs := make(chan error, 3)
	for _, u := range users {
		wg.Add(1)
		go func(u User) {
			defer wg.Done()
			r, err := s.RequestEnvironment(ctx, u, eid, "request-key", now)
			out <- r
			errs <- err
		}(u)
	}
	wg.Wait()
	close(out)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	assigned, waiting := 0, 0
	for r := range out {
		if r.State == "PREPARING_USER" {
			assigned++
		} else if r.State == "WAITING" {
			waiting++
		} else {
			t.Fatal(r)
		}
	}
	if assigned != 2 || waiting != 1 {
		t.Fatal(assigned, waiting)
	}
	r, err := s.RequestEnvironment(ctx, users[0], eid, "request-key", now)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := s.RequestEnvironment(ctx, users[0], eid, "different-key", now)
	if err != nil || r.ID != r2.ID {
		t.Fatal(r, r2, err)
	}
	var dbIndex int
	var dbName, dbPath string
	if err = s.DB.QueryRow(`PRAGMA database_list`).Scan(&dbIndex, &dbName, &dbPath); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	s.DB = reopened.DB
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileEnvironments(ctx, now); err != nil {
		t.Fatal(err)
	}
	var count int
	s.DB.QueryRow(`SELECT COUNT(*) FROM environment_assignments WHERE state!='RELEASED'`).Scan(&count)
	if count != 2 {
		t.Fatal(count)
	}
}
func TestSharedACLAndLegacyBypass(t *testing.T) {
	s, admin, users, eid, now := sharedFixture(t)
	ctx := context.Background()
	u := users[0]
	if s.CanControl(ctx, admin, "i-shared") || s.CanControl(ctx, u, "i-shared") {
		t.Fatal("Shared power bypass")
	}
	if err := s.AddSchedule(ctx, admin, "i-shared", "stop", "12:00", "1", "UTC"); err == nil {
		t.Fatal("Shared schedule bypass")
	}
	accounts, err := s.DCVAccounts(ctx, "i-shared", now)
	if err != nil || len(accounts) != 0 {
		t.Fatal(accounts, err)
	}
	if _, err = s.RequestEnvironment(ctx, User{ID: 999, Role: "portal_admin"}, eid, "request-key", now); err == nil {
		t.Fatal("forged role")
	}
	if err = s.SetEnvironmentACL(ctx, admin, eid, "user", u.ID, "environment.connect", true, "revoke"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RequestEnvironment(ctx, u, eid, "request-key", now); err == nil {
		t.Fatal("revoked ACL")
	}
	if _, err = s.CreateEnvironment(ctx, u, Environment{}, "x"); err == nil {
		t.Fatal("user admin API")
	}
}
func TestSharedReadinessReleaseAndTokenRevocation(t *testing.T) {
	s, admin, users, eid, now := sharedFixture(t)
	ctx := context.Background()
	u := users[0]
	r, err := s.RequestEnvironment(ctx, u, eid, "request-key", now)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := s.DCVAccounts(ctx, "i-shared", now)
	if err != nil || len(accounts) != 1 || accounts[0].Home == nil || accounts[0].Home.UID != 200000+u.ID {
		t.Fatal(accounts, err)
	}
	if _, err = s.IssueDCVToken(ctx, u, "i-shared", "before-ready", now.Add(time.Minute)); err == nil {
		t.Fatal("token before HOME/session proof")
	}
	report := sample(7, now)
	report.Work = []UserWork{{UserID: u.ID, HomeMounted: true, SessionPresent: true, StorageRevision: 1}}
	report.ReadyUsers = []int64{u.ID}
	if err = s.EnvironmentHeartbeat(ctx, "i-shared", report, now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.IssueDCVToken(ctx, u, "i-shared", "ready-token", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if name, ok := s.ConsumeInstanceDCVToken(ctx, "ready-token", DCVIdentity(u.ID), "i-shared", now); !ok || name != DCVIdentity(u.ID) {
		t.Fatal(name, ok)
	}
	if _, err = s.IssueDCVToken(ctx, u, "i-shared", "release-token", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	report.Sequence++
	report.Work[0].Jobs = 1
	if err = s.EnvironmentHeartbeat(ctx, "i-shared", report, now); err != nil {
		t.Fatal(err)
	}
	if err = s.EndEnvironmentRequest(ctx, u, r.ID, false, now); err == nil {
		t.Fatal("active job released")
	}
	report.Sequence++
	report.Work[0].Jobs = 0
	if err = s.EnvironmentHeartbeat(ctx, "i-shared", report, now); err != nil {
		t.Fatal(err)
	}
	if err = s.EndEnvironmentRequest(ctx, u, r.ID, false, now); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.ConsumeInstanceDCVToken(ctx, "release-token", DCVIdentity(u.ID), "i-shared", now); ok {
		t.Fatal("released token authorized")
	}
	var held int
	s.DB.QueryRow(`SELECT COUNT(*) FROM environment_assignments WHERE state!='RELEASED'`).Scan(&held)
	if held != 1 {
		t.Fatal("lease freed before acknowledgement")
	}
	if err = s.SetUserStorage(ctx, admin, u.ID, "fs-12345678", "fsap-12345678", "migration"); err == nil {
		t.Fatal("active HOME changed")
	}
	report.Sequence++
	report.ReadyUsers = []int64{}
	report.Work[0].SessionPresent = false
	report.ClosedAssignments = []int64{r.AssignmentID}
	if err = s.EnvironmentHeartbeat(ctx, "i-shared", report, now); err != nil {
		t.Fatal(err)
	}
	got, err := s.EnvironmentRequest(ctx, u, r.ID)
	if err != nil || got.State != "RELEASED" {
		t.Fatal(got, err)
	}
	if _, err = s.EnvironmentRequest(ctx, users[1], r.ID); err == nil {
		t.Fatal("other user read request")
	}
}
func TestSharedReplayFreshnessLoadAndIdleProtection(t *testing.T) {
	s, _, users, eid, now := sharedFixture(t)
	ctx := context.Background()
	r := sample(6, now)
	if err := s.EnvironmentHeartbeat(ctx, "i-shared", r, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var received int64
	s.DB.QueryRow(`SELECT received_at FROM instance_observations`).Scan(&received)
	if received != now.Unix() {
		t.Fatal("retry renewed freshness")
	}
	r.CPU = 99
	if err := s.EnvironmentHeartbeat(ctx, "i-shared", r, now); err == nil {
		t.Fatal("changed replay accepted")
	}
	if _, err := s.RecordIdleDecisions(ctx, now); err != nil {
		t.Fatal(err)
	}
	decisions, err := s.RecordIdleDecisions(ctx, now.Add(16*time.Minute))
	if err != nil || len(decisions) != 1 || decisions[0].Candidate || decisions[0].Since != 0 {
		t.Fatal("stale metrics became idle candidate", decisions, err)
	}
	req, err := s.RequestEnvironment(ctx, users[0], eid, "request-key", now.Add(2*time.Minute))
	if err != nil || req.State != "WAITING" {
		t.Fatal(req, err)
	}
	// Exactly 70 is ineligible, including when the CPU threshold alone is met.
	for i := int64(7); i <= 12; i++ {
		at := now.Add(time.Duration(i-6) * time.Minute)
		r = sample(i, at)
		r.CPU = 70
		if err = s.EnvironmentHeartbeat(ctx, "i-shared", r, at); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.ReconcileEnvironments(ctx, now.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	req, err = s.EnvironmentRequest(ctx, users[0], req.ID)
	if err != nil || req.State != "WAITING" || !strings.Contains(req.Reason, "70%") {
		t.Fatal(req, err)
	}
}

func TestSharedBootResetWarmupAndPolicyChange(t *testing.T) {
	s, admin, users, eid, now := sharedFixture(t)
	ctx := context.Background()
	r := sample(1, now.Add(time.Second))
	r.BootID = "boot-new-0002"
	if err := s.EnvironmentHeartbeat(ctx, "i-shared", r, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	req, err := s.RequestEnvironment(ctx, users[0], eid, "request-key", now.Add(time.Second))
	if err != nil || req.State != "WAITING" {
		t.Fatal(req, err)
	}
	r.Sequence = 2
	r.ObservedAt = now.Add(61 * time.Second).Unix()
	if err = s.EnvironmentHeartbeat(ctx, "i-shared", r, now.Add(61*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileEnvironments(ctx, now.Add(61*time.Second)); err != nil {
		t.Fatal(err)
	}
	req, err = s.EnvironmentRequest(ctx, users[0], req.ID)
	if err != nil || req.State != "PREPARING_USER" {
		t.Fatal(req, err)
	}
	if err = s.SetDCVPolicy(ctx, admin, "i-shared", []string{"display"}); err == nil {
		t.Fatal("Shared policy changed with occupied seat")
	}
	old := sample(7, now.Add(62*time.Second))
	if err = s.EnvironmentHeartbeat(ctx, "i-shared", old, now.Add(62*time.Second)); err == nil {
		t.Fatal("retired boot accepted")
	}
	r.Sequence = 3
	r.Generation = 2
	r.ObservedAt = now.Add(62 * time.Second).Unix()
	if err = s.EnvironmentHeartbeat(ctx, "i-shared", r, now.Add(62*time.Second)); err == nil {
		t.Fatal("wrong generation accepted")
	}
}
