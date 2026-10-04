package server_test

// These exercise the projection over a REAL gRPC connection, a real
// service.Service, a real witness and the generated client — not a stub. The
// stubbed tests in the package next door prove the mapping of each taxonomy
// arm; these prove the arms actually arise from the shipped code and survive
// the wire, which is the part a hand-written adapter gets wrong.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"net"
	"testing"
	"time"

	"policy-log/mirror"
	"policy-log/objectstore"
	"policy-log/objectstore/memory"
	policylogv1 "policy-log/pkg/gen/saas/policylog/v1"
	"policy-log/server"
	"policy-log/service"
	"policy-log/witness"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type discard struct{}

func (discard) Write(context.Context, witness.Record) error { return nil }

func genesisStore(t *testing.T) *memory.Store {
	t.Helper()
	store := memory.New()
	genesis, err := json.Marshal(struct {
		Seq       uint64 `json:"seq"`
		ChainHash []byte `json:"chain_hash"`
	}{ChainHash: make([]byte, sha256.Size)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(context.Background(), witness.HeadObject, genesis, 0); err != nil {
		t.Fatal(err)
	}
	return store
}

// serve stands up the whole shipped stack — witness, service, projection — on a
// loopback listener and returns the generated client for it. No fixed port is
// written here any more than one is written in the binary.
func serve(t *testing.T, objects objectstore.Store, key ed25519.PrivateKey) policylogv1.WitnessServiceClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	log, err := witness.Open(ctx, objects, key)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := mirror.Start(ctx, discard{}, func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	witnessService, err := service.New(log, worker)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := server.New(witnessService, func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := grpc.NewServer()
	policylogv1.RegisterWitnessServiceServer(grpcServer, handler)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)

	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return policylogv1.NewWitnessServiceClient(connection)
}

func dial(t *testing.T) (policylogv1.WitnessServiceClient, ed25519.PublicKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return serve(t, genesisStore(t), private), public
}

// headFailure persists an entry object and then loses the head write, which is
// the interrupted append the ErrPending arm exists for.
type headFailure struct{ objectstore.Store }

func (s headFailure) Write(ctx context.Context, name string, body []byte, generation int64) (int64, error) {
	if name == witness.HeadObject {
		return 0, objectstore.ErrPrecondition
	}
	return s.Store.Write(ctx, name, body, generation)
}

func digest(b byte) []byte {
	h := make([]byte, sha256.Size)
	for i := range h {
		h[i] = b
	}
	return h
}

func entry(operation string, hash byte, requested time.Time) *policylogv1.Entry {
	return &policylogv1.Entry{
		OperationId:   operation,
		Kind:          "binding",
		Subject:       "subject-1",
		Tenant:        "tenant-1",
		NarrowingHash: digest(hash),
		RequestedAt:   timestamppb.New(requested),
	}
}

func receiptFrom(wire *policylogv1.Receipt) witness.Receipt {
	return witness.Receipt{
		Seq: wire.GetSeq(), EntryHash: wire.GetEntryHash(), PrevHash: wire.GetPrevHash(),
		ChainHash: wire.GetChainHash(), IssuedAt: wire.GetIssuedAt().AsTime(), Signature: wire.GetSignature(),
	}
}

// TestAppendedReceiptVerifiesUnderThePublishedKey is the whole point of the
// transport: what comes back over the wire must verify under the key Keys
// publishes, and its entry hash must be the hash of the bytes the caller asked
// to append. A projection that lost a byte of any digest would pass a shape
// check and fail this.
func TestAppendedReceiptVerifiesUnderThePublishedKey(t *testing.T) {
	client, public := dial(t)
	ctx := context.Background()
	requested := time.Unix(1700000000, 123456789).UTC()

	appended, err := client.Append(ctx, &policylogv1.AppendRequest{Entry: entry("operation-1", 0x11, requested)})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	receipt := receiptFrom(appended.GetReceipt())
	if receipt.Seq != 1 {
		t.Fatalf("first sequence = %d, want 1", receipt.Seq)
	}
	if !witness.VerifyReceipt(public, receipt) {
		t.Fatal("the receipt that crossed the wire does not verify under the published key")
	}
	wantHash, err := witness.EntryHash(witness.Entry{
		OperationID: "operation-1", Kind: "binding", Subject: "subject-1", Tenant: "tenant-1",
		NarrowingHash: digest(0x11), RequestedAt: requested,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(receipt.EntryHash) != string(wantHash) {
		t.Fatalf("receipt entry hash %x does not match the appended bytes %x", receipt.EntryHash, wantHash)
	}

	keys, err := client.Keys(ctx, &policylogv1.KeysRequest{})
	if err != nil {
		t.Fatalf("Keys: %v", err)
	}
	if string(keys.GetKey().GetPublicKey()) != string(public) {
		t.Fatal("Keys published a different public key than the one the log signs with")
	}
}

// TestRetryOfTheIdenticalEntryReturnsTheOriginalReceipt proves the idempotency
// the whole protocol rests on survives the projection — including requested_at,
// which is covered by the entry hash, so a transport that rounded it would turn
// every retry into a conflict.
func TestRetryOfTheIdenticalEntryReturnsTheOriginalReceipt(t *testing.T) {
	client, _ := dial(t)
	ctx := context.Background()
	requested := time.Unix(1700000000, 987654321).UTC()

	first, err := client.Append(ctx, &policylogv1.AppendRequest{Entry: entry("operation-1", 0x11, requested)})
	if err != nil {
		t.Fatalf("first Append: %v", err)
	}
	second, err := client.Append(ctx, &policylogv1.AppendRequest{Entry: entry("operation-1", 0x11, requested)})
	if err != nil {
		t.Fatalf("retried Append: %v", err)
	}
	if first.GetReceipt().GetSeq() != second.GetReceipt().GetSeq() ||
		string(first.GetReceipt().GetSignature()) != string(second.GetReceipt().GetSignature()) {
		t.Fatalf("a retry minted a new receipt: %v then %v", first.GetReceipt(), second.GetReceipt())
	}
}

// TestReusedOperationIDWithDifferentBytesIsAlreadyExists: the conflict arm,
// raised by the shipped witness rather than injected.
func TestReusedOperationIDWithDifferentBytesIsAlreadyExists(t *testing.T) {
	client, _ := dial(t)
	ctx := context.Background()
	requested := time.Unix(1700000000, 0).UTC()
	if _, err := client.Append(ctx, &policylogv1.AppendRequest{Entry: entry("operation-1", 0x11, requested)}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	_, err := client.Append(ctx, &policylogv1.AppendRequest{Entry: entry("operation-1", 0x22, requested)})
	if got := status.Code(err); got != codes.AlreadyExists {
		t.Fatalf("reusing an operation id with different bytes returned %v, want %v (err %v)", got, codes.AlreadyExists, err)
	}
}

// TestAnInvalidEntryIsRefusedWithoutASequence: the invalid arm, and the proof
// that a refusal consumes nothing — the next valid append is still sequence 1.
func TestAnInvalidEntryIsRefusedWithoutASequence(t *testing.T) {
	client, _ := dial(t)
	ctx := context.Background()
	short := entry("operation-1", 0x11, time.Unix(1700000000, 0).UTC())
	short.NarrowingHash = []byte{0x11}
	_, err := client.Append(ctx, &policylogv1.AppendRequest{Entry: short})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("a 1-byte narrowing hash returned %v, want %v (err %v)", got, codes.InvalidArgument, err)
	}
	good, err := client.Append(ctx, &policylogv1.AppendRequest{Entry: entry("operation-2", 0x11, time.Unix(1700000000, 0).UTC())})
	if err != nil {
		t.Fatalf("Append after a refusal: %v", err)
	}
	if good.GetReceipt().GetSeq() != 1 {
		t.Fatalf("a refused append consumed sequence 1; next append got %d", good.GetReceipt().GetSeq())
	}
}

// TestReadAtTheHeadIsEmptyAndBeyondItIsAborted is the distinction the README
// calls out: an ahead-of-log reconciler must not be able to mistake a rollback
// for inactivity, so head+1 is an empty page and head+2 is a refusal.
func TestReadAtTheHeadIsEmptyAndBeyondItIsAborted(t *testing.T) {
	client, public := dial(t)
	ctx := context.Background()
	requested := time.Unix(1700000000, 5).UTC()
	if _, err := client.Append(ctx, &policylogv1.AppendRequest{Entry: entry("operation-1", 0x11, requested)}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	page, err := client.Read(ctx, &policylogv1.ReadRequest{FromSeq: 1, Limit: witness.MaxReadLimit})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(page.GetRecords()) != 1 {
		t.Fatalf("Read(1) returned %d records, want 1", len(page.GetRecords()))
	}
	record := page.GetRecords()[0]
	if !witness.VerifyReceipt(public, receiptFrom(record.GetReceipt())) {
		t.Fatal("a read record's original receipt does not verify")
	}
	if !record.GetEntry().GetRequestedAt().AsTime().Equal(requested) {
		t.Fatalf("read requested_at = %v, want %v", record.GetEntry().GetRequestedAt().AsTime(), requested)
	}

	atHead, err := client.Read(ctx, &policylogv1.ReadRequest{FromSeq: 2, Limit: 1})
	if err != nil {
		t.Fatalf("Read at head+1 must be an empty page, got %v", err)
	}
	if len(atHead.GetRecords()) != 0 {
		t.Fatalf("Read at head+1 returned %d records, want 0", len(atHead.GetRecords()))
	}

	_, err = client.Read(ctx, &policylogv1.ReadRequest{FromSeq: 3, Limit: 1})
	if got := status.Code(err); got != codes.Aborted {
		t.Fatalf("Read beyond head+1 returned %v, want %v (err %v)", got, codes.Aborted, err)
	}
}

// TestReadBoundsAreRefusedNotClamped: from_seq 0 and an over-ceiling limit are
// invalid requests, not requests to be quietly adjusted.
func TestReadBoundsAreRefusedNotClamped(t *testing.T) {
	client, _ := dial(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		request *policylogv1.ReadRequest
	}{
		{"from_seq 0", &policylogv1.ReadRequest{FromSeq: 0, Limit: 1}},
		{"limit 0", &policylogv1.ReadRequest{FromSeq: 1, Limit: 0}},
		{"limit over the ceiling", &policylogv1.ReadRequest{FromSeq: 1, Limit: witness.MaxReadLimit + 1}},
	} {
		_, err := client.Read(ctx, tc.request)
		if got := status.Code(err); got != codes.InvalidArgument {
			t.Errorf("%s returned %v, want %v (err %v)", tc.name, got, codes.InvalidArgument, err)
		}
	}
}

// TestCursorVerifiesAndTracksTheHead keeps the signed observation usable: an
// unsigned or stale-shaped cursor is not something a client can pin against.
func TestCursorVerifiesAndTracksTheHead(t *testing.T) {
	client, public := dial(t)
	ctx := context.Background()

	empty, err := client.Cursor(ctx, &policylogv1.CursorRequest{})
	if err != nil {
		t.Fatalf("Cursor: %v", err)
	}
	genesis := witness.Cursor{
		HeadSeq: empty.GetCursor().GetHeadSeq(), ChainHash: empty.GetCursor().GetChainHash(),
		IssuedAt: empty.GetCursor().GetIssuedAt().AsTime(), Signature: empty.GetCursor().GetSignature(),
	}
	if genesis.HeadSeq != 0 || !witness.VerifyCursor(public, genesis) {
		t.Fatalf("the genesis cursor does not verify: %+v", genesis)
	}

	if _, err := client.Append(ctx, &policylogv1.AppendRequest{Entry: entry("operation-1", 0x11, time.Unix(1700000000, 0).UTC())}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	advanced, err := client.Cursor(ctx, &policylogv1.CursorRequest{})
	if err != nil {
		t.Fatalf("Cursor: %v", err)
	}
	moved := witness.Cursor{
		HeadSeq: advanced.GetCursor().GetHeadSeq(), ChainHash: advanced.GetCursor().GetChainHash(),
		IssuedAt: advanced.GetCursor().GetIssuedAt().AsTime(), Signature: advanced.GetCursor().GetSignature(),
	}
	if moved.HeadSeq != 1 || !witness.VerifyCursor(public, moved) {
		t.Fatalf("the cursor after one append does not verify at sequence 1: %+v", moved)
	}
}

// TestAnInterruptedAppendIsFailedPreconditionUntilItsOwnRetry is the arm that
// must never read like ErrConflict. An append that persisted its immutable
// entry and then lost the head write reserves that sequence: a DIFFERENT
// operation is refused with FailedPrecondition, which clears — and only the
// identical operation can finish it. A caller told AlreadyExists here would
// give up on a state that was about to resolve; a caller told nothing would
// mint a second sequence for one narrowing.
func TestAnInterruptedAppendIsFailedPreconditionUntilItsOwnRetry(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	store := genesisStore(t)
	ctx := context.Background()
	requested := time.Unix(1700000000, 11).UTC()

	// The same log, reached once through a store that loses the head write and
	// once through the healthy one, which is how an interrupted append is
	// observed after a restart.
	interrupted := serve(t, headFailure{store}, private)
	healthy := serve(t, store, private)

	_, err = interrupted.Append(ctx, &policylogv1.AppendRequest{Entry: entry("operation-1", 0x11, requested)})
	if got := status.Code(err); got != codes.Unavailable {
		t.Fatalf("an append whose head write is lost returned %v, want %v (err %v)", got, codes.Unavailable, err)
	}

	_, err = healthy.Append(ctx, &policylogv1.AppendRequest{Entry: entry("operation-2", 0x22, requested)})
	if got := status.Code(err); got != codes.FailedPrecondition {
		t.Fatalf("a different operation over the reserved sequence returned %v, want %v (err %v)", got, codes.FailedPrecondition, err)
	}

	finished, err := healthy.Append(ctx, &policylogv1.AppendRequest{Entry: entry("operation-1", 0x11, requested)})
	if err != nil {
		t.Fatalf("the identical operation must be able to finish its own reserved sequence: %v", err)
	}
	if finished.GetReceipt().GetSeq() != 1 {
		t.Fatalf("the finished retry got sequence %d, want the reserved 1", finished.GetReceipt().GetSeq())
	}

	// And the same operation id with different bytes is still the permanent
	// answer, so the two arms stay distinguishable end to end.
	_, err = healthy.Append(ctx, &policylogv1.AppendRequest{Entry: entry("operation-1", 0x33, requested)})
	if got := status.Code(err); got != codes.AlreadyExists {
		t.Fatalf("reusing the committed operation id with different bytes returned %v, want %v (err %v)", got, codes.AlreadyExists, err)
	}
}
