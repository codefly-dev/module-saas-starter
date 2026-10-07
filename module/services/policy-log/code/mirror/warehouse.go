// Package mirror implements the best-effort warehouse projection. The warehouse
// receives receipts; it never produces them or participates in the head commit.
package mirror

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"policy-log/witness"
)

// Writer follows the repository's warehouse convention: one JSON object per
// POST with a dedicated bearer credential. No host sink or credential is reused.
type Writer struct {
	endpoint, token string
	client          *http.Client
}

func NewWriter(endpoint, token string) (*Writer, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || token == "" {
		return nil, errors.New("policy-log: warehouse HTTPS endpoint and dedicated writer token are required")
	}
	return &Writer{endpoint: endpoint, token: token, client: &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (w *Writer) Write(ctx context.Context, record witness.Record) error {
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+w.token)
	response, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	// A broken sink cannot make its response an unbounded read either.
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("policy-log: warehouse HTTP %d", response.StatusCode)
	}
	return nil
}

type Sink interface {
	Write(context.Context, witness.Record) error
}

// Worker has bounded memory and concurrency. Dropped or failed mirrors are
// reported and repairable from Read. It has no durable authority of its own.
type Worker struct {
	queue   chan witness.Record
	failed  func(error)
	done    <-chan struct{}
	dropped atomic.Uint64
}

func Start(ctx context.Context, sink Sink, failed func(error)) (*Worker, error) {
	if sink == nil || failed == nil {
		return nil, errors.New("policy-log: mirror sink and failure reporter required")
	}
	w := &Worker{queue: make(chan witness.Record, 64), failed: failed, done: ctx.Done()}
	go func() {
		var reported uint64
		for {
			select {
			case <-ctx.Done():
				return
			case record := <-w.queue:
				if dropped := w.dropped.Load(); dropped > reported {
					failed(fmt.Errorf("policy-log: dropped %d warehouse mirrors; repair from Read", dropped-reported))
					reported = dropped
				}
				attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
				err := sink.Write(attempt, record)
				cancel()
				if err != nil {
					failed(err)
				}
			}
		}
	}()
	return w, nil
}

// Offer must be called only after Append returned a committed receipt. It never
// waits for warehouse I/O and never changes an append's outcome.
func (w *Worker) Offer(record witness.Record) {
	select {
	case <-w.done:
		w.dropped.Add(1)
		return
	default:
	}
	record.Entry.NarrowingHash = bytes.Clone(record.Entry.NarrowingHash)
	record.Receipt.EntryHash = bytes.Clone(record.Receipt.EntryHash)
	record.Receipt.PrevHash = bytes.Clone(record.Receipt.PrevHash)
	record.Receipt.ChainHash = bytes.Clone(record.Receipt.ChainHash)
	record.Receipt.Signature = bytes.Clone(record.Receipt.Signature)
	select {
	case w.queue <- record:
	default:
		// The reporting callback may itself block on logging. It must never
		// run on the append path; expose the monotonic drop count instead.
		w.dropped.Add(1)
	}
}

func (w *Worker) Dropped() uint64 { return w.dropped.Load() }
