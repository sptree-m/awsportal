package store

import (
	"context"
	"encoding/json"
	"fmt"
)

// Archive each historical row as immutable open and close events in the same
// durable Parquet queue. A late collector reconstructs both sides of a closure.
func (s *Store) ArchiveHistory(ctx context.Context) error {
	for _, table := range []string{"resource_registry", "group_membership_history", "user_status_history"} {
		rows, err := s.DB.QueryContext(ctx, `SELECT h.* FROM `+table+` h WHERE NOT EXISTS(SELECT 1 FROM usage_export_queue q WHERE q.object_key='usage/history/v1/`+table+`/'||h.id||'/open.json') OR (h.valid_to IS NOT NULL AND NOT EXISTS(SELECT 1 FROM usage_export_queue q WHERE q.object_key='usage/history/v1/`+table+`/'||h.id||'/close.json')) ORDER BY h.id LIMIT 200`)
		if err != nil {
			return err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return err
		}
		type event struct{ key, payload string }
		var events []event
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err = rows.Scan(pointers...); err != nil {
				rows.Close()
				return err
			}
			record := map[string]any{"record_type": table}
			for i, k := range columns {
				record[k] = values[i]
			}
			id := record["id"]
			closed := record["valid_to"]
			record["valid_to"] = nil
			record["observed_at"] = record["valid_from"]
			raw, _ := json.Marshal(record)
			events = append(events, event{fmt.Sprintf("usage/history/v1/%s/%v/open.json", table, id), string(raw)})
			if closed != nil {
				record["valid_to"] = closed
				record["observed_at"] = closed
				raw, _ = json.Marshal(record)
				events = append(events, event{fmt.Sprintf("usage/history/v1/%s/%v/close.json", table, id), string(raw)})
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, e := range events {
			if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO usage_export_queue(object_key,payload) VALUES(?,?)`, e.key, e.payload); err != nil {
				tx.Rollback()
				return err
			}
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
