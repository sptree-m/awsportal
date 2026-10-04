package store

import (
	"context"
	"database/sql"
	"sort"
	"time"
)

const operationMetricSchema = `
CREATE TABLE IF NOT EXISTS operational_samples(instance_id INTEGER NOT NULL,observed_at INTEGER NOT NULL,reason TEXT NOT NULL,idle INTEGER NOT NULL,seconds INTEGER NOT NULL,PRIMARY KEY(instance_id,observed_at));
CREATE TABLE IF NOT EXISTS instance_readiness(instance_id INTEGER PRIMARY KEY,ready_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS interval_group_time ON connection_intervals(group_id,start_at,end_at);
CREATE INDEX IF NOT EXISTS interval_instance_time ON connection_intervals(instance_id,start_at,end_at);
`

type OperationalMetrics struct {
	SampleCount, MeasuredSeconds, IdleSeconds, BootCount int64
	BootAverage, BootP95                                 float64
	Reasons                                              map[string]int64
}

func recordOperationObservation(ctx context.Context, tx *sql.Tx, iid int64, reason string, idle bool, now time.Time) error {
	slot := now.Unix() / 60 * 60
	var previous int64
	var previousIdle bool
	var previousReason string
	if err := tx.QueryRowContext(ctx, `SELECT observed_at,idle,reason FROM operational_samples WHERE instance_id=? ORDER BY observed_at DESC LIMIT 1`, iid).Scan(&previous, &previousIdle, &previousReason); err != nil && err != sql.ErrNoRows {
		return err
	}
	seconds := slot - previous
	if previous == 0 || seconds > 90 || seconds < 0 || previousReason == "metrics unavailable or stale" || reason == "metrics unavailable or stale" {
		seconds = 0
	}
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO operational_samples VALUES(?,?,?,?,?)`, iid, slot, reason, idle && previousIdle, seconds)
	return err
}
func (s *Store) OperationalMetrics(ctx context.Context, now time.Time) (OperationalMetrics, error) {
	var m OperationalMetrics
	m.Reasons = map[string]int64{}
	cutoff := now.AddDate(0, 0, -30).Unix()
	rows, err := s.DB.QueryContext(ctx, `SELECT reason,COUNT(*),COALESCE(SUM(seconds),0),COALESCE(SUM(CASE WHEN idle=1 THEN seconds ELSE 0 END),0) FROM operational_samples WHERE observed_at>=? GROUP BY reason`, cutoff)
	if err != nil {
		return m, err
	}
	for rows.Next() {
		var reason string
		var n, seconds, idle int64
		if err = rows.Scan(&reason, &n, &seconds, &idle); err != nil {
			rows.Close()
			return m, err
		}
		m.Reasons[reason] = n
		m.SampleCount += n
		m.MeasuredSeconds += seconds
		m.IdleSeconds += idle
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return m, err
	}
	rows, err = s.DB.QueryContext(ctx, `SELECT r.ready_at-o.created_at FROM instance_readiness r JOIN dynamic_instances d ON d.instance_id=r.instance_id JOIN cloud_operations o ON o.id=d.operation_id WHERE r.ready_at>=? AND r.ready_at>=o.created_at`, cutoff)
	if err != nil {
		return m, err
	}
	var times []int64
	for rows.Next() {
		var n int64
		if err = rows.Scan(&n); err != nil {
			rows.Close()
			return m, err
		}
		times = append(times, n)
		m.BootAverage += float64(n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return m, err
	}
	m.BootCount = int64(len(times))
	if len(times) > 0 {
		sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
		m.BootAverage /= float64(len(times))
		m.BootP95 = float64(times[(95*len(times)+99)/100-1])
	}
	return m, nil
}
