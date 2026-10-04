package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLegacyMetricReplayAcrossJobMigration(t *testing.T) {
	s, _, _, _, now := sharedFixture(t)
	ctx := context.Background()
	// Stored v2 reports from PR #38 do not contain the optional job extension.
	if _, err := s.DB.Exec(`UPDATE instance_samples SET payload=json_remove(payload,'$.measurement_version','$.job_measurements'); UPDATE instance_observations SET payload=json_remove(payload,'$.measurement_version','$.job_measurements')`); err != nil {
		t.Fatal(err)
	}
	r := sample(6, now)
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), "measurement_version") || strings.Contains(string(raw), "job_measurements") {
		t.Fatal("legacy wire format changed")
	}
	if err := s.EnvironmentHeartbeat(ctx, "i-shared", r, now.Add(time.Second)); err != nil {
		t.Fatal("legacy retry rejected after migration", err)
	}
	var received int64
	s.DB.QueryRow(`SELECT received_at FROM instance_observations`).Scan(&received)
	if received != now.Unix() {
		t.Fatal("retry renewed sample freshness")
	}
}

func TestJobMetricsLedgerReplayResetAndOwnerBoundary(t *testing.T) {
	s, admin, users, eid, now := sharedFixture(t)
	ctx := context.Background()
	if _, err := s.RequestEnvironment(ctx, users[0], eid, "job-test", now); err != nil {
		t.Fatal(err)
	}
	j := JobMeasurement{JobID: strings.Repeat("a", 32), UserID: users[0].ID, BootID: "boot-pilot-0001", State: "RUNNING", StartedAt: now.Unix(), Quality: "ok", CounterEpoch: "boot-pilot-0001:123", CPUUsec: 100, MemoryBytes: 4096, ReadBytes: 20, WriteBytes: 10}
	report := func(seq int64, jobs []JobMeasurement) EnvironmentReport {
		r := sample(seq, now.Add(time.Duration(seq-6)*30*time.Second))
		r.MeasurementVersion = 1
		r.JobMeasurements = jobs
		r.Work = []UserWork{{UserID: users[0].ID, Jobs: 1}}
		return r
	}
	r := report(7, []JobMeasurement{j})
	at := time.Unix(r.ObservedAt, 0)
	if err := s.EnvironmentHeartbeat(ctx, "i-shared", r, at); err != nil {
		t.Fatal(err)
	}
	if err := s.EnvironmentHeartbeat(ctx, "i-shared", r, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var count int
	s.DB.QueryRow(`SELECT COUNT(*) FROM job_samples`).Scan(&count)
	if count != 1 {
		t.Fatal("replay duplicated job sample", count)
	}
	rows, err := s.VisibleManagedJobs(ctx, users[1], at)
	if err != nil || len(rows) != 0 {
		t.Fatal("another user can see job", err, rows)
	}
	rows, err = s.VisibleManagedJobs(ctx, admin, at.Add(91*time.Second))
	if err != nil || len(rows) != 1 || !rows[0].Stale {
		t.Fatal("stale active job not shown", err, rows)
	}
	j.CPUUsec = 150
	j.ReadBytes = 30
	j.WriteBytes = 15
	r = report(8, []JobMeasurement{j})
	if err = s.EnvironmentHeartbeat(ctx, "i-shared", r, time.Unix(r.ObservedAt, 0)); err != nil {
		t.Fatal(err)
	}
	var delta sql.NullInt64
	s.DB.QueryRow(`SELECT cpu_delta FROM job_samples WHERE sequence=8`).Scan(&delta)
	if !delta.Valid || delta.Int64 != 50 {
		t.Fatal(delta)
	}
	j.CPUUsec = 2
	j.ReadBytes = 1
	j.WriteBytes = 0
	r = report(9, []JobMeasurement{j})
	if err = s.EnvironmentHeartbeat(ctx, "i-shared", r, time.Unix(r.ObservedAt, 0)); err != nil {
		t.Fatal(err)
	}
	var quality string
	s.DB.QueryRow(`SELECT quality,cpu_delta FROM job_samples WHERE sequence=9`).Scan(&quality, &delta)
	if quality != "counter_reset" || delta.Valid {
		t.Fatal("reset became usage", quality, delta)
	}
	// Missing active jobs and changed identity fail the whole transaction.
	for _, bad := range []EnvironmentReport{report(10, nil), report(10, []JobMeasurement{func() JobMeasurement { x := j; x.UserID = users[1].ID; return x }()})} {
		if err = s.EnvironmentHeartbeat(ctx, "i-shared", bad, time.Unix(bad.ObservedAt, 0)); err == nil {
			t.Fatal("unsafe job report accepted")
		}
	}
	code := 0
	j.State = "SUCCEEDED"
	j.ExitCode = &code
	j.EndedAt = report(10, nil).ObservedAt
	r = report(10, []JobMeasurement{j})
	r.Work[0].Jobs = 0
	if err = s.EnvironmentHeartbeat(ctx, "i-shared", r, time.Unix(r.ObservedAt, 0)); err != nil {
		t.Fatal(err)
	}
	rows, err = s.VisibleManagedJobs(ctx, users[0], time.Unix(r.ObservedAt+120, 0))
	if err != nil || len(rows) != 1 || rows[0].State != "SUCCEEDED" || rows[0].Stale {
		t.Fatal(rows, err)
	}
	// Complete payload is also in the durable S3 queue.
	var raw string
	s.DB.QueryRow(`SELECT payload FROM usage_export_queue WHERE object_key LIKE '%00000000000000000010.json'`).Scan(&raw)
	if !strings.Contains(raw, j.JobID) {
		t.Fatal("job missing from export")
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal("migration not repeatable", err)
	}
}

func TestJobMissingProtectionUnassignedAndGap(t *testing.T) {
	s, _, users, eid, now := sharedFixture(t)
	ctx := context.Background()
	j := JobMeasurement{JobID: strings.Repeat("b", 32), UserID: users[0].ID, BootID: "boot-pilot-0001", State: "UNKNOWN", StartedAt: now.Unix(), Quality: "unavailable", CounterEpoch: "boot-pilot-0001:1"}
	r := sample(7, now)
	r.MeasurementVersion = 1
	r.JobMeasurements = []JobMeasurement{j}
	if err := s.EnvironmentHeartbeat(ctx, "i-shared", r, now); err == nil {
		t.Fatal("job omitted from safety fields")
	}
	r.Work = []UserWork{{UserID: j.UserID, Jobs: 1, Unclassified: true}}
	if err := s.EnvironmentHeartbeat(ctx, "i-shared", r, now); err == nil {
		t.Fatal("unassigned job accepted")
	}
	if _, err := s.RequestEnvironment(ctx, users[0], eid, "job-test", now); err != nil {
		t.Fatal(err)
	}
	j.State = "RUNNING"
	j.Quality = "ok"
	j.CPUUsec = 10
	r.JobMeasurements = []JobMeasurement{j}
	if err := s.EnvironmentHeartbeat(ctx, "i-shared", r, now); err != nil {
		t.Fatal(err)
	}
	r.Sequence = 8
	r.ObservedAt = now.Add(2 * time.Minute).Unix()
	j.CPUUsec = 100
	r.JobMeasurements = []JobMeasurement{j}
	if err := s.EnvironmentHeartbeat(ctx, "i-shared", r, time.Unix(r.ObservedAt, 0)); err != nil {
		t.Fatal(err)
	}
	var q string
	var delta sql.NullInt64
	s.DB.QueryRow(`SELECT quality,cpu_delta FROM job_samples WHERE sequence=8`).Scan(&q, &delta)
	if q != "gap" || delta.Valid {
		t.Fatal(q, delta)
	}
}
