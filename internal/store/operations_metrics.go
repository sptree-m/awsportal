package store

import (
	"context"
	"database/sql"
	"sort"
	"time"
)

const operationMetricSchema = `
CREATE TABLE IF NOT EXISTS operational_samples(instance_id INTEGER NOT NULL,observed_at INTEGER NOT NULL,reason TEXT NOT NULL,idle INTEGER NOT NULL,seconds INTEGER NOT NULL,idle_seconds INTEGER NOT NULL,PRIMARY KEY(instance_id,observed_at));
CREATE TABLE IF NOT EXISTS node_load_minutes(instance_id INTEGER NOT NULL,observed_at INTEGER NOT NULL,boot_id TEXT NOT NULL,cpu REAL NOT NULL,memory REAL NOT NULL,seconds INTEGER NOT NULL,PRIMARY KEY(instance_id,observed_at));
CREATE TABLE IF NOT EXISTS instance_readiness(instance_id INTEGER PRIMARY KEY,ready_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS interval_group_time ON connection_intervals(group_id,start_at,end_at);
CREATE INDEX IF NOT EXISTS interval_instance_time ON connection_intervals(instance_id,start_at,end_at);
`

type OperationalMetrics struct {
	SampleCount, MeasuredSeconds, IdleSeconds, BootCount int64
	BootAverage, BootP95                                 float64
	Reasons                                              map[string]int64
	ScaleReasons                                         map[string]int64
	LoadCount, LoadSeconds, TerminationCount             int64
	CPUAverage, CPUP95, MemoryAverage, MemoryP95         float64
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
	idleSeconds := int64(0)
	if idle && previousIdle {
		idleSeconds = seconds
	}
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO operational_samples VALUES(?,?,?,?,?,?)`, iid, slot, reason, idle, seconds, idleSeconds)
	return err
}
func (s *Store) OperationalMetrics(ctx context.Context, now time.Time) (OperationalMetrics, error) {
	var m OperationalMetrics
	m.Reasons = map[string]int64{}
	m.ScaleReasons = map[string]int64{}
	cutoff := now.AddDate(0, 0, -30).Unix()
	rows, err := s.DB.QueryContext(ctx, `SELECT reason,COUNT(*),COALESCE(SUM(seconds),0),COALESCE(SUM(idle_seconds),0) FROM operational_samples WHERE observed_at>=? GROUP BY reason`, cutoff)
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
	rows, err = s.DB.QueryContext(ctx, `SELECT cpu,memory,seconds FROM node_load_minutes WHERE observed_at>=? AND seconds>0`, cutoff)
	if err != nil {
		return m, err
	}
	var cpus, memories []float64
	for rows.Next() {
		var cpu, mem float64
		var seconds int64
		if err = rows.Scan(&cpu, &mem, &seconds); err != nil {
			rows.Close()
			return m, err
		}
		cpus = append(cpus, cpu)
		memories = append(memories, mem)
		m.CPUAverage += cpu * float64(seconds)
		m.MemoryAverage += mem * float64(seconds)
		m.LoadSeconds += seconds
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return m, err
	}
	m.LoadCount = int64(len(cpus))
	if m.LoadSeconds > 0 {
		m.CPUAverage /= float64(m.LoadSeconds)
		m.MemoryAverage /= float64(m.LoadSeconds)
		sort.Float64s(cpus)
		sort.Float64s(memories)
		rank := (95*len(cpus)+99)/100 - 1
		m.CPUP95 = cpus[rank]
		m.MemoryP95 = memories[rank]
	}
	rows, err = s.DB.QueryContext(ctx, `SELECT COALESCE(json_extract(input,'$.scale_reason'),'legacy/unrecorded'),COUNT(*) FROM cloud_operations WHERE kind='PROVISION' AND created_at>=? GROUP BY 1`, cutoff)
	if err != nil {
		return m, err
	}
	for rows.Next() {
		var reason string
		var count int64
		if err = rows.Scan(&reason, &count); err != nil {
			rows.Close()
			return m, err
		}
		m.ScaleReasons[reason] = count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return m, err
	}
	err = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM cloud_operations WHERE kind='TERMINATE' AND state='SUCCEEDED' AND updated_at>=?`, cutoff).Scan(&m.TerminationCount)
	return m, err
}

func recordLoadMinute(ctx context.Context, tx *sql.Tx, iid int64, r EnvironmentReport, now time.Time) error {
	slot := now.Unix() / 60 * 60
	var previous int64
	var boot string
	err := tx.QueryRowContext(ctx, `SELECT observed_at,boot_id FROM node_load_minutes WHERE instance_id=? ORDER BY observed_at DESC LIMIT 1`, iid).Scan(&previous, &boot)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	seconds := slot - previous
	if previous == 0 || boot != r.BootID || seconds < 0 || seconds > 90 {
		seconds = 0
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO node_load_minutes VALUES(?,?,?,?,?,?)`, iid, slot, r.BootID, r.CPU, r.Memory, seconds)
	return err
}
