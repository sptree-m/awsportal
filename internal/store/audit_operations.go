package store

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/sptree-m/awsportal/internal/auth"
)

const auditOperationSchema = `CREATE TABLE IF NOT EXISTS audit_operations(id TEXT PRIMARY KEY,actor TEXT NOT NULL,action TEXT NOT NULL,target TEXT NOT NULL,created INTEGER NOT NULL,completed INTEGER,result TEXT NOT NULL DEFAULT 'pending');`

// An intent is durable before an external effect. If recording its outcome fails,
// it remains pending for reconciliation; never tell callers to repeat the effect.
func (s *Store) BeginAuditOperation(ctx context.Context, actor, action, target string) (string, error) {
	id, err := auth.NewPasskeyToken()
	if err != nil {
		return "", err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_operations(id,actor,action,target,created) VALUES(?,?,?,?,?)`, id, actor, action, target, time.Now().Unix()); err != nil {
		return "", err
	}
	if err = passkeyAudit(ctx, tx, actor, "operation.begin", target, "operation="+id+";action="+action); err != nil {
		return "", err
	}
	return id, tx.Commit()
}

func (s *Store) CompleteAuditOperation(ctx context.Context, id, result string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var actor, action, target string
	if err = tx.QueryRowContext(ctx, `SELECT actor,action,target FROM audit_operations WHERE id=? AND completed IS NULL`, id).Scan(&actor, &action, &target); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE audit_operations SET completed=?,result=? WHERE id=?`, time.Now().Unix(), result, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_log(actor,action,target,result,detail) VALUES(?,?,?,?,?)`, actor, "operation.complete", target, result, "operation="+id+";action="+action); err != nil {
		return err
	}
	return tx.Commit()
}

// Report outside SQLite as well: a database fault must not suppress its alert.
func AuditFailure(action string) {
	log.Printf("SECURITY audit persistence failed: action=%q; reconcile pending operations", action)
}

func (s *Store) AuditedEffect(ctx context.Context, actor, action, target string, effect func() error) error {
	id, err := s.BeginAuditOperation(ctx, actor, action, target)
	if err != nil {
		AuditFailure(action)
		return fmt.Errorf("audit intent unavailable: %w", err)
	}
	err = effect()
	result := "ok"
	if err != nil {
		result = "error"
	}
	if finishErr := s.CompleteAuditOperation(context.WithoutCancel(ctx), id, result); finishErr != nil {
		AuditFailure(action)
	}
	return err
}
