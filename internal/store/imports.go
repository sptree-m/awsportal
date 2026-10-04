package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const importSchema = `
CREATE TABLE IF NOT EXISTS import_controls(id INTEGER PRIMARY KEY CHECK(id=1),enabled INTEGER NOT NULL DEFAULT 0);
INSERT OR IGNORE INTO import_controls(id) VALUES(1);
CREATE TABLE IF NOT EXISTS import_jobs(id TEXT PRIMARY KEY,requested_by INTEGER NOT NULL REFERENCES users(id),idempotency_key TEXT NOT NULL,profile TEXT NOT NULL,source_root TEXT NOT NULL,bucket TEXT NOT NULL,prefix TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'REQUESTED',instance_id TEXT NOT NULL DEFAULT '',token_hash TEXT NOT NULL DEFAULT '',provision_input TEXT NOT NULL DEFAULT '',execution_arn TEXT NOT NULL DEFAULT '',cleanup_arn TEXT NOT NULL DEFAULT '',cleanup_operation_id TEXT NOT NULL DEFAULT '',cleanup_origin TEXT NOT NULL DEFAULT '',sequence INTEGER NOT NULL DEFAULT 0,expected_manifest TEXT NOT NULL DEFAULT '',validation TEXT NOT NULL DEFAULT '',logs TEXT NOT NULL DEFAULT '',created_at INTEGER NOT NULL,deadline INTEGER NOT NULL,error TEXT NOT NULL DEFAULT '',UNIQUE(requested_by,idempotency_key));
CREATE TABLE IF NOT EXISTS import_events(job_id TEXT NOT NULL REFERENCES import_jobs(id),sequence INTEGER NOT NULL,state TEXT NOT NULL,at INTEGER NOT NULL,detail TEXT NOT NULL,PRIMARY KEY(job_id,sequence));
CREATE TABLE IF NOT EXISTS datasets(id TEXT PRIMARY KEY,bucket TEXT NOT NULL,prefix TEXT NOT NULL,manifest TEXT NOT NULL,state TEXT NOT NULL CHECK(state IN ('AVAILABLE','REVOKED')),source_job TEXT UNIQUE NOT NULL REFERENCES import_jobs(id),created_at INTEGER NOT NULL);
`

type ImportJob struct {
	ID, Profile, SourceRoot, Bucket, Prefix, State, InstanceID, ExecutionARN, CleanupARN, ExpectedManifest, Validation, Logs, Error, ProvisionInput, CleanupOperationID, CleanupOrigin string
	RequestedBy, Sequence, CreatedAt, Deadline                                                                                                                                         int64
}
type ImportEvidence struct {
	ExpectedManifest  string `json:"expected_manifest"`
	Validation        string `json:"validation"`
	Logs              string `json:"logs"`
	ExpectedFiles     int64  `json:"expected_files"`
	UploadedFiles     int64  `json:"uploaded_files"`
	ExpectedBytes     int64  `json:"expected_bytes"`
	UploadedBytes     int64  `json:"uploaded_bytes"`
	ReadErrors        int64  `json:"read_errors"`
	Mismatches        int64  `json:"mismatches"`
	ExitCode          int    `json:"exit_code"`
	ChecksumsVerified bool   `json:"checksums_verified"`
}

func (s *Store) SetImportEnabled(ctx context.Context, u User, enabled bool) error {
	if err := adminOnly(u); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE import_controls SET enabled=? WHERE id=1`, enabled)
	return err
}
func (s *Store) RequestImport(ctx context.Context, u User, key, root, bucket, prefix string, now time.Time) (string, error) {
	if err := adminOnly(u); err != nil {
		return "", err
	}
	if len(key) < 8 || len(key) > 128 || !strings.HasPrefix(root, `C:\Users\`) || strings.Contains(root, "..") || !strings.Contains(root, `\Box\`) || bucket == "" || prefix == "" || strings.Contains(prefix, "..") || strings.HasPrefix(prefix, "/") {
		return "", fmt.Errorf("approved Box root, bucket, isolated prefix and idempotency key required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var enabled bool
	if err = tx.QueryRowContext(ctx, `SELECT enabled FROM import_controls WHERE id=1`).Scan(&enabled); err != nil {
		return "", err
	}
	if !enabled {
		return "", fmt.Errorf("import feature disabled")
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM import_jobs WHERE requested_by=? AND idempotency_key=?`, u.ID, key).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM import_jobs WHERE state IN ('REQUESTED','PROVISIONING','WAITING_BOX_LOGIN','WAITING_OFFLINE_READY','UPLOADING','VALIDATING','VERIFIED','CLEANUP_REQUESTED','CLEANING_UP')`).Scan(&active); err != nil { return "", err }
	if active >= 10 { return "", fmt.Errorf("active import limit reached") }
	id, err = operationID()
	if err != nil {
		return "", err
	}
	var image, template, version, checksum string
	if err = tx.QueryRowContext(ctx, `SELECT ami_id,launch_template_id,launch_template_version,checksum FROM golden_images WHERE profile_id='windows-box-v1' AND channel='STABLE'`).Scan(&image, &template, &version, &checksum); err != nil {
		return "", fmt.Errorf("validated stable Windows Golden AMI required")
	}
	input, _ := json.Marshal(map[string]any{"operation_id": id, "environment_id": "windows", "generation": 1, "kind": "PROVISION", "expected_image_id": image, "expected_template_id": template, "expected_template_version": version, "expected_checksum": checksum})
	// Prefix ownership prevents two jobs overwriting one another's destination.
	var occupied bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM import_jobs WHERE bucket=? AND (prefix=? OR prefix LIKE ? OR ? LIKE prefix||'%'))`, bucket, prefix, prefix+"%", prefix).Scan(&occupied); err != nil {
		return "", err
	}
	if occupied {
		return "", fmt.Errorf("destination prefix already reserved")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO import_jobs(id,requested_by,idempotency_key,profile,source_root,bucket,prefix,provision_input,created_at,deadline) VALUES(?,?,?,'windows-box-v1',?,?,?,?,?,?)`, id, u.ID, key, root, bucket, strings.TrimSuffix(prefix, "/")+"/", string(input), now.Unix(), now.Add(48*time.Hour).Unix())
	if err != nil {
		return "", err
	}
	return id, tx.Commit()
}
func (s *Store) ImportJobs(ctx context.Context) ([]ImportJob, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,profile,source_root,bucket,prefix,state,instance_id,execution_arn,cleanup_arn,expected_manifest,validation,logs,error,provision_input,cleanup_operation_id,cleanup_origin,requested_by,sequence,created_at,deadline FROM import_jobs ORDER BY CASE WHEN state IN ('REQUESTED','PROVISIONING','WAITING_BOX_LOGIN','WAITING_OFFLINE_READY','UPLOADING','VALIDATING','VERIFIED','CLEANUP_REQUESTED','CLEANING_UP') THEN 0 ELSE 1 END,created_at DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var xs []ImportJob
	for rows.Next() {
		var x ImportJob
		if err = rows.Scan(&x.ID, &x.Profile, &x.SourceRoot, &x.Bucket, &x.Prefix, &x.State, &x.InstanceID, &x.ExecutionARN, &x.CleanupARN, &x.ExpectedManifest, &x.Validation, &x.Logs, &x.Error, &x.ProvisionInput, &x.CleanupOperationID, &x.CleanupOrigin, &x.RequestedBy, &x.Sequence, &x.CreatedAt, &x.Deadline); err != nil {
			return nil, err
		}
		xs = append(xs, x)
	}
	return xs, rows.Err()
}
func (s *Store) ImportAgentJob(ctx context.Context, token string) (ImportJob, error) {
	var x ImportJob
	err := s.DB.QueryRowContext(ctx, `SELECT id,profile,source_root,bucket,prefix,state,instance_id,execution_arn,cleanup_arn,expected_manifest,validation,logs,error,provision_input,cleanup_operation_id,cleanup_origin,requested_by,sequence,created_at,deadline FROM import_jobs WHERE token_hash=? AND token_hash!=''`, DCVTokenHash(token)).Scan(&x.ID, &x.Profile, &x.SourceRoot, &x.Bucket, &x.Prefix, &x.State, &x.InstanceID, &x.ExecutionARN, &x.CleanupARN, &x.ExpectedManifest, &x.Validation, &x.Logs, &x.Error, &x.ProvisionInput, &x.CleanupOperationID, &x.CleanupOrigin, &x.RequestedBy, &x.Sequence, &x.CreatedAt, &x.Deadline)
	return x, err
}

var importTransitions = map[string]string{"WAITING_BOX_LOGIN": "WAITING_OFFLINE_READY", "WAITING_OFFLINE_READY": "UPLOADING", "UPLOADING": "VALIDATING"}

// Agent messages cannot assert VERIFIED or SUCCEEDED. Independent S3 verification
// and cloud cleanup own those transitions. Failure retains the Windows instance.
func (s *Store) ImportAgentEvent(ctx context.Context, token, next string, seq int64, e ImportEvidence, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id, state string
	var previous, deadline int64
	if err = tx.QueryRowContext(ctx, `SELECT id,state,sequence,deadline FROM import_jobs WHERE token_hash=? AND token_hash!=''`, DCVTokenHash(token)).Scan(&id, &state, &previous, &deadline); err != nil {
		return err
	}
	raw, _ := json.Marshal(e)
	if seq <= previous {
		var detail, oldstate string
		if err = tx.QueryRowContext(ctx, `SELECT state,detail FROM import_events WHERE job_id=? AND sequence=?`, id, seq).Scan(&oldstate, &detail); err == nil && oldstate == next && detail == string(raw) {
			return nil
		}
		return fmt.Errorf("changed or old event")
	}
	if seq != previous+1 {
		return fmt.Errorf("event gap")
	}
	if now.Unix() > deadline {
		return fmt.Errorf("import timed out")
	}
	if next != "FAILED" && importTransitions[state] != next {
		return fmt.Errorf("invalid import transition")
	}
	if state == "SUCCEEDED" || state == "CLEANING_UP" || state == "VERIFIED" || state == "FAILED" || state == "CANCELLED" || state == "TIMED_OUT" || state == "CLEANUP_FAILED" || state == "CLEANUP_REQUESTED" {
		return fmt.Errorf("terminal or cleanup state")
	}
	if next == "UPLOADING" && (e.ExpectedManifest == "" || e.ExpectedFiles <= 0 || e.ExpectedBytes < 0) {
		return fmt.Errorf("frozen readable source manifest required")
	}
	if next == "VALIDATING" && (e.Validation == "" || e.Logs == "") {
		return fmt.Errorf("immutable validation and logs required")
	}
	_, err = tx.ExecContext(ctx, `UPDATE import_jobs SET state=?,sequence=?,expected_manifest=CASE WHEN ?!='' THEN ? ELSE expected_manifest END,validation=CASE WHEN ?!='' THEN ? ELSE validation END,logs=CASE WHEN ?!='' THEN ? ELSE logs END,error=CASE WHEN ?='FAILED' THEN 'upload or source validation failed; resources retained' ELSE error END WHERE id=?`, next, seq, e.ExpectedManifest, e.ExpectedManifest, e.Validation, e.Validation, e.Logs, e.Logs, next, id)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO import_events VALUES(?,?,?,?,?)`, id, seq, next, now.Unix(), string(raw)); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) MarkImportProvision(ctx context.Context, id, arn string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE import_jobs SET state='PROVISIONING',execution_arn=? WHERE id=? AND (state='PROVISIONING' OR (state='REQUESTED' AND (SELECT enabled FROM import_controls WHERE id=1)=1))`, arn, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fmt.Errorf("import cancelled or disabled before submission")
	}
	return nil
}
func (s *Store) FinishImportProvision(ctx context.Context, id string, r CloudResult, failed bool) error {
	if failed {
		_, err := s.DB.ExecContext(ctx, `UPDATE import_jobs SET state='FAILED',error='provisioning failed; reconcile retained resources' WHERE id=?`, id)
		return err
	}
	if !ec2ResourceID.MatchString(r.InstanceID) || len(r.TokenHash) != 64 {
		return fmt.Errorf("invalid import resource identity")
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE import_jobs SET state='WAITING_BOX_LOGIN',instance_id=?,token_hash=? WHERE id=? AND state='PROVISIONING'`, r.InstanceID, r.TokenHash, id)
	return err
}
func (s *Store) VerifyImport(ctx context.Context, id string, e ImportEvidence) error {
	if e.ExpectedManifest == "" || e.Validation == "" || e.Logs == "" || e.ExpectedFiles <= 0 || e.ExpectedFiles != e.UploadedFiles || e.ExpectedBytes != e.UploadedBytes || e.ReadErrors != 0 || e.Mismatches != 0 || e.ExitCode != 0 || !e.ChecksumsVerified {
		return fmt.Errorf("full manifest/path/size/checksum validation failed")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE import_jobs SET state='VERIFIED' WHERE id=? AND state='VALIDATING' AND expected_manifest=? AND validation=? AND logs=?`, id, e.ExpectedManifest, e.Validation, e.Logs)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fmt.Errorf("validation references changed")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO datasets(id,bucket,prefix,manifest,state,source_job,created_at) SELECT id,bucket,prefix,validation,'AVAILABLE',id,? FROM import_jobs WHERE id=?`, time.Now().Unix(), id); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) StartImportCleanup(ctx context.Context, id, arn string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE import_jobs SET state='CLEANING_UP',cleanup_arn=? WHERE id=? AND state IN ('VERIFIED','CLEANUP_REQUESTED')`, arn, id)
	return err
}

// Manual cleanup retains the original failed/cancelled/timed-out outcome. It
// cannot turn a failed import into a successful dataset publication.
func (s *Store) RequestImportCleanup(ctx context.Context, u User, id, reason string) error {
	if err := adminOnly(u); err != nil {
		return err
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("cleanup reason required")
	}
	op, err := operationID()
	if err != nil {
		return err
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE import_jobs SET cleanup_origin=CASE WHEN cleanup_origin='' THEN CASE WHEN state='CLEANUP_FAILED' THEN 'VERIFIED' ELSE state END ELSE cleanup_origin END,cleanup_operation_id=?,cleanup_arn='',state='CLEANUP_REQUESTED',error=? WHERE id=? AND instance_id!='' AND token_hash!='' AND state IN ('FAILED','CANCELLED','TIMED_OUT','CLEANUP_FAILED')`, op, "administrator cleanup: "+reason, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fmt.Errorf("retained known instance and failed/cancelled/timed-out outcome required; uncertain provisioning must be reconciled first")
	}
	return nil
}
func (s *Store) FinishImportCleanup(ctx context.Context, id string, r CloudResult, failed bool) error {
	success := !failed && r.ResourcesGone && r.CredentialRevoked
	_, err := s.DB.ExecContext(ctx, `UPDATE import_jobs SET state=CASE WHEN ?=0 THEN 'CLEANUP_FAILED' WHEN cleanup_origin IN ('FAILED','CANCELLED','TIMED_OUT') THEN cleanup_origin ELSE 'SUCCEEDED' END,token_hash=CASE WHEN ? THEN '' ELSE token_hash END,error=CASE WHEN ?=0 THEN 'cleanup failed; retained resources need administrator action' WHEN cleanup_origin IN ('FAILED','CANCELLED','TIMED_OUT') THEN 'resources removed and credentials revoked; original outcome retained' ELSE '' END WHERE id=? AND state='CLEANING_UP'`, success, success, success, id)
	return err
}
func (s *Store) CancelImport(ctx context.Context, u User, id string) error {
	if err := adminOnly(u); err != nil {
		return err
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE import_jobs SET state='CANCELLED',error='cancelled; resources retained for manual cleanup' WHERE id=? AND state IN ('REQUESTED','WAITING_BOX_LOGIN','WAITING_OFFLINE_READY','UPLOADING','VALIDATING')`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fmt.Errorf("cannot cancel during provisioning or verified cleanup")
	}
	return nil
}
func (s *Store) TimeoutImports(ctx context.Context, now time.Time) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE import_jobs SET state='TIMED_OUT',error='timed out; resources retained' WHERE deadline<? AND state IN ('WAITING_BOX_LOGIN','WAITING_OFFLINE_READY','UPLOADING','VALIDATING')`, now.Unix())
	return err
}
func (s *Store) FailImportVerification(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE import_jobs SET state='FAILED',error='S3 manifest/path/size/checksum validation failed; resources retained' WHERE id=? AND state='VALIDATING'`, id)
	return err
}

func (s *Store) ReconcileImportProvision(ctx context.Context, u User, id, reason string) error {
 if err := adminOnly(u); err != nil { return err }
 if strings.TrimSpace(reason)=="" { return fmt.Errorf("AWS reconciliation reason required") }
 tx,err:=s.DB.BeginTx(ctx,nil);if err!=nil{return err};defer tx.Rollback()
 var input string
 if err=tx.QueryRowContext(ctx,`SELECT provision_input FROM import_jobs WHERE id=? AND state='FAILED' AND instance_id='' AND execution_arn!='' AND (SELECT enabled FROM import_controls WHERE id=1)=1`,id).Scan(&input);err!=nil{return err}
 var fields map[string]any
 if err=json.Unmarshal([]byte(input),&fields);err!=nil{return err}
 attempt,_:=fields["retry_attempt"].(float64);fields["retry_attempt"]=int(attempt)+1
 raw,_:=json.Marshal(fields)
 _,err=tx.ExecContext(ctx,`UPDATE import_jobs SET state='PROVISIONING',provision_input=?,execution_arn='',error=? WHERE id=?`,string(raw),"administrator provisioning reconciliation: "+reason,id)
 if err!=nil{return err};return tx.Commit()
}
