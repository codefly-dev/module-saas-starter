package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"policy-log/mirror"
	"policy-log/objectstore"
	"policy-log/objectstore/memory"
	"policy-log/witness"
)

type sinkFunc func(context.Context, witness.Record) error

func (f sinkFunc) Write(ctx context.Context, r witness.Record) error { return f(ctx, r) }

type headFailure struct{ objectstore.Store }

type countingMirror struct{ offers int }

func (m *countingMirror) Offer(witness.Record) { m.offers++ }

func (s headFailure) Write(ctx context.Context, name string, b []byte, g int64) (int64, error) {
	if name == witness.HeadObject {
		return 0, objectstore.ErrPrecondition
	}
	return s.Store.Write(ctx, name, b, g)
}

func newService(t *testing.T, fail bool, sink mirror.Sink) (*Service, *memory.Store, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	store := memory.New()
	genesis, _ := json.Marshal(struct {
		Seq       uint64 `json:"seq"`
		ChainHash []byte `json:"chain_hash"`
	}{ChainHash: make([]byte, 32)})
	if _, err := store.Write(ctx, witness.HeadObject, genesis, 0); err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var objects objectstore.Store = store
	if fail {
		objects = headFailure{store}
	}
	log, err := witness.Open(ctx, objects, key)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := mirror.Start(ctx, sink, func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(log, worker)
	if err != nil {
		t.Fatal(err)
	}
	return s, store, cancel
}

func operation() witness.Entry {
	h := sha256.Sum256([]byte("narrowing bytes"))
	return witness.Entry{OperationID: "example-operation", Kind: "narrowed", Subject: "example-principal", Tenant: "example-tenant", NarrowingHash: h[:], RequestedAt: time.Now().UTC()}
}

func TestCommittedEntryIsMirroredWithTheSameReceipt(t *testing.T) {
	mirrored := make(chan witness.Record, 1)
	s, _, _ := newService(t, false, sinkFunc(func(_ context.Context, r witness.Record) error { mirrored <- r; return nil }))
	e := operation()
	r, err := s.Append(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case row := <-mirrored:
		if row.Entry.OperationID != e.OperationID || row.Receipt.Seq != r.Seq || !ed25519.Verify(s.Keys().PublicKey, witness.ReceiptBytes(row.Receipt), row.Receipt.Signature) {
			t.Fatal("mirror did not receive the committed signed record")
		}
		cursor, err := s.Cursor(context.Background())
		if err != nil || cursor.HeadSeq != row.Receipt.Seq {
			t.Fatalf("mirrored before head commit: %v, %v", cursor, err)
		}
	case <-time.After(time.Second):
		t.Fatal("committed entry was not offered to warehouse")
	}
}

func TestFailedHeadCommitNeverOffersWarehouseRecord(t *testing.T) {
	s, _, _ := newService(t, true, sinkFunc(func(context.Context, witness.Record) error { return nil }))
	mirrored := &countingMirror{}
	s.mirror = mirrored
	r, err := s.Append(context.Background(), operation())
	if r != nil || !errors.Is(err, objectstore.ErrPrecondition) {
		t.Fatalf("head loss returned receipt: %v, %v", r, err)
	}
	if mirrored.offers != 0 {
		t.Fatal("uncommitted entry offered to warehouse")
	}
}

func TestWarehouseFailureCannotWithholdOrMintAReceipt(t *testing.T) {
	entered := make(chan struct{})
	s, _, _ := newService(t, false, sinkFunc(func(ctx context.Context, _ witness.Record) error {
		close(entered)
		<-ctx.Done()
		return errors.New("warehouse unavailable")
	}))
	start := time.Now()
	r, err := s.Append(context.Background(), operation())
	if err != nil || r == nil || time.Since(start) > time.Second || !witness.VerifyReceipt(s.Keys().PublicKey, *r) {
		t.Fatalf("warehouse controlled receipt: %v, %v", r, err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("mirror was never attempted")
	}
}

func TestExpiredAndInvalidRequestsNeverReachMirror(t *testing.T) {
	s, _, _ := newService(t, false, sinkFunc(func(context.Context, witness.Record) error { return nil }))
	mirrored := &countingMirror{}
	s.mirror = mirrored
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if receipt, err := s.Append(ctx, operation()); receipt != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired request: %v, %v", receipt, err)
	}
	bad := operation()
	bad.NarrowingHash = make([]byte, objectstore.MaxObjectBytes+1)
	if receipt, err := s.Append(context.Background(), bad); receipt != nil || !errors.Is(err, witness.ErrInvalid) {
		t.Fatalf("oversized hash: %v, %v", receipt, err)
	}
	if mirrored.offers != 0 {
		t.Fatal("refused request reached mirror")
	}
	cursor, err := s.Cursor(context.Background())
	if err != nil || cursor.HeadSeq != 0 {
		t.Fatalf("refused request advanced head: %v, %v", cursor, err)
	}
}
