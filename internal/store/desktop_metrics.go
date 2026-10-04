package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

const desktopSchema = `CREATE TABLE IF NOT EXISTS desktop_samples(instance_id INTEGER NOT NULL,boot_id TEXT NOT NULL,sequence INTEGER NOT NULL,user_id INTEGER NOT NULL,observed_at INTEGER NOT NULL,quality TEXT NOT NULL,cpu_delta INTEGER,memory_bytes INTEGER,read_delta INTEGER,write_delta INTEGER,payload TEXT NOT NULL,PRIMARY KEY(instance_id,boot_id,sequence,user_id));`

func recordDesktop(ctx context.Context, tx *sql.Tx, iid int64, r, previous EnvironmentReport) error {
	for _, w := range r.Work {
		m := w.DesktopMeasurement
		if m == nil {
			continue
		}
		if m.Quality != "ok" && m.Quality != "unavailable" || m.CPUUsec < 0 || m.MemoryBytes < 0 || m.ReadBytes < 0 || m.WriteBytes < 0 || len(m.CounterEpoch) > 100 {
			return fmt.Errorf("invalid desktop measurement")
		}
		quality := m.Quality
		var cpu, read, write, memory any
		if quality == "ok" {
			memory = m.MemoryBytes
			quality = "baseline"
			for _, before := range previous.Work {
				p := before.DesktopMeasurement
				if before.UserID != w.UserID || p == nil || p.Quality != "ok" {
					continue
				}
				if previous.BootID != r.BootID || r.ObservedAt-previous.ObservedAt > 90 {
					quality = "gap"
				} else if m.CounterEpoch != p.CounterEpoch || m.CPUUsec < p.CPUUsec || m.ReadBytes < p.ReadBytes || m.WriteBytes < p.WriteBytes {
					quality = "counter_reset"
				} else {
					quality = "ok"
					cpu = m.CPUUsec - p.CPUUsec
					read = m.ReadBytes - p.ReadBytes
					write = m.WriteBytes - p.WriteBytes
				}
			}
		}
		raw, _ := json.Marshal(m)
		if _, err := tx.ExecContext(ctx, `INSERT INTO desktop_samples VALUES(?,?,?,?,?,?,?,?,?,?,?)`, iid, r.BootID, r.Sequence, w.UserID, r.ObservedAt, quality, cpu, memory, read, write, string(raw)); err != nil {
			return err
		}
	}
	return nil
}
