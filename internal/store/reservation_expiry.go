package store

import (
	"context"
	"database/sql"
	"time"
)

const reservationSchema = `CREATE TABLE IF NOT EXISTS assignment_timing(assignment_id INTEGER PRIMARY KEY REFERENCES environment_assignments(id),ready_at INTEGER NOT NULL DEFAULT 0,ever_connected INTEGER NOT NULL DEFAULT 0,expired INTEGER NOT NULL DEFAULT 0);`

func reconcileReservationExpiry(ctx context.Context, tx *sql.Tx, id int64, next, request string, w UserWork, supported bool, now time.Time) (string, string, error) {
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO assignment_timing(assignment_id) VALUES(?)`, id); err != nil {
		return next, request, err
	}
	var ready int64
	var connected, expired bool
	if err := tx.QueryRowContext(ctx, `SELECT ready_at,ever_connected,expired FROM assignment_timing WHERE assignment_id=?`, id).Scan(&ready, &connected, &expired); err != nil {
		return next, request, err
	}
	if w.Connected || next == "DISCONNECTED_GRACE" || next == "JOB_HELD" {
		connected = true
	}
	if next == "READY" && ready == 0 {
		ready = now.Unix()
	}
	if expired && next == "RELEASED" {
		request = "TIMED_OUT"
	}
	// A late connection/job cancels automatic cleanup; the live work stays held.
	if expired && (w.Connected || w.Jobs > 0 || w.Unclassified) {
		expired = false
		next = "JOB_HELD"
		request = "READY"
		if w.Connected {
			next = "CONNECTED"
			request = "CONNECTED"
		}
	}
	if supported && next == "READY" && !connected && !w.Connected && w.Jobs == 0 && !w.Unclassified && ready > 0 && ready <= now.Add(-120*time.Second).Unix() {
		var tokenActive bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM dcv_tokens t JOIN environment_assignments a ON a.instance_id=t.instance_id AND a.user_id=t.user_id WHERE a.id=? AND t.expires_at>=?)`, id, now.Add(-90*time.Second).Unix()).Scan(&tokenActive); err != nil {
			return next, request, err
		}
		if !tokenActive {
			expired = true
			next = "RELEASING"
			request = "RELEASING"
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE assignment_timing SET ready_at=?,ever_connected=?,expired=? WHERE assignment_id=?`, ready, connected, expired, id)
	return next, request, err
}
