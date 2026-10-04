package store

import "context"

type UsageExport struct {
	ID           int64
	Key, Payload string
	Retries      int
}

func (s *Store) PendingUsageExports(ctx context.Context) ([]UsageExport, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,object_key,payload,retry_count FROM usage_export_queue WHERE status='PENDING' ORDER BY retry_count,id LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageExport{}
	for rows.Next() {
		var x UsageExport
		if err = rows.Scan(&x.ID, &x.Key, &x.Payload, &x.Retries); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *Store) CompleteUsageExport(ctx context.Context, id int64, success bool) error {
	if success {
		_, err := s.DB.ExecContext(ctx, `UPDATE usage_export_queue SET status='EXPORTED',error='' WHERE id=?`, id)
		return err
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE usage_export_queue SET retry_count=retry_count+1,error='S3 export failed; retry pending' WHERE id=?`, id)
	return err
}
