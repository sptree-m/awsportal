package shared

import (
	"context"
	"errors"
	"testing"

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
