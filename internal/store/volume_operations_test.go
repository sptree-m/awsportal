package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestVolumePerformanceRequiresOwnershipAndHoldsTermination(t *testing.T) {
	s, admin, users, eid, now := sharedFixture(t)
	ctx := context.Background()
	approveGolden(t, s, admin, "shared-cpu-v1")
	if err := s.SetPoolControl(ctx, admin, eid, false, true, "accepted pilot", "test"); err != nil {
		t.Fatal(err)
	}
	_, err := s.DB.Exec(`INSERT INTO cloud_operations(id,environment_id,instance_id,generation,kind,state,input,created_at,updated_at) VALUES('original',?,1,1,'PROVISION','SUCCEEDED','{}',?,?);INSERT INTO dynamic_instances(instance_id,operation_id) VALUES(1,'original');INSERT INTO instance_resources(instance_id,resource_id) VALUES(1,'vol-test')`, eid, now.Unix(), now.Unix())
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []struct {
		u                User
		volume           string
		iops, throughput int64
		reason           string
	}{{users[0], "vol-test", 3000, 125, "test"}, {admin, "vol-other", 3000, 125, "test"}, {admin, "vol-test", 3000, 1000, "test"}, {admin, "vol-test", 3000, 125, ""}} {
		if err = s.RequestVolumePerformance(ctx, request.u, 1, request.volume, request.iops, request.throughput, request.reason); err == nil {
			t.Fatal("unsafe volume request accepted")
		}
	}
	if err = s.RequestVolumePerformance(ctx, admin, 1, "vol-test", 3000, 125, "lower after preparation"); err != nil {
		t.Fatal(err)
	}
	if err = s.RequestVolumePerformance(ctx, admin, 1, "vol-test", 16000, 1000, "overlap"); err == nil {
		t.Fatal("overlapping modification accepted")
	}
	if _, err = s.DB.Exec(`UPDATE environment_instances SET idle_since=? WHERE instance_id=1`, now.Add(-time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if err = s.ReservePoolOperations(ctx, now, false, true); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.DB.QueryRow(`SELECT COUNT(*) FROM cloud_operations WHERE kind='TERMINATE'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("modifying instance selected for termination", err, count)
	}
	ops, err := s.VolumeOperations(ctx)
	if err != nil || len(ops) != 1 {
		t.Fatal(err, ops)
	}
	id := ops[0].ID
	if err = s.MarkVolumeOperation(ctx, id, "FAILED", ""); err != nil {
		t.Fatal(err)
	}
	if err = s.RetryVolumeOperation(ctx, admin, id, "AWS modification checked"); err != nil {
		t.Fatal(err)
	}
	ops, _ = s.VolumeOperations(ctx)
	if ops[0].ID != id || !strings.Contains(ops[0].Input, `"retry_attempt":1`) {
		t.Fatal("retry lost frozen operation identity", ops)
	}
}
func TestLoadMetricsIgnoreBootChangesAndGaps(t *testing.T) {
	s, _, _, _, now := sharedFixture(t)
	ctx := context.Background()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for n, offset := range []time.Duration{0, time.Minute, 2 * time.Minute, 5 * time.Minute, 6 * time.Minute} {
		r := sample(int64(n+10), now.Add(offset))
		r.CPU = float64(n * 10)
		r.Memory = float64(n * 20)
		if n == 4 {
			r.BootID = "another-boot"
		}
		if err = recordLoadMinute(ctx, tx, 1, r, now.Add(offset)); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	m, err := s.OperationalMetrics(ctx, now.Add(6*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if m.LoadSeconds != 120 || m.LoadCount != 2 || m.CPUAverage != 15 || m.CPUP95 != 20 || m.MemoryAverage != 30 {
		t.Fatalf("unexpected metrics: %+v", m)
	}
}
