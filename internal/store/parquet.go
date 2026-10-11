package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/parquet-go/parquet-go"
	"time"
)

const parquetSchema = `
CREATE TABLE IF NOT EXISTS instance_boots(instance_id INTEGER NOT NULL,boot_id TEXT NOT NULL,seen_at INTEGER NOT NULL,PRIMARY KEY(instance_id,boot_id));
INSERT OR IGNORE INTO instance_boots SELECT instance_id,boot_id,MIN(observed_at) FROM instance_samples GROUP BY instance_id,boot_id;
CREATE TABLE IF NOT EXISTS connection_intervals(instance_id INTEGER NOT NULL REFERENCES instances(id),user_id INTEGER NOT NULL REFERENCES users(id),environment_id INTEGER NOT NULL,group_id INTEGER NOT NULL,boot_id TEXT NOT NULL,sequence INTEGER NOT NULL,generation INTEGER NOT NULL,start_at INTEGER NOT NULL,end_at INTEGER NOT NULL,PRIMARY KEY(instance_id,user_id,boot_id,sequence));

CREATE TABLE IF NOT EXISTS usage_parquet_batches(object_key TEXT PRIMARY KEY,payload BLOB NOT NULL,status TEXT NOT NULL DEFAULT 'PENDING',created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS usage_parquet_rows(export_id INTEGER PRIMARY KEY REFERENCES usage_export_queue(id),object_key TEXT NOT NULL REFERENCES usage_parquet_batches(object_key));
`

type ParquetBatch struct {
	Key     string
	Payload []byte
}
type usageRow struct {
	RecordType   string  `parquet:"record_type"`
	SourceKey    string  `parquet:"source_key"`
	BootID       string  `parquet:"boot_id"`
	Sequence     int64   `parquet:"sequence"`
	Generation   int64   `parquet:"generation"`
	ObservedAt   int64   `parquet:"observed_at"`
	CPU          float64 `parquet:"cpu"`
	Memory       float64 `parquet:"memory"`
	MetricsValid bool    `parquet:"metrics_valid"`
	Payload      string  `parquet:"payload"`
}

func (s *Store) PrepareParquet(ctx context.Context, now time.Time) ([]ParquetBatch, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT q.id,q.object_key,q.payload FROM usage_export_queue q WHERE q.status IN ('PENDING','EXPORTED') AND NOT EXISTS(SELECT 1 FROM usage_parquet_rows p WHERE p.export_id=q.id) ORDER BY q.id LIMIT 100`)
	if err != nil {
		return nil, err
	}
	var ids []int64
	var samples []usageRow
	total := 0
	digest := sha256.New()
	for rows.Next() {
		var id int64
		var key, raw string
		if err = rows.Scan(&id, &key, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		if total+len(raw) > 8*1024*1024 && len(ids) > 0 {
			break
		}
		total += len(raw)
		var r EnvironmentReport
		if err = json.Unmarshal([]byte(raw), &r); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
		fmt.Fprintln(digest, key)
		var record map[string]json.RawMessage
		_ = json.Unmarshal([]byte(raw), &record)
		recordType := "node_usage_v2"
		_ = json.Unmarshal(record["record_type"], &recordType)
		samples = append(samples, usageRow{recordType, key, r.BootID, r.Sequence, r.Generation, r.ObservedAt, r.CPU, r.Memory, r.MetricsValid, raw})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		var buf bytes.Buffer
		w := parquet.NewGenericWriter[usageRow](&buf)
		if _, err = w.Write(samples); err != nil {
			return nil, err
		}
		if err = w.Close(); err != nil {
			return nil, err
		}
		key := "usage/parquet/v1/" + hex.EncodeToString(digest.Sum(nil)) + ".parquet"
		if _, err = tx.ExecContext(ctx, `INSERT INTO usage_parquet_batches(object_key,payload,created_at) VALUES(?,?,?)`, key, buf.Bytes(), now.Unix()); err != nil {
			return nil, err
		}
		for _, id := range ids {
			if _, err = tx.ExecContext(ctx, `INSERT INTO usage_parquet_rows VALUES(?,?)`, id, key); err != nil {
				return nil, err
			}
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT object_key,payload FROM usage_parquet_batches WHERE status='PENDING' ORDER BY created_at LIMIT 2`)
	if err != nil {
		return nil, err
	}
	var out []ParquetBatch
	for rows.Next() {
		var x ParquetBatch
		if err = rows.Scan(&x.Key, &x.Payload); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}
func (s *Store) CompleteParquet(ctx context.Context, key string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE usage_export_queue SET status='EXPORTED',error='' WHERE id IN (SELECT export_id FROM usage_parquet_rows WHERE object_key=?)`, key); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE usage_parquet_batches SET status='EXPORTED',payload=X'' WHERE object_key=?`, key); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RetainUsage(ctx context.Context, now time.Time) error {
	// Never prune an unsent sample or an unexported Parquet batch. Historical
	// billing uses S3 replay after seven days; finalized runs retain their evidence.
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `DELETE FROM instance_samples WHERE observed_at<? AND EXISTS(SELECT 1 FROM usage_export_queue q JOIN usage_parquet_rows p ON p.export_id=q.id JOIN usage_parquet_batches b ON b.object_key=p.object_key WHERE q.object_key='usage/v2/'||(SELECT instance_id FROM instances WHERE id=instance_samples.instance_id)||'/'||instance_samples.boot_id||'/'||printf('%020d',instance_samples.sequence)||'.json' AND q.status='EXPORTED' AND b.status='EXPORTED')`, now.Add(-7*24*time.Hour).Unix())
	if err != nil {
		return err
	}
	for _, table := range []string{"desktop_samples", "job_samples"} {
		query := `DELETE FROM ` + table + ` WHERE observed_at<? AND NOT EXISTS(SELECT 1 FROM instance_samples s WHERE s.instance_id=` + table + `.instance_id AND s.boot_id=` + table + `.boot_id AND s.sequence=` + table + `.sequence)`
		if _, err = tx.ExecContext(ctx, query, now.Add(-7*24*time.Hour).Unix()); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE usage_export_queue SET payload='{}' WHERE status='EXPORTED' AND json_extract(payload,'$.observed_at')<? AND EXISTS(SELECT 1 FROM usage_parquet_rows p JOIN usage_parquet_batches b ON b.object_key=p.object_key WHERE p.export_id=usage_export_queue.id AND b.status='EXPORTED')`, now.Add(-7*24*time.Hour).Unix()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM connection_intervals WHERE end_at<?`, now.AddDate(0, -13, 0).Unix()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM operational_samples WHERE observed_at<?`, now.AddDate(0, 0, -45).Unix()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM storage_snapshots WHERE observed_at<? AND EXISTS(SELECT 1 FROM usage_export_queue q JOIN usage_parquet_rows p ON p.export_id=q.id JOIN usage_parquet_batches b ON b.object_key=p.object_key WHERE q.object_key='usage/v3/storage/'||storage_snapshots.resource_id||'/'||printf('%020d',storage_snapshots.observed_at)||'.json' AND q.status='EXPORTED' AND b.status='EXPORTED')`, now.Add(-7*24*time.Hour).Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
