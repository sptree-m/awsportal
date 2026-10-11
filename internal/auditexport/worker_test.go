package auditexport

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"github.com/sptree-m/awsportal/internal/store"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func unzipAudit(t *testing.T, payload []byte) []byte {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type fakeSink struct {
	fail     bool
	keys     []string
	payloads [][]byte
}

func (s *fakeSink) Put(_ context.Context, key string, p []byte) error {
	s.keys = append(s.keys, key)
	s.payloads = append(s.payloads, append([]byte(nil), p...))
	if s.fail {
		return errors.New("S3 unavailable")
	}
	return nil
}

func TestRestartRetriesExactBatchAndExportsNewEvents(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "audit.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.Close() }()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	db.Audit(ctx, "alice", "login", "", "deny", "line\nbreak")
	sink := &fakeSink{fail: true}
	w := &Worker{Store: db, Sink: sink, Destination: "bucket/audit/", Prefix: "audit/"}
	if err = w.Tick(ctx); err == nil {
		t.Fatal("expected upload failure")
	}
	db.Close()
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	db.Audit(ctx, "alice", "login", "", "ok", "")
	sink.fail = false
	w.Store = db
	if err = w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sink.keys) != 3 || sink.keys[0] != sink.keys[1] || !bytes.Equal(sink.payloads[0], sink.payloads[1]) {
		t.Fatal("pending batch changed after restart")
	}
	var entry map[string]any
	if !strings.HasSuffix(sink.keys[0], ".jsonl.gz") {
		t.Fatal("missing gzip extension")
	}
	if err = json.Unmarshal(bytes.TrimSpace(unzipAudit(t, sink.payloads[0])), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["actor"] != "alice" || entry["result"] != "deny" || entry["detail"] != "line\nbreak" {
		t.Fatal(entry)
	}
	if err = w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sink.keys) != 3 {
		t.Fatal("acknowledged records exported twice")
	}
	entries, err := db.AuditEntries(ctx, 200)
	if err != nil || len(entries) != 2 {
		t.Fatal("local audit records removed", err)
	}
	// A changed destination gets its own cursor and backfills existing history.
	w.Destination = "other/audit/"
	if err = w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sink.keys) != 4 || bytes.Count(unzipAudit(t, sink.payloads[3]), []byte{'\n'}) != 2 {
		t.Fatal("destination history missing")
	}
}

func TestAuditBatchLimitAndStaleAcknowledgement(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1001; i++ {
		db.Audit(ctx, "user", "login", "", "ok", "")
	}
	b, err := db.PrepareAuditExport(ctx, "bucket/audit/", "audit/")
	if err != nil {
		t.Fatal(err)
	}
	plain := unzipAudit(t, b.Payload)
	if len(b.Payload) >= len(plain) {
		t.Fatal("gzip did not reduce repetitive log size")
	}
	if bytes.Count(plain, []byte{'\n'}) != 1000 {
		t.Fatal("unbounded batch")
	}
	if err = db.CompleteAuditExport(ctx, "bucket/audit/", store.AuditBatch{Key: "stale", LastID: 1001}); err != nil {
		t.Fatal(err)
	}
	retry, err := db.PrepareAuditExport(ctx, "bucket/audit/", "audit/")
	if err != nil || retry.Key != b.Key {
		t.Fatal("stale ack advanced cursor", err)
	}
	if err = db.CompleteAuditExport(ctx, "bucket/audit/", b); err != nil {
		t.Fatal(err)
	}
	next, err := db.PrepareAuditExport(ctx, "bucket/audit/", "audit/")
	if err != nil || next.LastID != 1001 || bytes.Count(unzipAudit(t, next.Payload), []byte{'\n'}) != 1 {
		t.Fatal("batch boundary lost records", err)
	}
}

func TestLegacyPendingJSONLFinishesBeforeGzipBatch(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	db.Audit(ctx, "alice", "login", "", "ok", "")
	legacy := []byte("{\"id\":1,\"action\":\"login\"}\n")
	if _, err = db.DB.ExecContext(ctx, `INSERT INTO audit_export_state(destination,last_id,object_key,payload) VALUES(?,?,?,?)`, "bucket/audit/", 1, "audit/legacy.jsonl", legacy); err != nil {
		t.Fatal(err)
	}
	db.Audit(ctx, "alice", "logout", "", "ok", "")
	sink := &fakeSink{fail: true}
	w := &Worker{Store: db, Sink: sink, Destination: "bucket/audit/", Prefix: "audit/"}
	if err = w.Tick(ctx); err == nil {
		t.Fatal("expected failure")
	}
	sink.fail = false
	if err = w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sink.keys) != 3 || sink.keys[0] != "audit/legacy.jsonl" || sink.keys[1] != sink.keys[0] || !bytes.Equal(sink.payloads[0], legacy) || !bytes.Equal(sink.payloads[1], legacy) {
		t.Fatal("legacy pending upload changed")
	}
	if !strings.HasSuffix(sink.keys[2], ".jsonl.gz") || !bytes.Contains(unzipAudit(t, sink.payloads[2]), []byte(`"action":"logout"`)) {
		t.Fatal("new batch was not gzip")
	}
}

func TestNormalizePrefix(t *testing.T) {
	for _, p := range []string{"", "/audit", "audit//x", "audit/../x", "audit\nx", "audit\\x"} {
		if _, err := NormalizePrefix(p); err == nil {
			t.Fatalf("accepted %q", p)
		}
	}
	for _, p := range []string{"audit", "audit/", "portal/audit/"} {
		got, err := NormalizePrefix(p)
		if err != nil || got[len(got)-1] != '/' {
			t.Fatal(p, err)
		}
	}
}
