package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

const auditExportSchema = `CREATE TABLE IF NOT EXISTS audit_export_state(destination TEXT PRIMARY KEY,cursor INTEGER NOT NULL DEFAULT 0,last_id INTEGER NOT NULL DEFAULT 0,object_key TEXT NOT NULL DEFAULT '',payload BLOB);`

type AuditBatch struct {
	Key     string
	Payload []byte
	LastID  int64
}

// Persist the exact bytes before upload. Ambiguous S3 responses and restarts
// retry the same key and bytes, including when new audit rows arrive.
func (s *Store) PrepareAuditExport(ctx context.Context, destination, prefix string) (AuditBatch, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return AuditBatch{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO audit_export_state(destination) VALUES(?)`, destination); err != nil {
		return AuditBatch{}, err
	}
	var b AuditBatch
	var cursor int64
	if err = tx.QueryRowContext(ctx, `SELECT cursor,last_id,object_key,payload FROM audit_export_state WHERE destination=?`, destination).Scan(&cursor, &b.LastID, &b.Key, &b.Payload); err != nil {
		return b, err
	}
	if b.Key != "" {
		return b, tx.Commit()
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,at,actor,action,COALESCE(target,''),result,COALESCE(detail,'') FROM audit_log WHERE id>? ORDER BY id LIMIT 1000`, cursor)
	if err != nil {
		return b, err
	}
	var first int64
	var date string
	for rows.Next() {
		var x struct {
			ID     int64  `json:"id"`
			At     string `json:"at"`
			Actor  string `json:"actor"`
			Action string `json:"action"`
			Target string `json:"target"`
			Result string `json:"result"`
			Detail string `json:"detail"`
		}
		if err = rows.Scan(&x.ID, &x.At, &x.Actor, &x.Action, &x.Target, &x.Result, &x.Detail); err != nil {
			rows.Close()
			return b, err
		}
		if first == 0 {
			first = x.ID
			date = strings.SplitN(x.At, " ", 2)[0]
		}
		x.At = strings.Replace(x.At, " ", "T", 1) + "Z"
		data, e := json.Marshal(x)
		if e != nil {
			rows.Close()
			return b, e
		}
		b.Payload = append(b.Payload, data...)
		b.Payload = append(b.Payload, '\n')
		b.LastID = x.ID
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return b, err
	}
	if first != 0 {
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		if _, err = writer.Write(b.Payload); err != nil {
			return b, err
		}
		if err = writer.Close(); err != nil {
			return b, err
		}
		b.Payload = compressed.Bytes()
		digest := sha256.Sum256(b.Payload)
		b.Key = fmt.Sprintf("%sdate=%s/%020d-%020d-%x.jsonl.gz", prefix, date, first, b.LastID, digest)
		if _, err = tx.ExecContext(ctx, `UPDATE audit_export_state SET last_id=?,object_key=?,payload=? WHERE destination=?`, b.LastID, b.Key, b.Payload, destination); err != nil {
			return b, err
		}
	}
	return b, tx.Commit()
}

func (s *Store) CompleteAuditExport(ctx context.Context, destination string, b AuditBatch) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE audit_export_state SET cursor=last_id,last_id=0,object_key='',payload=NULL WHERE destination=? AND object_key=? AND last_id=?`, destination, b.Key, b.LastID)
	return err
}
