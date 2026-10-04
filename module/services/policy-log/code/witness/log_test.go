package witness

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"policy-log/objectstore"
	"policy-log/objectstore/memory"
)

func newStore(t *testing.T) *memory.Store {
	t.Helper()
	s := memory.New()
	body, err := json.Marshal(head{ChainHash: make([]byte, sha256.Size)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(context.Background(), HeadObject, body, 0); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMissingHeadRefusesBoot(t *testing.T) {
	if _, err := Open(context.Background(), memory.New(), signingKey(t)); !errors.Is(err, ErrRollback) {
		t.Fatalf("missing head accepted: %v", err)
	}
}

func signingKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func openLog(t *testing.T, store objectstore.Store, key ed25519.PrivateKey) *Log {
	t.Helper()
	l, err := Open(context.Background(), store, key)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func entry(id string) Entry {
	h := sha256.Sum256([]byte("canonical narrowing bytes"))
	return Entry{OperationID: id, Kind: "narrowed", Subject: "example-principal", Tenant: "example-tenant",
		NarrowingHash: h[:], RequestedAt: time.Date(2026, 10, 1, 10, 20, 30, 456, time.UTC)}
}

func appendEntry(t *testing.T, l *Log, e Entry) *Receipt {
	t.Helper()
	r, err := l.Append(context.Background(), e)
	if err != nil || r == nil {
		t.Fatalf("Append = %v, %v", r, err)
	}
	return r
}

// Independently encode the contract, rather than verifying the implementation
// with the encoder that signed it. This is also a minimal client example.
func independentEntryHash(e Entry) []byte {
	b := []byte("policy-log.entry.v1\x00")
	for _, s := range []string{e.OperationID, e.Kind, e.Subject, e.Tenant} {
		b = binary.BigEndian.AppendUint32(b, uint32(len(s)))
		b = append(b, s...)
	}
	b = append(b, e.NarrowingHash...)
	b = binary.BigEndian.AppendUint64(b, uint64(e.RequestedAt.Unix()))
	b = binary.BigEndian.AppendUint32(b, uint32(e.RequestedAt.Nanosecond()))
	h := sha256.Sum256(b)
	return h[:]
}

func independentReceiptBytes(r Receipt) []byte {
	b := binary.BigEndian.AppendUint64([]byte("policy-log.receipt.v1\x00"), r.Seq)
	for _, h := range [][]byte{r.EntryHash, r.PrevHash, r.ChainHash} {
		b = append(b, h...)
	}
	b = binary.BigEndian.AppendUint64(b, uint64(r.IssuedAt.Unix()))
	return binary.BigEndian.AppendUint32(b, uint32(r.IssuedAt.Nanosecond()))
}

func TestReadVerifiesEntireChainAndPagination(t *testing.T) {
	l := openLog(t, newStore(t), signingKey(t))
	for i := 1; i <= 4; i++ {
		appendEntry(t, l, entry(fmt.Sprint(i)))
	}
	var all []Record
	for _, from := range []uint64{1, 3} {
		page, err := l.Read(context.Background(), from, 2)
		if err != nil || len(page) != 2 {
			t.Fatalf("Read(%d) = %v, %v", from, page, err)
		}
		all = append(all, page...)
	}
	prev := make([]byte, sha256.Size)
	for i, r := range all {
		seq := uint64(i + 1)
		hash := independentEntryHash(r.Entry)
		b := binary.BigEndian.AppendUint64([]byte("policy-log.chain.v1\x00"), seq)
		b = append(b, prev...)
		b = append(b, hash...)
		chain := sha256.Sum256(b)
		if r.Receipt.Seq != seq || !bytes.Equal(hash, r.Receipt.EntryHash) ||
			!bytes.Equal(prev, r.Receipt.PrevHash) || !bytes.Equal(chain[:], r.Receipt.ChainHash) {
			t.Fatalf("chain broken at %d", seq)
		}
		prev = chain[:]
	}
	c, err := l.Cursor(context.Background())
	if err != nil || c.HeadSeq != 4 || !bytes.Equal(c.ChainHash, prev) {
		t.Fatalf("cursor = %v, %v", c, err)
	}
	page, err := l.Read(context.Background(), 5, 1)
	if err != nil || len(page) != 0 {
		t.Fatalf("quiet log = %v, %v", page, err)
	}
}

func TestReceiptVerifiesUnderKeysAndRejectsAnotherKey(t *testing.T) {
	l := openLog(t, newStore(t), signingKey(t))
	r := appendEntry(t, l, entry("one"))
	keys := l.Keys()
	if keys.Algorithm != "Ed25519" || len(keys.KeyID) != 64 || !ed25519.Verify(keys.PublicKey, independentReceiptBytes(*r), r.Signature) {
		t.Fatal("receipt does not verify under published key")
	}
	other := signingKey(t).Public().(ed25519.PublicKey)
	if ed25519.Verify(other, independentReceiptBytes(*r), r.Signature) {
		t.Fatal("another key accepted receipt")
	}
	for _, mutate := range []func(*Receipt){
		func(r *Receipt) { r.Seq++ }, func(r *Receipt) { r.EntryHash[0] ^= 1 },
		func(r *Receipt) { r.PrevHash[0] ^= 1 }, func(r *Receipt) { r.ChainHash[0] ^= 1 },
		func(r *Receipt) { r.IssuedAt = r.IssuedAt.Add(time.Nanosecond) }, func(r *Receipt) { r.Signature[0] ^= 1 },
	} {
		altered := cloneReceipt(*r)
		mutate(&altered)
		if VerifyReceipt(keys.PublicKey, altered) {
			t.Fatal("tampered receipt verified")
		}
	}
}

func TestIdempotentReappendReturnsSameReceiptAfterRestart(t *testing.T) {
	s, key := newStore(t), signingKey(t)
	l := openLog(t, s, key)
	first := appendEntry(t, l, entry("same"))
	appendEntry(t, l, entry("later"))
	l = openLog(t, s, key)
	second := appendEntry(t, l, entry("same"))
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("receipt reminted: %v != %v", first, second)
	}
	c, err := l.Cursor(context.Background())
	if err != nil || c.HeadSeq != 2 {
		t.Fatalf("idempotent call advanced head: %v, %v", c, err)
	}
}

func TestOperationIDReuseWithDifferentEntryIsRefused(t *testing.T) {
	l := openLog(t, newStore(t), signingKey(t))
	appendEntry(t, l, entry("same"))
	for _, mutate := range []func(*Entry){
		func(e *Entry) { e.Kind = "regranted" }, func(e *Entry) { e.Subject = "other" },
		func(e *Entry) { e.Tenant = "other" }, func(e *Entry) { e.NarrowingHash[0] ^= 1 },
		func(e *Entry) { e.RequestedAt = e.RequestedAt.Add(time.Nanosecond) },
	} {
		e := entry("same")
		mutate(&e)
		r, err := l.Append(context.Background(), e)
		if r != nil || !errors.Is(err, ErrConflict) {
			t.Fatalf("reused operation accepted: %v, %v", r, err)
		}
	}
}

type interceptStore struct {
	objectstore.Store
	write func(context.Context, string, []byte, int64) (int64, error)
	read  func(context.Context, string) (objectstore.Object, error)
}

func (s *interceptStore) Write(ctx context.Context, name string, b []byte, g int64) (int64, error) {
	if s.write != nil {
		return s.write(ctx, name, b, g)
	}
	return s.Store.Write(ctx, name, b, g)
}
func (s *interceptStore) Read(ctx context.Context, name string) (objectstore.Object, error) {
	if s.read != nil {
		return s.read(ctx, name)
	}
	return s.Store.Read(ctx, name)
}

func TestConcurrentSecondAppenderLosesHeadPreconditionAndIssuesNoReceipt(t *testing.T) {
	s, key := newStore(t), signingKey(t)
	// A crash after the immutable entry leaves a candidate, not a receipt.
	interrupted := &interceptStore{Store: s}
	interrupted.write = func(ctx context.Context, name string, b []byte, g int64) (int64, error) {
		if name == HeadObject {
			return 0, objectstore.ErrPrecondition
		}
		return s.Write(ctx, name, b, g)
	}
	r, err := openLog(t, interrupted, key).Append(context.Background(), entry("same"))
	if r != nil || !errors.Is(err, objectstore.ErrPrecondition) {
		t.Fatalf("failed head write returned receipt: %v, %v", r, err)
	}
	var arrivals atomic.Int32
	ready := make(chan struct{})
	racing := &interceptStore{Store: s}
	racing.write = func(ctx context.Context, name string, b []byte, g int64) (int64, error) {
		if name == HeadObject {
			if arrivals.Add(1) == 2 {
				close(ready)
			}
			select {
			case <-ready:
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}
		return s.Write(ctx, name, b, g)
	}
	a, b := openLog(t, racing, key), openLog(t, racing, key)
	type answer struct {
		receipt *Receipt
		err     error
	}
	answers := make(chan answer, 2)
	for _, l := range []*Log{a, b} {
		go func() { r, e := l.Append(context.Background(), entry("same")); answers <- answer{r, e} }()
	}
	successes, losses := 0, 0
	for range 2 {
		result := <-answers
		switch {
		case result.err == nil && result.receipt != nil:
			successes++
		case errors.Is(result.err, objectstore.ErrPrecondition) && result.receipt == nil:
			losses++
		default:
			t.Fatalf("unexpected append: %+v", result)
		}
	}
	if successes != 1 || losses != 1 || arrivals.Load() != 2 {
		t.Fatalf("successes=%d, losses=%d, head attempts=%d", successes, losses, arrivals.Load())
	}
}

func TestConcurrentDifferentOperationsCannotOverwriteSequence(t *testing.T) {
	s, key := newStore(t), signingKey(t)
	var arrivals atomic.Int32
	ready := make(chan struct{})
	racing := &interceptStore{Store: s}
	racing.write = func(ctx context.Context, name string, b []byte, g int64) (int64, error) {
		if name == EntryObject(1) {
			if arrivals.Add(1) == 2 {
				close(ready)
			}
			select {
			case <-ready:
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}
		return s.Write(ctx, name, b, g)
	}
	a, b := openLog(t, racing, key), openLog(t, racing, key)
	var wg sync.WaitGroup
	var successes, losses atomic.Int32
	for i, l := range []*Log{a, b} {
		wg.Go(func() {
			r, err := l.Append(context.Background(), entry(fmt.Sprint(i)))
			if err == nil && r != nil {
				successes.Add(1)
			} else if errors.Is(err, objectstore.ErrPrecondition) && r == nil {
				losses.Add(1)
			} else {
				t.Errorf("unexpected result: %v, %v", r, err)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 || losses.Load() != 1 {
		t.Fatalf("successes=%d, losses=%d", successes.Load(), losses.Load())
	}
}

func TestUncommittedEntryIsInvisibleAndOnlyExactRetryCanComplete(t *testing.T) {
	s, key := newStore(t), signingKey(t)
	broken := &interceptStore{Store: s, write: func(ctx context.Context, name string, b []byte, g int64) (int64, error) {
		if name == HeadObject {
			return 0, errors.New("head unavailable")
		}
		return s.Write(ctx, name, b, g)
	}}
	r, err := openLog(t, broken, key).Append(context.Background(), entry("original"))
	if err == nil || r != nil {
		t.Fatal("issued receipt before head commit")
	}
	l := openLog(t, s, key)
	rows, err := l.Read(context.Background(), 1, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("uncommitted row visible: %v, %v", rows, err)
	}
	r, err = l.Append(context.Background(), entry("other"))
	if !errors.Is(err, ErrPending) || r != nil {
		t.Fatalf("overwrote pending slot: %v, %v", r, err)
	}
	bad := entry("original")
	bad.Kind = "regranted"
	r, err = l.Append(context.Background(), bad)
	if !errors.Is(err, ErrConflict) || r != nil {
		t.Fatalf("changed pending operation: %v, %v", r, err)
	}
	appendEntry(t, l, entry("original"))
}

func TestCursorIsSignedAndForwardOnlyAcrossAppenders(t *testing.T) {
	s, key := newStore(t), signingKey(t)
	a, b := openLog(t, s, key), openLog(t, s, key)
	c, err := b.Cursor(context.Background())
	if err != nil || c.HeadSeq != 0 || !VerifyCursor(b.Keys().PublicKey, *c) {
		t.Fatalf("genesis cursor: %v, %v", c, err)
	}
	appendEntry(t, a, entry("one"))
	old, _ := s.Read(context.Background(), HeadObject)
	c, err = b.Cursor(context.Background())
	if err != nil || c.HeadSeq != 1 || !VerifyCursor(b.Keys().PublicKey, *c) {
		t.Fatalf("cursor: %v, %v", c, err)
	}
	appendEntry(t, b, entry("two"))
	c, err = a.Cursor(context.Background())
	if err != nil || c.HeadSeq != 2 {
		t.Fatalf("cursor: %v, %v", c, err)
	}
	current, _ := s.Read(context.Background(), HeadObject)
	if _, err := s.Write(context.Background(), HeadObject, old.Data, current.Generation); err != nil {
		t.Fatal(err)
	}
	c, err = a.Cursor(context.Background())
	if c != nil || !errors.Is(err, ErrRollback) {
		t.Fatalf("rollback served: %v, %v", c, err)
	}
	rows, err := a.Read(context.Background(), 1, 1)
	if rows != nil || !errors.Is(err, ErrRollback) {
		t.Fatalf("rollback read served: %v, %v", rows, err)
	}
}

func TestUnreadableHeadRefusesReadsAndIdempotentAppend(t *testing.T) {
	s := &interceptStore{Store: newStore(t)}
	l := openLog(t, s, signingKey(t))
	appendEntry(t, l, entry("one"))
	s.read = func(context.Context, string) (objectstore.Object, error) {
		return objectstore.Object{}, errors.New("unreachable")
	}
	if r, err := l.Append(context.Background(), entry("one")); err == nil || r != nil {
		t.Fatal("cached receipt hid unreachable log")
	}
	if c, err := l.Cursor(context.Background()); err == nil || c != nil {
		t.Fatal("cached cursor hid unreachable log")
	}
	if rows, err := l.Read(context.Background(), 1, 1); err == nil || rows != nil {
		t.Fatal("cached entries hid unreachable log")
	}
}

func TestAppendPastDeadlineIsRefusedWithoutWriting(t *testing.T) {
	s := &interceptStore{Store: newStore(t)}
	l := openLog(t, s, signingKey(t))
	s.write = func(context.Context, string, []byte, int64) (int64, error) {
		t.Error("write after deadline")
		return 0, nil
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	r, err := l.Append(ctx, entry("one"))
	if r != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("late Append: %v, %v", r, err)
	}
}

func TestDeadlineDuringHeadWriteDoesNotCommitLate(t *testing.T) {
	s := newStore(t)
	delayed := &interceptStore{Store: s, write: func(ctx context.Context, name string, b []byte, g int64) (int64, error) {
		if name == HeadObject {
			<-ctx.Done()
		}
		return s.Write(ctx, name, b, g)
	}}
	l := openLog(t, delayed, signingKey(t))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	r, err := l.Append(ctx, entry("one"))
	if r != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("late head write: %v, %v", r, err)
	}
	obj, err := s.Read(context.Background(), HeadObject)
	if err != nil {
		t.Fatal(err)
	}
	var h head
	if err := json.Unmarshal(obj.Data, &h); err != nil || h.Seq != 0 {
		t.Fatalf("late head committed: %v, %v", h, err)
	}
}

func TestAppendWithoutCallerDeadlineIsBoundedAtFiveSeconds(t *testing.T) {
	s := &interceptStore{Store: newStore(t)}
	l := openLog(t, s, signingKey(t))
	s.read = func(ctx context.Context, _ string) (objectstore.Object, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Fatal("append has no five-second budget")
		}
		<-ctx.Done()
		return objectstore.Object{}, ctx.Err()
	}
	start := time.Now()
	r, err := l.Append(context.Background(), entry("one"))
	if r != nil || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5500*time.Millisecond {
		t.Fatalf("unbounded append: %v, %v (%v)", r, err, time.Since(start))
	}
}

func TestWaitingAppenderHonorsItsDeadline(t *testing.T) {
	l := openLog(t, newStore(t), signingKey(t))
	l.gate <- struct{}{}
	defer func() { <-l.gate }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	r, err := l.Append(ctx, entry("one"))
	if r != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued append: %v, %v", r, err)
	}
}

func TestOpenRejectsTamperedHistoryAndAnotherSigningKey(t *testing.T) {
	s, key := newStore(t), signingKey(t)
	l := openLog(t, s, key)
	appendEntry(t, l, entry("one"))
	if _, err := Open(context.Background(), s, signingKey(t)); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("wrong key: %v", err)
	}
	obj, _ := s.Read(context.Background(), EntryObject(1))
	var record Record
	if err := json.Unmarshal(obj.Data, &record); err != nil {
		t.Fatal(err)
	}
	record.Entry.Subject = "tampered"
	body, _ := json.Marshal(record)
	if _, err := s.Write(context.Background(), EntryObject(1), body, obj.Generation); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), s, key); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("tampered history: %v", err)
	}
}

func TestInvalidRequestsAndMutableResultsCannotChangeLog(t *testing.T) {
	l := openLog(t, newStore(t), signingKey(t))
	bad := entry("bad")
	bad.NarrowingHash = []byte{1}
	if r, err := l.Append(context.Background(), bad); r != nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad digest: %v, %v", r, err)
	}
	for _, q := range []struct {
		from  uint64
		limit uint32
	}{{0, 1}, {1, 0}, {1, 501}, {2, 1}} {
		if rows, err := l.Read(context.Background(), q.from, q.limit); err == nil || rows != nil {
			t.Fatalf("bad read accepted: %+v", q)
		}
	}
	first := appendEntry(t, l, entry("one"))
	first.Signature[0] ^= 1
	rows, err := l.Read(context.Background(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	rows[0].Entry.NarrowingHash[0] ^= 1
	rows[0].Receipt.ChainHash[0] ^= 1
	k := l.Keys()
	k.PublicKey[0] ^= 1
	r := appendEntry(t, l, entry("one"))
	if !VerifyReceipt(l.Keys().PublicKey, *r) {
		t.Fatal("caller mutated committed receipt or public key")
	}
}
