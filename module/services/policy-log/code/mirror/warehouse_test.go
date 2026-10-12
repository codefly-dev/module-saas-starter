package mirror

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"policy-log/witness"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type sinkFunc func(context.Context, witness.Record) error

func (f sinkFunc) Write(ctx context.Context, r witness.Record) error { return f(ctx, r) }

func TestWarehouseWriterUsesDedicatedCredentialAndCommittedRecord(t *testing.T) {
	w, err := NewWriter("https://warehouse.example/policy-log", "example-writer-token")
	if err != nil {
		t.Fatal(err)
	}
	w.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer example-writer-token" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("wrong warehouse request: %s, %v", r.Method, r.Header)
		}
		var record witness.Record
		if err := json.NewDecoder(r.Body).Decode(&record); err != nil || record.Receipt.Seq != 7 {
			t.Errorf("lost receipt: %+v, %v", record, err)
		}
		return &http.Response{StatusCode: 204, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	if err := w.Write(context.Background(), witness.Record{Receipt: witness.Receipt{Seq: 7}}); err != nil {
		t.Fatal(err)
	}
}

func TestWarehouseWriterRefusesRedirectsAndUnsafeConfiguration(t *testing.T) {
	for _, endpoint := range []string{"", "http://warehouse.example/log", "https://user:password@warehouse.example/log", "https://warehouse.example/log#fragment"} {
		if _, err := NewWriter(endpoint, "example-token"); err == nil {
			t.Fatalf("accepted %q", endpoint)
		}
	}
	if _, err := NewWriter("https://warehouse.example/log", ""); err == nil {
		t.Fatal("accepted absent credential")
	}
	w, err := NewWriter("https://warehouse.example/log", "example-token")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	w.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://other.example/steal"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	if err := w.Write(context.Background(), witness.Record{}); err == nil || calls.Load() != 1 {
		t.Fatalf("redirect followed: calls=%d, err=%v", calls.Load(), err)
	}
}

func TestSlowFailingMirrorNeverBlocksOffer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	sink := sinkFunc(func(ctx context.Context, _ witness.Record) error {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-ctx.Done()
		return ctx.Err()
	})
	w, err := Start(ctx, sink, func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	w.Offer(witness.Record{})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("mirror not called")
	}
	done := make(chan struct{})
	go func() {
		for range 1000 {
			w.Offer(witness.Record{})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("mirror blocked append path")
	}
	if w.Dropped() == 0 {
		t.Fatal("queue was not bounded or losses hidden")
	}
}

func TestWarehouseFailureIsReportedOutsideAppendPath(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	failure := make(chan error, 1)
	w, err := Start(ctx, sinkFunc(func(context.Context, witness.Record) error { return errors.New("warehouse unavailable") }), func(err error) { failure <- err })
	if err != nil {
		t.Fatal(err)
	}
	w.Offer(witness.Record{})
	select {
	case err := <-failure:
		if err == nil {
			t.Fatal("failure hidden")
		}
	case <-time.After(time.Second):
		t.Fatal("failure not reported")
	}
}
