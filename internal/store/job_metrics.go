package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

const jobMetricsSchema = `
CREATE TABLE IF NOT EXISTS managed_jobs(
 instance_id INTEGER NOT NULL REFERENCES instances(id), job_id TEXT NOT NULL,
 environment_id INTEGER NOT NULL REFERENCES environments(id), user_id INTEGER NOT NULL REFERENCES users(id),
 boot_id TEXT NOT NULL, state TEXT NOT NULL, started_at INTEGER NOT NULL, ended_at INTEGER NOT NULL,
 last_seen INTEGER NOT NULL, payload TEXT NOT NULL, PRIMARY KEY(instance_id,job_id));
CREATE TABLE IF NOT EXISTS job_samples(
 instance_id INTEGER NOT NULL, boot_id TEXT NOT NULL, sequence INTEGER NOT NULL, job_id TEXT NOT NULL,
 observed_at INTEGER NOT NULL, quality TEXT NOT NULL, cpu_delta INTEGER, read_delta INTEGER, write_delta INTEGER,
 payload TEXT NOT NULL, PRIMARY KEY(instance_id,boot_id,sequence,job_id),
 FOREIGN KEY(instance_id,job_id) REFERENCES managed_jobs(instance_id,job_id));
CREATE INDEX IF NOT EXISTS managed_job_user ON managed_jobs(user_id,last_seen);
INSERT OR IGNORE INTO environment_migrations(version) VALUES(2);
`

type JobMeasurement struct {
	JobID        string `json:"job_id"`
	UserID       int64  `json:"user_id"`
	BootID       string `json:"boot_id"`
	State        string `json:"state"`
	StartedAt    int64  `json:"started_at"`
	EndedAt      int64  `json:"ended_at"`
	ExitCode     *int   `json:"exit_code"`
	Quality      string `json:"quality"`
	CounterEpoch string `json:"counter_epoch"`
	CPUUsec      int64  `json:"cpu_usec"`
	MemoryBytes  int64  `json:"memory_bytes"`
	ReadBytes    int64  `json:"read_bytes"`
	WriteBytes   int64  `json:"write_bytes"`
}

var jobIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

func jobTerminal(state string) bool {
	return state == "SUCCEEDED" || state == "FAILED" || state == "INTERRUPTED"
}

func validateJobMeasurements(r EnvironmentReport) error {
	if r.MeasurementVersion < 0 || r.MeasurementVersion > 1 || len(r.JobMeasurements) > 128 || r.MeasurementVersion == 0 && len(r.JobMeasurements) > 0 {
		return fmt.Errorf("unsupported job measurement schema")
	}
	seen := map[string]bool{}
	active := map[int64]int{}
	unknown := map[int64]bool{}
	for _, j := range r.JobMeasurements {
		if !jobIDPattern.MatchString(j.JobID) || seen[j.JobID] || j.UserID < 1 || j.UserID > 1000000 || len(j.BootID) < 8 || len(j.BootID) > 64 || len(j.CounterEpoch) < 1 || len(j.CounterEpoch) > 100 || j.CPUUsec < 0 || j.MemoryBytes < 0 || j.ReadBytes < 0 || j.WriteBytes < 0 || j.StartedAt < 1 || j.StartedAt > r.ObservedAt+30 || j.EndedAt < 0 || j.EndedAt > r.ObservedAt+30 || j.EndedAt > 0 && j.EndedAt < j.StartedAt {
			return fmt.Errorf("invalid job measurement")
		}
		seen[j.JobID] = true
		if j.Quality != "ok" && j.Quality != "counter_reset" && j.Quality != "unavailable" && j.Quality != "boot_changed" && j.Quality != "scope_removed" {
			return fmt.Errorf("invalid job quality")
		}
		if j.State != "RUNNING" && j.State != "UNKNOWN" && !jobTerminal(j.State) {
			return fmt.Errorf("invalid job state")
		}
		if jobTerminal(j.State) != (j.EndedAt > 0) || !jobTerminal(j.State) && j.BootID != r.BootID || j.State == "SUCCEEDED" && (j.ExitCode == nil || *j.ExitCode != 0) || j.State == "FAILED" && (j.ExitCode == nil || *j.ExitCode == 0) || j.ExitCode != nil && (*j.ExitCode < -255 || *j.ExitCode > 255) {
			return fmt.Errorf("inconsistent job state")
		}
		if !jobTerminal(j.State) {
			active[j.UserID]++
			unknown[j.UserID] = unknown[j.UserID] || j.State == "UNKNOWN"
		}
	}
	for uid, count := range active {
		found := false
		for _, w := range r.Work {
			if w.UserID == uid && w.Jobs >= count && (!unknown[uid] || w.Unclassified) {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("active job missing work protection")
		}
	}
	return nil
}

func recordJobMeasurements(ctx context.Context, tx *sql.Tx, iid, eid int64, r EnvironmentReport, now time.Time) error {
	// Missing a known active job is a collector fault, not evidence of completion.
	rows, err := tx.QueryContext(ctx, `SELECT job_id FROM managed_jobs WHERE instance_id=? AND state IN ('RUNNING','UNKNOWN')`, iid)
	if err != nil {
		return err
	}
	missing := false
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		found := false
		for _, j := range r.JobMeasurements {
			if j.JobID == id {
				found = true
			}
		}
		missing = missing || !found
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if missing {
		return fmt.Errorf("known active job omitted")
	}
	for _, j := range r.JobMeasurements {
		var previousRaw string
		err := tx.QueryRowContext(ctx, `SELECT payload FROM managed_jobs WHERE instance_id=? AND job_id=?`, iid, j.JobID).Scan(&previousRaw)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		var previous JobMeasurement
		quality := j.Quality
		var cpu, read, written any
		if err == nil {
			if err = json.Unmarshal([]byte(previousRaw), &previous); err != nil {
				return err
			}
			if previous.UserID != j.UserID || previous.BootID != j.BootID || previous.StartedAt != j.StartedAt {
				return fmt.Errorf("job identity changed")
			}
			if jobTerminal(previous.State) && previousRaw != jobJSON(j) {
				return fmt.Errorf("terminal job changed")
			}
			if j.CounterEpoch != previous.CounterEpoch || j.CPUUsec < previous.CPUUsec || j.ReadBytes < previous.ReadBytes || j.WriteBytes < previous.WriteBytes {
				quality = "counter_reset"
			} else if quality == "ok" && previous.Quality == "ok" {
				var at int64
				if err = tx.QueryRowContext(ctx, `SELECT MAX(observed_at) FROM job_samples WHERE instance_id=? AND job_id=?`, iid, j.JobID).Scan(&at); err != nil {
					return err
				}
				if r.ObservedAt <= at || r.ObservedAt-at > 90 {
					quality = "gap"
				} else {
					cpu = j.CPUUsec - previous.CPUUsec
					read = j.ReadBytes - previous.ReadBytes
					written = j.WriteBytes - previous.WriteBytes
				}
			}
		} else {
			// Even a sub-minute completed job needs a valid assignment. Recovered
			// terminal jobs retain their original owner after an agent/OS restart.
			var count int
			if err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM environment_assignments WHERE instance_id=? AND user_id=? AND generation=? AND state!='RELEASED')+(SELECT COUNT(*) FROM personal_home_leases l JOIN environment_instances ei ON ei.instance_id=l.instance_id JOIN environments e ON e.id=ei.environment_id WHERE l.instance_id=? AND l.user_id=? AND ei.generation=? AND e.mode='personal' AND e.owner_user_id=l.user_id)`, iid, j.UserID, r.Generation, iid, j.UserID, r.Generation).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				return fmt.Errorf("job has no assignment")
			}
			if quality == "ok" {
				quality = "baseline"
			}
		}
		raw := jobJSON(j)
		if _, err = tx.ExecContext(ctx, `INSERT INTO managed_jobs VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(instance_id,job_id) DO UPDATE SET state=excluded.state,ended_at=excluded.ended_at,last_seen=excluded.last_seen,payload=excluded.payload`, iid, j.JobID, eid, j.UserID, j.BootID, j.State, j.StartedAt, j.EndedAt, now.Unix(), raw); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO job_samples VALUES(?,?,?,?,?,?,?,?,?,?)`, iid, r.BootID, r.Sequence, j.JobID, r.ObservedAt, quality, cpu, read, written, raw); err != nil {
			return err
		}
	}
	return nil
}

func jobJSON(j JobMeasurement) string { b, _ := json.Marshal(j); return string(b) }

type ManagedJob struct {
	JobMeasurement
	InstanceID string
	LastSeen   int64
	Stale      bool
}

func (s *Store) VisibleManagedJobs(ctx context.Context, user User, now time.Time) ([]ManagedJob, error) {
	// A job's details are visible only to its owner or a Portal Admin. Group ACL
	// does not grant access to another user's process history.
	query := `SELECT j.payload,i.instance_id,j.last_seen FROM managed_jobs j JOIN instances i ON i.id=j.instance_id WHERE (?='portal_admin' OR j.user_id=?) ORDER BY j.last_seen DESC,j.job_id LIMIT 100`
	rows, err := s.DB.QueryContext(ctx, query, user.Role, user.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []ManagedJob{}
	for rows.Next() {
		var j ManagedJob
		var raw string
		if err = rows.Scan(&raw, &j.InstanceID, &j.LastSeen); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &j.JobMeasurement); err != nil {
			return nil, err
		}
		j.Stale = !jobTerminal(j.State) && j.LastSeen < now.Add(-90*time.Second).Unix()
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}
