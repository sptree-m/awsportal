package store

import (
	"bytes"
	"context"
	"fmt"
	"github.com/parquet-go/parquet-go"
	"github.com/sptree-m/awsportal/internal/billing"
	"io"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"
)

func approveGolden(t *testing.T, s *Store, u User, profile string) {
	t.Helper()
	ctx := context.Background()
	if err := s.RegisterGolden(ctx, u, GoldenImage{ProfileID: profile, Version: "test1", AMI: "ami-aaaaaaaa", TemplateID: "lt-aaaaaaaa", TemplateVersion: "1", Checksum: strings.Repeat("a", 64), ValidationRef: "acceptance-test-artifact", Channel: "NEXT"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PromoteGolden(ctx, u, profile, "test1", "unit test"); err != nil {
		t.Fatal(err)
	}
}
func TestPoolAtomicCeilingIncludesUncertainAndLaunchingSeats(t *testing.T) {
	s, a, us, eid, now := sharedFixture(t)
	ctx := context.Background()
	approveGolden(t, s, a, "shared-cpu-v1")
	if err := s.SetPoolControl(ctx, a, eid, true, false, "two-user-five-day-evidence", "test"); err != nil {
		t.Fatal(err)
	}
	s.DB.Exec(`UPDATE environment_instances SET lifecycle='UNKNOWN'`)
	for n := 0; n < 10; n++ {
		var u User
		if n < len(us) {
			u = us[n]
		} else {
			name := fmt.Sprintf("extra%d", n)
			s.CreateUser(ctx, name, "x", "user", "")
			u, _ = s.UserByName(ctx, name)
			s.SetEnvironmentACL(ctx, a, eid, "user", u.ID, "environment.connect", false, "test")
			s.SetUserStorage(ctx, a, u.ID, fmt.Sprintf("fs-%08x", u.ID), fmt.Sprintf("fsap-%08x", u.ID), "test")
		}
		if _, err := s.RequestEnvironment(ctx, u, eid, "pool-waiting", now); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	fail := make(chan error, 20)
	for n := 0; n < 20; n++ {
		wg.Add(1)
		go func() { defer wg.Done(); fail <- s.ReservePoolOperations(ctx, now, true, false) }()
	}
	wg.Wait()
	close(fail)
	for err := range fail {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	s.DB.QueryRow(`SELECT COUNT(*) FROM cloud_operations WHERE kind='PROVISION'`).Scan(&count)
	if count != 4 {
		t.Fatal("five total resources, including fixed", count)
	}
	s.DB.Exec(`UPDATE cloud_operations SET state='QUARANTINED'`)
	if err := s.ReservePoolOperations(ctx, now.Add(time.Minute), true, false); err != nil {
		t.Fatal(err)
	}
	s.DB.QueryRow(`SELECT COUNT(*) FROM cloud_operations`).Scan(&count)
	if count != 4 {
		t.Fatal("uncertain capacity incorrectly freed", count)
	}
}
func TestPoolFlagsCancelOnlyBeforeSubmission(t *testing.T) {
	s, a, _, eid, now := sharedFixture(t)
	ctx := context.Background()
	approveGolden(t, s, a, "shared-cpu-v1")
	s.SetPoolControl(ctx, a, eid, true, true, "evidence", "test")
	s.DB.Exec(`INSERT INTO cloud_operations(id,environment_id,generation,kind,input,created_at,updated_at) VALUES('aaaa',?,1,'PROVISION','{}',?,?)`, eid, now.Unix(), now.Unix())
	ok, err := s.MarkCloudSubmitting(ctx, "aaaa")
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	s.DB.Exec(`INSERT INTO cloud_operations(id,environment_id,generation,kind,input,created_at,updated_at) VALUES('bbbb',?,1,'PROVISION','{}',?,?)`, eid, now.Unix(), now.Unix())
	if err = s.SetPoolControl(ctx, a, eid, false, false, "", "off"); err != nil {
		t.Fatal(err)
	}
	var state string
	s.DB.QueryRow(`SELECT state FROM cloud_operations WHERE id='aaaa'`).Scan(&state)
	if state != "SUBMITTING" {
		t.Fatal("uncertain submission lost", state)
	}
	ok, err = s.MarkCloudSubmitting(ctx, "bbbb")
	if ok || err != nil {
		t.Fatal("cancelled operation submitted", ok, err)
	}
}
func TestDrainCancelledByNewRequestAndGenerationProof(t *testing.T) {
	s, a, users, eid, now := sharedFixture(t)
	ctx := context.Background()
	approveGolden(t, s, a, "shared-cpu-v1")
	s.SetPoolControl(ctx, a, eid, true, true, "evidence", "test")
	s.DB.Exec(`INSERT INTO cloud_operations(id,environment_id,instance_id,generation,kind,state,input,created_at,updated_at) VALUES(?, ?,1,1,'TERMINATE','DRAINING','{}',?,?)`, strings.Repeat("a", 32), eid, now.Unix(), now.Unix())
	s.DB.Exec(`UPDATE environment_instances SET lifecycle='DRAINING',idle_since=?`, now.Add(-16*time.Minute).Unix())
	o := CloudOperation{ID: strings.Repeat("a", 32), InstanceID: 1, EnvironmentID: eid, Generation: 1, Kind: "TERMINATE", State: "DRAINING"}
	r := sample(7, now)
	r.CPU = 0
	r.DrainOperation = o.ID
	r.DrainComplete = true
	if err := s.EnvironmentHeartbeat(ctx, "i-shared", r, now); err != nil {
		t.Fatal(err)
	}
	ready, err := s.ReconcileDrain(ctx, o, now)
	if err != nil || !ready {
		t.Fatal(ready, err)
	}
	agent, err := s.EnvironmentAgentState(ctx, "i-shared")
	if err != nil || agent.DrainOperation != o.ID {
		t.Fatal("drain fence lost after proof", agent, err)
	}
	if _, err = s.RequestEnvironment(ctx, users[0], eid, "new-demand", now); err != nil {
		t.Fatal(err)
	}
	ok, err := s.MarkCloudSubmitting(ctx, o.ID)
	if ok || err != nil {
		t.Fatal("new demand did not cancel termination", ok, err)
	}
}
func TestPersonalLeaseSurvivesAgentGap(t *testing.T) {
	s, _, us, eid, now := sharedFixture(t)
	ctx := context.Background()
	ok, err := s.ClaimPersonalHome(ctx, "i-shared", us[0].ID)
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	r, err := s.RequestEnvironment(ctx, us[0], eid, "personal-held", now.Add(2*time.Minute))
	if err != nil || r.AssignmentID != 0 || !strings.Contains(r.Reason, "HOME held") {
		t.Fatal(r, err)
	}
	if err = s.ConfirmPersonalStopped(ctx, "i-shared", "running"); err == nil {
		t.Fatal("running HOME lease freed")
	}
	if err = s.ConfirmPersonalStopped(ctx, "i-shared", "stopped"); err != nil {
		t.Fatal(err)
	}
}
func TestParquetRetryStableAndSevenDayRetentionAfterAck(t *testing.T) {
	s, _, _, _, now := sharedFixture(t)
	ctx := context.Background()
	first, err := s.PrepareParquet(ctx, now)
	if err != nil || len(first) != 1 {
		t.Fatal(len(first), err)
	}
	again, err := s.PrepareParquet(ctx, now)
	if err != nil || len(again) != 1 || again[0].Key != first[0].Key || !bytes.Equal(again[0].Payload, first[0].Payload) {
		t.Fatal("batch changed on retry", err)
	}
	reader := parquet.NewGenericReader[usageRow](bytes.NewReader(first[0].Payload))
	rows := make([]usageRow, 6)
	n, err := reader.Read(rows)
	reader.Close()
	if n != 6 || err != nil && err != io.EOF {
		t.Fatal(n, err)
	}
	if err = s.RetainUsage(ctx, now.Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var count int
	s.DB.QueryRow(`SELECT COUNT(*) FROM instance_samples`).Scan(&count)
	if count != 6 {
		t.Fatal("unexported samples pruned")
	}
	if err = s.CompleteParquet(ctx, first[0].Key); err != nil {
		t.Fatal(err)
	}
	if err = s.RetainUsage(ctx, now.Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	s.DB.QueryRow(`SELECT COUNT(*) FROM instance_samples`).Scan(&count)
	if count != 0 {
		t.Fatal("exported old samples retained", count)
	}
}
func TestBillingSignedRunImmutableAndResidualBlocksFinal(t *testing.T) {
	s, a, us, _, now := sharedFixture(t)
	ctx := context.Background()
	scope := billing.Scope{Account: "123", Currency: "USD", Basis: "unblended", Start: now.Add(-time.Hour), End: now.Add(time.Hour)}
	s.DB.Exec(`INSERT INTO resource_registry(resource_id,owner_user_id,valid_from) VALUES('vol-aaaaaaaa',?,?)`, us[0].ID, scope.Start.Unix())
	header := "identity_line_item_id,line_item_usage_account_id,line_item_currency_code,line_item_line_item_type,line_item_resource_id,line_item_usage_start_date,line_item_usage_end_date,line_item_unblended_cost\n"
	line := func(id, resource, amount string) string {
		return fmt.Sprintf("%s,123,USD,Usage,%s,%s,%s,%s\n", id, resource, scope.Start.Format(time.RFC3339), scope.End.Format(time.RFC3339), amount)
	}
	data := header + line("one", "vol-aaaaaaaa", "1.123456789") + line("two", "vol-aaaaaaaa", "-0.000000789")
	id, err := s.ImportBilling(ctx, a, "source@v1", scope, BillingPolicy{}, strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinalizeBilling(ctx, a, id); err != nil {
		t.Fatal(err)
	}
	same, err := s.ImportBilling(ctx, a, "source@v1", scope, BillingPolicy{}, strings.NewReader(data))
	if err != nil || same != id {
		t.Fatal("source replay created new run", same, err)
	}
	var actual, allocated string
	s.DB.QueryRow(`SELECT actual_micros FROM billing_runs WHERE id=?`, id).Scan(&actual)
	s.DB.QueryRow(`SELECT micros FROM billing_allocations WHERE run_id=?`, id).Scan(&allocated)
	if actual != "1123456" || actual != allocated {
		t.Fatal(actual, allocated)
	}
	unmapped, err := s.ImportBilling(ctx, a, "source@v2", scope, BillingPolicy{}, strings.NewReader(header+line("unknown", "vol-bbbbbbbb", "0.01")))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinalizeBilling(ctx, a, unmapped); err == nil {
		t.Fatal("unallocated run finalized")
	}
	amounts := map[int64]*big.Rat{1: big.NewRat(-1, 3000000), 2: big.NewRat(2, 3000000)}
	out := settleRationals(amounts, big.NewRat(1, 3000000))
	sum := new(big.Int)
	for _, v := range out {
		sum.Add(sum, v)
	}
	if sum.Sign() != 0 {
		t.Fatal("signed fractional rounding", out)
	}
}
func TestImportStateAndCleanupProof(t *testing.T) {
	s, a, _, _, now := sharedFixture(t)
	ctx := context.Background()
	approveGolden(t, s, a, "windows-box-v1")
	s.SetImportEnabled(ctx, a, true)
	id, err := s.RequestImport(ctx, a, "import-key", `C:\Users\AwsImportAdmin\Box\dataset`, "approved-bucket", "datasets/test/", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.MarkImportProvision(ctx, id, "execution"); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("b", 64)
	if err = s.FinishImportProvision(ctx, id, CloudResult{InstanceID: "i-aaaaaaaa", TokenHash: DCVTokenHash(token)}, false); err != nil {
		t.Fatal(err)
	}
	if err = s.ImportAgentEvent(ctx, token, "SUCCEEDED", 1, ImportEvidence{}, now); err == nil {
		t.Fatal("agent asserted success")
	}
	if err = s.ImportAgentEvent(ctx, token, "WAITING_OFFLINE_READY", 1, ImportEvidence{}, now); err != nil {
		t.Fatal(err)
	}
	e := ImportEvidence{ExpectedManifest: "immutable-source", ExpectedFiles: 1, ExpectedBytes: 3}
	if err = s.ImportAgentEvent(ctx, token, "UPLOADING", 2, e, now); err != nil {
		t.Fatal(err)
	}
	if err = s.ImportAgentEvent(ctx, token, "UPLOADING", 2, e, now); err != nil {
		t.Fatal("exact retry rejected", err)
	}
	changed := e
	changed.ExpectedBytes++
	if err = s.ImportAgentEvent(ctx, token, "UPLOADING", 2, changed, now); err == nil {
		t.Fatal("changed retry accepted")
	}
	changed = e
	changed.ExpectedManifest = "changed-after-upload-start"
	changed.Validation = "validation"
	changed.Logs = "logs"
	if err = s.ImportAgentEvent(ctx, token, "VALIDATING", 3, changed, now); err == nil {
		t.Fatal("frozen source changed")
	}
	e.Validation = "immutable-validation"
	e.Logs = "immutable-logs"
	if err = s.ImportAgentEvent(ctx, token, "VALIDATING", 3, e, now); err != nil {
		t.Fatal(err)
	}
	if err = s.VerifyImport(ctx, id, e); err == nil {
		t.Fatal("unverified dataset published")
	}
	e.UploadedFiles = 1
	e.UploadedBytes = 3
	e.ChecksumsVerified = true
	if err = s.VerifyImport(ctx, id, e); err != nil {
		t.Fatal(err)
	}
	if err = s.StartImportCleanup(ctx, id, "cleanup"); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishImportCleanup(ctx, id, CloudResult{ResourcesGone: true}, false); err != nil {
		t.Fatal(err)
	}
	var state string
	s.DB.QueryRow(`SELECT state FROM import_jobs WHERE id=?`, id).Scan(&state)
	if state != "CLEANUP_FAILED" {
		t.Fatal("success before credential revocation", state)
	}
}

func TestBillingStreamDoesNotHoldPortalDatabase(t *testing.T) {
	s, a, us, _, now := sharedFixture(t)
	ctx := context.Background()
	scope := billing.Scope{Account: "123", Currency: "USD", Basis: "unblended", Start: now.Add(-time.Hour), End: now.Add(time.Hour)}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	done := make(chan error, 1)
	go func() {
		_, err := s.ImportBilling(ctx, a, "stream-source", scope, BillingPolicy{NonResource: map[string]string{"Tax": "fallback_owner"}, FallbackUserID: us[0].ID}, reader)
		done <- err
	}()
	_, err := io.WriteString(writer, "identity_line_item_id,line_item_usage_account_id,line_item_currency_code,line_item_line_item_type,line_item_resource_id,line_item_usage_start_date,line_item_usage_end_date,line_item_unblended_cost\n"+fmt.Sprintf("one,123,USD,Tax,,%s,%s,0.01\n", scope.Start.Format(time.RFC3339), scope.End.Format(time.RFC3339)))
	if err != nil {
		t.Fatal(err)
	}
	timeout, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var n int
	if err = s.DB.QueryRowContext(timeout, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		t.Fatal("slow S3 blocks database", err)
	}
	writer.Close()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}
func TestFailedImportManualCleanupNeverPublishesDataset(t *testing.T) {
	s, a, _, _, now := sharedFixture(t)
	ctx := context.Background()
	approveGolden(t, s, a, "windows-box-v1")
	s.SetImportEnabled(ctx, a, true)
	id, err := s.RequestImport(ctx, a, "failed-import-key", `C:\Users\AwsImportAdmin\Box\dataset`, "approved-bucket", "datasets/failed/", now)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("d", 64)
	s.MarkImportProvision(ctx, id, "execution")
	if err = s.FinishImportProvision(ctx, id, CloudResult{InstanceID: "i-bbbbbbbb", TokenHash: DCVTokenHash(token)}, false); err != nil {
		t.Fatal(err)
	}
	if err = s.CancelImport(ctx, a, id); err != nil {
		t.Fatal(err)
	}
	if err = s.RequestImportCleanup(ctx, a, id, "investigation finished"); err != nil {
		t.Fatal(err)
	}
	s.StartImportCleanup(ctx, id, "cleanup")
	if err = s.FinishImportCleanup(ctx, id, CloudResult{ResourcesGone: true, CredentialRevoked: true}, false); err != nil {
		t.Fatal(err)
	}
	var state, hash string
	s.DB.QueryRow(`SELECT state,token_hash FROM import_jobs WHERE id=?`, id).Scan(&state, &hash)
	if state != "CANCELLED" || hash != "" {
		t.Fatal(state, hash)
	}
	var n int
	s.DB.QueryRow(`SELECT COUNT(*) FROM datasets WHERE source_job=?`, id).Scan(&n)
	if n != 0 {
		t.Fatal("cancelled import published dataset")
	}
}

func TestOperationalMetricsIdleIntervalsExcludeGaps(t *testing.T) {
 s,_,_,_,now:=sharedFixture(t);ctx:=context.Background()
 record:=func(at time.Time,reason string,idle bool){tx,err:=s.DB.BeginTx(ctx,nil);if err!=nil{t.Fatal(err)};if err=recordOperationObservation(ctx,tx,1,reason,idle,at);err!=nil{t.Fatal(err)};if err=tx.Commit();err!=nil{t.Fatal(err)}}
 start:=now.Add(-10*time.Minute).Truncate(time.Minute)
 record(start,"idle observation only; automatic termination disabled",true)
 record(start.Add(time.Minute),"idle observation only; automatic termination disabled",true)
 record(start.Add(2*time.Minute),"metrics unavailable or stale",false)
 record(start.Add(3*time.Minute),"idle observation only; automatic termination disabled",true)
 record(start.Add(4*time.Minute),"idle observation only; automatic termination disabled",true)
 record(start.Add(7*time.Minute),"idle observation only; automatic termination disabled",true)
 m,err:=s.OperationalMetrics(ctx,now);if err!=nil{t.Fatal(err)}
 if m.IdleSeconds!=120||m.MeasuredSeconds!=120{t.Fatal(m)}
}
func TestCancelledImportCannotBeSubmittedByStaleWorker(t *testing.T) {
 s,a,_,_,now:=sharedFixture(t);ctx:=context.Background()
 approveGolden(t,s,a,"windows-box-v1");s.SetImportEnabled(ctx,a,true)
 id,err:=s.RequestImport(ctx,a,"cancel-before-submit",`C:\Users\AwsImportAdmin\Box\dataset`,"approved-bucket","datasets/cancel-submit/",now);if err!=nil{t.Fatal(err)}
 if err=s.CancelImport(ctx,a,id);err!=nil{t.Fatal(err)}
 if err=s.MarkImportProvision(ctx,id,"");err==nil{t.Fatal("cancelled import submitted")}
}
