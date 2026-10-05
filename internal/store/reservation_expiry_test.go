package store

import (
	"context"
	"testing"
	"time"
)

func TestReservationExpiryPreservesWorkTokensAndLease(t *testing.T) {
	s, admin, users, eid, now := sharedFixture(t)
	ctx := context.Background()
	req, err := s.RequestEnvironment(ctx, users[0], eid, "expiry-test", now)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := UserWork{UserID: users[0].ID, HomeMounted: true, SessionPresent: true}
	for _, check := range []struct {
		offset    time.Duration
		supports  bool
		connected bool
		jobs      int
		expected  string
	}{
		{0, true, false, 0, "READY"}, {130 * time.Second, false, false, 0, "READY"}, {140 * time.Second, true, false, 1, "READY"}, {150 * time.Second, true, false, 0, "RELEASING"},
	} {
		w.Connected = check.connected
		w.Jobs = check.jobs
		next, _, e := reconcileReservationExpiry(ctx, tx, req.AssignmentID, "READY", "READY", w, check.supports, now.Add(check.offset))
		if e != nil || next != check.expected {
			t.Fatalf("next=%s err=%v check=%+v", next, e, check)
		}
	}
	// Late work cancels automatic cleanup and preserves the existing assignment.
	w.Jobs = 1
	next, _, err := reconcileReservationExpiry(ctx, tx, req.AssignmentID, "RELEASING", "RELEASING", w, true, now.Add(160*time.Second))
	if err != nil || next != "JOB_HELD" {
		t.Fatal(next, err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = s.SetUserStorage(ctx, admin, users[0].ID, "fs-12345678", "fsap-12345678", "cannot change mounted lease"); err == nil {
		t.Fatal("lease released without root cleanup")
	}
	tx, err = s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	w.Jobs = 0
	w.Connected = true
	next, _, err = reconcileReservationExpiry(ctx, tx, req.AssignmentID, "CONNECTED", "CONNECTED", w, true, now.Add(170*time.Second))
	if err != nil || next != "CONNECTED" {
		t.Fatal(next, err)
	}
	w.Connected = false
	next, _, err = reconcileReservationExpiry(ctx, tx, req.AssignmentID, "READY", "READY", w, true, now.Add(time.Hour))
	if err != nil || next != "READY" {
		t.Fatal("previously connected work expired", next, err)
	}
}
func TestReservationExpiryWaitsForRecentDCVToken(t *testing.T) {
	s, _, users, eid, now := sharedFixture(t)
	ctx := context.Background()
	req, err := s.RequestEnvironment(ctx, users[0], eid, "token-expiry-test", now)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	w := UserWork{UserID: users[0].ID, HomeMounted: true, SessionPresent: true}
	if _, _, err = reconcileReservationExpiry(ctx, tx, req.AssignmentID, "READY", "READY", w, true, now); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO dcv_tokens(token_hash,user_id,instance_id,session_id,expires_at) VALUES('expiry-token',?,1,'session',?)`, users[0].ID, now.Add(150*time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	next, _, err := reconcileReservationExpiry(ctx, tx, req.AssignmentID, "READY", "READY", w, true, now.Add(130*time.Second))
	if err != nil || next != "READY" {
		t.Fatal(next, err)
	}
	next, _, err = reconcileReservationExpiry(ctx, tx, req.AssignmentID, "READY", "READY", w, true, now.Add(250*time.Second))
	if err != nil || next != "RELEASING" {
		t.Fatal(next, err)
	}
	w.HomeMounted = false
	w.SessionPresent = false
	_, state, err := reconcileReservationExpiry(ctx, tx, req.AssignmentID, "RELEASED", "RELEASED", w, true, now.Add(260*time.Second))
	if err != nil || state != "TIMED_OUT" {
		t.Fatal(state, err)
	}
}
