package auditexport

import (
	"context"
	"fmt"
	"github.com/sptree-m/awsportal/internal/store"
	"log"
	"strings"
	"time"
)

type Sink interface {
	Put(context.Context, string, []byte) error
}
type Worker struct {
	Store               *store.Store
	Sink                Sink
	Destination, Prefix string
	Interval            time.Duration
}

func NormalizePrefix(prefix string) (string, error) {
	prefix = strings.TrimSuffix(prefix, "/")
	if prefix == "" || strings.HasPrefix(prefix, "/") || strings.ContainsAny(prefix, "\\\r\n") {
		return "", fmt.Errorf("invalid audit prefix")
	}
	for _, part := range strings.Split(prefix, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("invalid audit prefix")
		}
	}
	return prefix + "/", nil
}

func (w *Worker) Tick(ctx context.Context) error {
	// Bound each pass; a backlog drains over successive passes without monopolizing DB.
	for i := 0; i < 10; i++ {
		b, err := w.Store.PrepareAuditExport(ctx, w.Destination, w.Prefix)
		if err != nil {
			return err
		}
		if b.Key == "" {
			return nil
		}
		if err = w.Sink.Put(ctx, b.Key, b.Payload); err != nil {
			return err
		}
		if err = w.Store.CompleteAuditExport(ctx, w.Destination, b); err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) Run(ctx context.Context) {
	interval := w.Interval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	run := func() {
		timeout, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		if err := w.Tick(timeout); err != nil {
			log.Print("audit S3 export failed; pending batch retained for retry")
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
