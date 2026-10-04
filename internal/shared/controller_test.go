package shared

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sptree-m/awsportal/internal/store"
)

type retrySink struct {
	calls int
	fail  bool
	keys  []string
}

func (s *retrySink) Put(_ context.Context, key string, b []byte) error {
	s.calls++
	s.keys = append(s.keys, key)
	if s.fail {
		return errors.New("offline")
	}
	return nil
}
func TestExportSurvivesFailureAndControllerRestart(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = db.DB.Exec(`INSERT INTO usage_export_queue(object_key,payload) VALUES('usage/v2/i-a/boot/1.json','{"sequence":1}')`)
	if err != nil {
		t.Fatal(err)
	}
	sink := &retrySink{fail: true}
	c := &Controller{Store: db, Sink: sink}
	if err = c.Export(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err := db.PendingUsageExports(ctx)
	if err != nil || len(pending) != 1 || pending[0].Retries != 1 {
		t.Fatal(pending, err)
	}
	sink.fail = false
	c = &Controller{Store: db, Sink: sink}
	if err = c.Export(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err = db.PendingUsageExports(ctx)
	if err != nil || len(pending) != 0 || sink.calls != 2 || sink.keys[0] != sink.keys[1] {
		t.Fatal(pending, sink, err)
	}
	if err = c.Export(ctx); err != nil || sink.calls != 2 {
		t.Fatal("exported item sent again")
	}
}

type blockedSink struct{ entered chan struct{} }

func (s *blockedSink) Put(ctx context.Context, _ string, _ []byte) error {
	close(s.entered)
	<-ctx.Done()
	return ctx.Err()
}
func TestSlowExportDoesNotBlockReconciliation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = db.DB.Exec(`INSERT INTO groups(id,name) VALUES(1,'pilot');
 INSERT INTO environments(id,name,mode,group_id,profile_id) VALUES(1,'pilot','shared',1,'shared-cpu-v1');
 INSERT INTO instances(id,instance_id,name,dcv_host) VALUES(1,'i-a','pilot','pilot.example');
 INSERT INTO environment_instances(instance_id,environment_id,idle_reason) VALUES(1,1,'not yet reconciled');
 INSERT INTO usage_export_queue(object_key,payload) VALUES('usage/v2/i-a/boot/1.json','{}');`)
	if err != nil {
		t.Fatal(err)
	}
	sink := &blockedSink{entered: make(chan struct{})}
	c := &Controller{Store: db, Sink: sink}
	ticks, exports := make(chan time.Time, 1), make(chan time.Time, 1)
	done := make(chan struct{})
	go func() { c.run(ctx, ticks, exports); close(done) }()
	exports <- time.Now()
	select {
	case <-sink.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("export did not start")
	}
	ticks <- time.Now()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("S3 request blocked the controller")
		case <-poll.C:
			var reason string
			if err = db.DB.QueryRow(`SELECT idle_reason FROM environment_instances WHERE instance_id=1`).Scan(&reason); err != nil {
				t.Fatal(err)
			}
			if reason == "metrics unavailable or stale" {
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("controller did not stop")
				}
				return
			}
		}
	}
}
