package server

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	policylogv1 "policy-log/pkg/gen/saas/policylog/v1"
	"policy-log/witness"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// stub answers each operation with whatever one case needs. The error taxonomy
// is the thing under test, so it must be injectable directly rather than
// provoked by corrupting a store, which can only produce some of the arms.
type stub struct {
	err     error
	receipt *witness.Receipt
	cursor  *witness.Cursor
	records []witness.Record
	key     witness.Key
	seen    witness.Entry
	calls   int
}

func (s *stub) Append(_ context.Context, entry witness.Entry) (*witness.Receipt, error) {
	s.calls++
	s.seen = entry
	if s.err != nil {
		return nil, s.err
	}
	return s.receipt, nil
}

func (s *stub) Cursor(context.Context) (*witness.Cursor, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.cursor, nil
}

func (s *stub) Read(_ context.Context, _ uint64, _ uint32) ([]witness.Record, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.records, nil
}

func (s *stub) Keys() witness.Key { return s.key }

func newServer(t *testing.T, w Witness) (*Server, *[]error) {
	t.Helper()
	reported := new([]error)
	s, err := New(w, func(err error) { *reported = append(*reported, err) })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, reported
}

func digest(b byte) []byte {
	h := make([]byte, sha256.Size)
	for i := range h {
		h[i] = b
	}
	return h
}

func wireEntry() *policylogv1.Entry {
	return &policylogv1.Entry{
		OperationId:   "operation-1",
		Kind:          "binding",
		Subject:       "subject-1",
		Tenant:        "tenant-1",
		NarrowingHash: digest(0x11),
		RequestedAt:   timestamppb.New(time.Unix(1700000000, 123456789).UTC()),
	}
}

// TestAppendMapsEachTaxonomyErrorToItsOwnCode is the point of the mapping: a
// retrying caller reads the code, and ErrConflict (permanent) and ErrPending
// (clears when the original operation is retried) must never arrive as the
// same answer. Collapsing any pair here makes a retryable state look permanent
// or the reverse.
func TestAppendMapsEachTaxonomyErrorToItsOwnCode(t *testing.T) {
	cases := []struct {
		name string
		// arm is the condition this case stands for. Two cases sharing a code
		// are only acceptable when they stand for the same arm, which is how a
		// wrapped sentinel differs from a second, distinct condition.
		arm  error
		err  error
		want codes.Code
	}{
		{"invalid", witness.ErrInvalid, witness.ErrInvalid, codes.InvalidArgument},
		{"conflict", witness.ErrConflict, witness.ErrConflict, codes.AlreadyExists},
		{"pending", witness.ErrPending, witness.ErrPending, codes.FailedPrecondition},
		{"rollback", witness.ErrRollback, witness.ErrRollback, codes.Aborted},
		{"integrity", witness.ErrIntegrity, witness.ErrIntegrity, codes.DataLoss},
		{"canceled", context.Canceled, context.Canceled, codes.Canceled},
		{"deadline", context.DeadlineExceeded, context.DeadlineExceeded, codes.DeadlineExceeded},
		// Wrapped, which is how the witness actually returns most of them.
		{"wrapped invalid", witness.ErrInvalid, fmt.Errorf("%w: identifier", witness.ErrInvalid), codes.InvalidArgument},
		{"wrapped integrity", witness.ErrIntegrity, fmt.Errorf("%w: sequence 7", witness.ErrIntegrity), codes.DataLoss},
		{"wrapped rollback", witness.ErrRollback, fmt.Errorf("read committed sequence 3: %w", witness.ErrRollback), codes.Aborted},
	}
	seen := make(map[codes.Code]error)
	for _, tc := range cases {
		s, reported := newServer(t, &stub{err: tc.err})
		_, err := s.Append(context.Background(), &policylogv1.AppendRequest{Entry: wireEntry()})
		if got := status.Code(err); got != tc.want {
			t.Errorf("%s: Append returned code %v, want %v (err %v)", tc.name, got, tc.want, err)
		}
		if len(*reported) != 0 {
			t.Errorf("%s: a taxonomy error must not be reported as an unreachable store: %v", tc.name, *reported)
		}
		if previous, ok := seen[tc.want]; ok && previous != tc.arm {
			t.Errorf("%q and %q are different conditions that both map to %v; the taxonomy arms must stay distinguishable", previous, tc.arm, tc.want)
		}
		seen[tc.want] = tc.arm
	}
	// Every arm of the taxonomy must be covered, so adding a sentinel to
	// witness/contract.go without mapping it fails here instead of silently
	// arriving as Unavailable.
	for _, sentinel := range []error{witness.ErrInvalid, witness.ErrConflict, witness.ErrPending, witness.ErrRollback, witness.ErrIntegrity} {
		covered := false
		for _, tc := range cases {
			if tc.arm == sentinel {
				covered = true
			}
		}
		if !covered {
			t.Errorf("witness taxonomy error %v has no mapping case", sentinel)
		}
	}
}

// TestAppendWithholdsStoreDetailAndReportsIt covers the one error class that is
// NOT in the taxonomy: a store failure. The caller learns only that no sequence
// was assigned, and the detail — which can carry bucket identity — goes to the
// service's own reporter.
func TestAppendWithholdsStoreDetailAndReportsIt(t *testing.T) {
	underlying := errors.New("dial gs://example-witness-bucket: connection refused")
	s, reported := newServer(t, &stub{err: underlying})
	_, err := s.Append(context.Background(), &policylogv1.AppendRequest{Entry: wireEntry()})
	if got := status.Code(err); got != codes.Unavailable {
		t.Fatalf("Append returned code %v, want %v", got, codes.Unavailable)
	}
	if message := status.Convert(err).Message(); message != unreachable {
		t.Errorf("Append message = %q, want the fixed unreachable message", message)
	}
	if len(*reported) != 1 || !errors.Is((*reported)[0], underlying) {
		t.Fatalf("the underlying error must reach the reporter exactly once, got %v", *reported)
	}
}

// TestAppendRefusesAnAbsentRequestedAtInsteadOfSigning1970 is the trap the
// projection has to close. AsTime of an absent Timestamp is 1970-01-01, which
// Entry.Validate accepts, so without this check the witness would sign an entry
// hash over a timestamp the caller never sent — and no retry of that operation
// could reproduce it.
func TestAppendRefusesAnAbsentRequestedAtInsteadOfSigning1970(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry *policylogv1.Entry
	}{
		{"absent requested_at", &policylogv1.Entry{
			OperationId: "operation-1", Kind: "binding", Subject: "subject-1",
			NarrowingHash: digest(0x11),
		}},
		{"out of range requested_at", &policylogv1.Entry{
			OperationId: "operation-1", Kind: "binding", Subject: "subject-1",
			NarrowingHash: digest(0x11),
			RequestedAt:   &timestamppb.Timestamp{Seconds: 1, Nanos: -5},
		}},
		{"absent entry", nil},
	} {
		backend := &stub{receipt: &witness.Receipt{Seq: 1}}
		s, _ := newServer(t, backend)
		_, err := s.Append(context.Background(), &policylogv1.AppendRequest{Entry: tc.entry})
		if got := status.Code(err); got != codes.InvalidArgument {
			t.Errorf("%s: Append returned code %v, want %v", tc.name, got, codes.InvalidArgument)
		}
		if backend.calls != 0 {
			t.Errorf("%s: the witness must not be reached at all, got %d calls", tc.name, backend.calls)
		}
	}
}

// TestAppendCarriesTheEntryLosslessly is the contract the proto exists to keep:
// the entry hash computed from what the server handed the witness must equal the
// hash of the entry the caller described on the wire, including the exact
// nanosecond of requested_at and the raw 32 digest bytes. If it did not, a retry
// of the same operation would produce a different hash and be refused.
func TestAppendCarriesTheEntryLosslessly(t *testing.T) {
	wire := wireEntry()
	backend := &stub{receipt: &witness.Receipt{Seq: 1}}
	s, _ := newServer(t, backend)
	if _, err := s.Append(context.Background(), &policylogv1.AppendRequest{Entry: wire}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	want := witness.Entry{
		OperationID: wire.GetOperationId(), Kind: wire.GetKind(), Subject: wire.GetSubject(),
		Tenant: wire.GetTenant(), NarrowingHash: wire.GetNarrowingHash(),
		RequestedAt: time.Unix(1700000000, 123456789).UTC(),
	}
	wantHash, err := witness.EntryHash(want)
	if err != nil {
		t.Fatalf("EntryHash: %v", err)
	}
	gotHash, err := witness.EntryHash(backend.seen)
	if err != nil {
		t.Fatalf("EntryHash of the carried entry: %v", err)
	}
	if string(gotHash) != string(wantHash) {
		t.Fatalf("the carried entry hashes differently from the wire entry:\n got %x\nwant %x\ncarried %+v", gotHash, wantHash, backend.seen)
	}
	// The round trip back out must reproduce the same bytes, so a reader can
	// recompute the hash from a Read response.
	round, err := entryFrom(entryTo(backend.seen))
	if err != nil {
		t.Fatalf("entryFrom(entryTo(...)): %v", err)
	}
	roundHash, err := witness.EntryHash(round)
	if err != nil {
		t.Fatalf("EntryHash of the round trip: %v", err)
	}
	if string(roundHash) != string(wantHash) {
		t.Fatalf("round trip hashes differently:\n got %x\nwant %x", roundHash, wantHash)
	}
}

// TestAppendProjectsTheWholeReceipt proves no field of the receipt is dropped:
// a caller that cannot see prev_hash or the signature cannot verify the chain,
// and a receipt it cannot verify is not evidence.
func TestAppendProjectsTheWholeReceipt(t *testing.T) {
	issued := time.Unix(1700000001, 42).UTC()
	receipt := &witness.Receipt{
		Seq: 9, EntryHash: digest(0x01), PrevHash: digest(0x02), ChainHash: digest(0x03),
		IssuedAt: issued, Signature: make([]byte, ed25519.SignatureSize),
	}
	s, _ := newServer(t, &stub{receipt: receipt})
	response, err := s.Append(context.Background(), &policylogv1.AppendRequest{Entry: wireEntry()})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	got := response.GetReceipt()
	if got.GetSeq() != 9 ||
		string(got.GetEntryHash()) != string(digest(0x01)) ||
		string(got.GetPrevHash()) != string(digest(0x02)) ||
		string(got.GetChainHash()) != string(digest(0x03)) ||
		len(got.GetSignature()) != ed25519.SignatureSize ||
		!got.GetIssuedAt().AsTime().Equal(issued) {
		t.Fatalf("receipt projection lost a field: %+v", got)
	}
}

// TestCursorAndKeysProjectTheSignedObservation keeps the two discovery
// operations honest: an unsigned cursor or a key document without its id is not
// usable by a verifying client.
func TestCursorAndKeysProjectTheSignedObservation(t *testing.T) {
	issued := time.Unix(1700000002, 7).UTC()
	backend := &stub{
		cursor: &witness.Cursor{HeadSeq: 4, ChainHash: digest(0x05), IssuedAt: issued, Signature: make([]byte, ed25519.SignatureSize)},
		key:    witness.Key{Algorithm: "Ed25519", KeyID: "abc123", PublicKey: make([]byte, ed25519.PublicKeySize)},
	}
	s, _ := newServer(t, backend)
	cursor, err := s.Cursor(context.Background(), &policylogv1.CursorRequest{})
	if err != nil {
		t.Fatalf("Cursor: %v", err)
	}
	if cursor.GetCursor().GetHeadSeq() != 4 ||
		string(cursor.GetCursor().GetChainHash()) != string(digest(0x05)) ||
		len(cursor.GetCursor().GetSignature()) != ed25519.SignatureSize ||
		!cursor.GetCursor().GetIssuedAt().AsTime().Equal(issued) {
		t.Fatalf("cursor projection lost a field: %+v", cursor.GetCursor())
	}
	keys, err := s.Keys(context.Background(), &policylogv1.KeysRequest{})
	if err != nil {
		t.Fatalf("Keys: %v", err)
	}
	if keys.GetKey().GetAlgorithm() != "Ed25519" || keys.GetKey().GetKeyId() != "abc123" ||
		len(keys.GetKey().GetPublicKey()) != ed25519.PublicKeySize {
		t.Fatalf("key projection lost a field: %+v", keys.GetKey())
	}
}

// TestReadReturnsAnEmptyPageRatherThanNothing matters because reading at
// head_seq + 1 is a legitimate empty page, and a reconciler must be able to
// tell it from a refusal. It must also carry each record's ORIGINAL receipt.
func TestReadReturnsAnEmptyPageRatherThanNothing(t *testing.T) {
	s, _ := newServer(t, &stub{records: nil})
	empty, err := s.Read(context.Background(), &policylogv1.ReadRequest{FromSeq: 1, Limit: 10})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if empty.GetRecords() == nil || len(empty.GetRecords()) != 0 {
		t.Fatalf("an empty page must be an empty list, got %v", empty.GetRecords())
	}

	requested := time.Unix(1700000003, 321).UTC()
	backend := &stub{records: []witness.Record{{
		Entry:   witness.Entry{OperationID: "op-2", Kind: "scope_grant", Subject: "s", Tenant: "", NarrowingHash: digest(0x07), RequestedAt: requested},
		Receipt: witness.Receipt{Seq: 2, EntryHash: digest(0x08), PrevHash: digest(0x09), ChainHash: digest(0x0a), IssuedAt: requested, Signature: make([]byte, ed25519.SignatureSize)},
	}}}
	s, _ = newServer(t, backend)
	page, err := s.Read(context.Background(), &policylogv1.ReadRequest{FromSeq: 2, Limit: 1})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(page.GetRecords()) != 1 {
		t.Fatalf("Read returned %d records, want 1", len(page.GetRecords()))
	}
	record := page.GetRecords()[0]
	if record.GetEntry().GetOperationId() != "op-2" || record.GetReceipt().GetSeq() != 2 ||
		string(record.GetEntry().GetNarrowingHash()) != string(digest(0x07)) ||
		!record.GetEntry().GetRequestedAt().AsTime().Equal(requested) ||
		len(record.GetReceipt().GetSignature()) != ed25519.SignatureSize {
		t.Fatalf("record projection lost a field: %+v", record)
	}
}

// TestReadPassesTheBoundsThroughUnchanged: the limit ceiling and the inclusive
// floor are the witness's to enforce, and a transport that clamped or defaulted
// them would turn a refusal into a silently different page.
func TestReadPassesTheBoundsThroughUnchanged(t *testing.T) {
	var gotFrom uint64
	var gotLimit uint32
	s, _ := newServer(t, &boundsStub{from: &gotFrom, limit: &gotLimit})
	if _, err := s.Read(context.Background(), &policylogv1.ReadRequest{FromSeq: 0, Limit: witness.MaxReadLimit + 1}); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if gotFrom != 0 || gotLimit != witness.MaxReadLimit+1 {
		t.Fatalf("Read passed from=%d limit=%d, want them unchanged (0, %d)", gotFrom, gotLimit, witness.MaxReadLimit+1)
	}
}

type boundsStub struct {
	stub
	from  *uint64
	limit *uint32
}

func (b *boundsStub) Read(_ context.Context, from uint64, limit uint32) ([]witness.Record, error) {
	*b.from, *b.limit = from, limit
	return nil, nil
}

// TestNewRefusesAnIncompleteConstruction keeps the server from being built
// without a reporter, which is what keeps a withheld store error observable.
func TestNewRefusesAnIncompleteConstruction(t *testing.T) {
	if _, err := New(nil, func(error) {}); err == nil {
		t.Error("New accepted a nil witness")
	}
	if _, err := New(&stub{}, nil); err == nil {
		t.Error("New accepted a nil failure reporter")
	}
}
