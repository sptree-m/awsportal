package store

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/sptree-m/awsportal/internal/billing"
	"time"
)

const billingJobSchema = `CREATE TABLE IF NOT EXISTS billing_jobs(id TEXT PRIMARY KEY,requested_by INTEGER NOT NULL REFERENCES users(id),source_key TEXT NOT NULL,source_version TEXT NOT NULL,scope TEXT NOT NULL,policy TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'PENDING',run_id TEXT NOT NULL DEFAULT '',error TEXT NOT NULL DEFAULT '',created_at INTEGER NOT NULL);`

type BillingJob struct {
	ID, Key, Version, State string
	UserID                  int64
	Scope                   billing.Scope
	Policy                  BillingPolicy
}

func (s *Store) QueueBilling(ctx context.Context, u User, key, version string, scope billing.Scope, policy BillingPolicy) (string, error) {
	if err := adminOnly(u); err != nil {
		return "", err
	}
	if key == "" || version == "" {
		return "", fmt.Errorf("source key and immutable version required")
	}
	id, err := operationID()
	if err != nil {
		return "", err
	}
	sj, _ := json.Marshal(scope)
	pj, _ := json.Marshal(policy)
	_, err = s.DB.ExecContext(ctx, `INSERT INTO billing_jobs(id,requested_by,source_key,source_version,scope,policy,created_at) VALUES(?,?,?,?,?,?,?)`, id, u.ID, key, version, string(sj), string(pj), time.Now().Unix())
	return id, err
}
func (s *Store) PendingBilling(ctx context.Context) ([]BillingJob, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,requested_by,source_key,source_version,scope,policy,state FROM billing_jobs WHERE state='PENDING' ORDER BY created_at LIMIT 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var xs []BillingJob
	for rows.Next() {
		var x BillingJob
		var scope, policy string
		if err = rows.Scan(&x.ID, &x.UserID, &x.Key, &x.Version, &scope, &policy, &x.State); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(scope), &x.Scope); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(policy), &x.Policy); err != nil {
			return nil, err
		}
		xs = append(xs, x)
	}
	return xs, rows.Err()
}
func (s *Store) FinishBillingJob(ctx context.Context, id, run string, failure bool) error {
	state := "SUCCEEDED"
	message := ""
	if failure {
		state = "FAILED"
		message = "CUR import failed; source retained"
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE billing_jobs SET state=?,run_id=?,error=? WHERE id=?`, state, run, message, id)
	return err
}
