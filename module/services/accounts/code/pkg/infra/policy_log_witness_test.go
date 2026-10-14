package infra

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"accounts/pkg/business"
	policylogv1 "accounts/pkg/gen/saas/policylog/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The witness client against a FAKE witness: one that signs, sequences and
// chains like the real one, and can be made to lie in each way the client must
// refuse. What a fake cannot show is that these encodings agree with the real
// witness's — that is policy_log_witness_integration_test.go, which verifies
// receipts the real witness signed.

// fakeWitness is policylogv1.WitnessServiceClient over an in-memory chain.
type fakeWitness struct {
	mu      sync.Mutex
	private ed25519.PrivateKey
	public  ed25519.PublicKey
	records []*policylogv1.Record
	byOp    map[string]*policylogv1.Receipt
	now     time.Time

	// appendRequests records every entry offered, retries included.
	appendRequests []*policylogv1.Entry
	cursorCalls    int

	// Tampering, each applied to the next answer of its kind.
	appendErrs      []error
	signWith        ed25519.PrivateKey // receipts and cursors signed by another key
	flipSignature   bool
	forceSeq        uint64 // receipt claims this sequence
	forcePrev       []byte // receipt claims this previous hash
	forceEntryHash  []byte // receipt witnesses these bytes instead
	forceChain      []byte // receipt signs a chain hash that is not its own link
	cursorIssuedAt  time.Time
	cursorHeadDelta int64 // cursor claims head + delta
	publishKey      ed25519.PublicKey
	dropRecords     bool
	breakChainAt    uint64 // the record at this sequence claims a wrong prev hash
	resignAt        uint64 // the record at this sequence is re-signed by signWithAtRead
	signWithAtRead  ed25519.PrivateKey
	alterEntryAt    uint64 // the record at this sequence names another subject
}

func newFakeWitness(t *testing.T) *fakeWitness {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return &fakeWitness{
		private: private, public: public, byOp: map[string]*policylogv1.Receipt{},
		now: time.Date(2026, 10, 10, 12, 0, 0, 123456789, time.UTC),
	}
}

func (f *fakeWitness) pin(t *testing.T) PolicyLogWitnessKey {
	t.Helper()
	digest := sha256.Sum256(f.public)
	key, err := policyLogWitnessKeyFrom("Ed25519", hex.EncodeToString(digest[:]), f.public)
	require.NoError(t, err)
	return key
}

func (f *fakeWitness) signer() ed25519.PrivateKey {
	if f.signWith != nil {
		return f.signWith
	}
	return f.private
}

func (f *fakeWitness) head() (uint64, []byte) {
	if len(f.records) == 0 {
		return 0, make([]byte, sha256.Size)
	}
	last := f.records[len(f.records)-1].GetReceipt()
	return last.GetSeq(), last.GetChainHash()
}

func (f *fakeWitness) Append(_ context.Context, in *policylogv1.AppendRequest, _ ...grpc.CallOption) (*policylogv1.AppendResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.appendRequests = append(f.appendRequests, proto.Clone(in.GetEntry()).(*policylogv1.Entry))
	if len(f.appendErrs) > 0 {
		err := f.appendErrs[0]
		f.appendErrs = f.appendErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	if existing, ok := f.byOp[in.GetEntry().GetOperationId()]; ok {
		return &policylogv1.AppendResponse{Receipt: existing}, nil
	}
	entryHash, err := witnessEntryHash(in.GetEntry())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	seq, prev := f.head()
	seq++
	if f.forceEntryHash != nil {
		entryHash = f.forceEntryHash
	}
	if f.forceSeq != 0 {
		seq = f.forceSeq
	}
	if f.forcePrev != nil {
		prev = f.forcePrev
	}
	receipt := f.sign(seq, prev, entryHash)
	f.records = append(f.records, &policylogv1.Record{Entry: in.GetEntry(), Receipt: receipt})
	f.byOp[in.GetEntry().GetOperationId()] = receipt
	return &policylogv1.AppendResponse{Receipt: receipt}, nil
}

func (f *fakeWitness) sign(seq uint64, prev, entryHash []byte) *policylogv1.Receipt {
	r := &witnessReceipt{
		seq: seq, entryHash: entryHash, prevHash: prev,
		chainHash: witnessChainHash(seq, prev, entryHash), issuedAt: f.now,
	}
	if f.forceChain != nil {
		r.chainHash = f.forceChain
	}
	r.signature = ed25519.Sign(f.signer(), witnessReceiptBytes(r))
	if f.flipSignature {
		r.signature[0] ^= 0x01
	}
	return &policylogv1.Receipt{
		Seq: r.seq, EntryHash: r.entryHash, PrevHash: r.prevHash, ChainHash: r.chainHash,
		IssuedAt: timestamppb.New(r.issuedAt), Signature: r.signature,
	}
}

func (f *fakeWitness) Cursor(context.Context, *policylogv1.CursorRequest, ...grpc.CallOption) (*policylogv1.CursorResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cursorCalls++
	seq, chain := f.head()
	seq = uint64(int64(seq) + f.cursorHeadDelta)
	issued := f.now
	if !f.cursorIssuedAt.IsZero() {
		issued = f.cursorIssuedAt
	}
	c := &witnessCursor{headSeq: seq, chainHash: chain, issuedAt: issued}
	c.signature = ed25519.Sign(f.signer(), witnessCursorBytes(c))
	if f.flipSignature {
		c.signature[0] ^= 0x01
	}
	return &policylogv1.CursorResponse{Cursor: &policylogv1.Cursor{
		HeadSeq: c.headSeq, ChainHash: c.chainHash, IssuedAt: timestamppb.New(c.issuedAt), Signature: c.signature,
	}}, nil
}

func (f *fakeWitness) Read(_ context.Context, in *policylogv1.ReadRequest, _ ...grpc.CallOption) (*policylogv1.ReadResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &policylogv1.ReadResponse{}
	if f.dropRecords {
		return out, nil
	}
	for _, record := range f.records {
		if record.GetReceipt().GetSeq() >= in.GetFromSeq() && uint32(len(out.Records)) < in.GetLimit() {
			copied := proto.Clone(record).(*policylogv1.Record)
			if copied.GetReceipt().GetSeq() == f.breakChainAt {
				copied.Receipt = f.sign(copied.GetReceipt().GetSeq(), bytes.Repeat([]byte{7}, sha256.Size), copied.GetReceipt().GetEntryHash())
			}
			if copied.GetReceipt().GetSeq() == f.resignAt {
				r := copied.GetReceipt()
				signed := &witnessReceipt{seq: r.GetSeq(), entryHash: r.GetEntryHash(), prevHash: r.GetPrevHash(),
					chainHash: r.GetChainHash(), issuedAt: r.GetIssuedAt().AsTime()}
				r.Signature = ed25519.Sign(f.signWithAtRead, witnessReceiptBytes(signed))
			}
			if copied.GetReceipt().GetSeq() == f.alterEntryAt {
				copied.Entry.Subject = "another-subject"
			}
			out.Records = append(out.Records, copied)
		}
	}
	return out, nil
}

func (f *fakeWitness) Keys(context.Context, *policylogv1.KeysRequest, ...grpc.CallOption) (*policylogv1.KeysResponse, error) {
	public := f.public
	if f.publishKey != nil {
		public = f.publishKey
	}
	digest := sha256.Sum256(public)
	return &policylogv1.KeysResponse{Key: &policylogv1.Key{
		Algorithm: "Ed25519", KeyId: hex.EncodeToString(digest[:]), PublicKey: public,
	}}, nil
}

func newTestWitnessLog(t *testing.T, fake *fakeWitness) *WitnessPolicyLog {
	t.Helper()
	log, err := NewWitnessPolicyLog(fake, fake.pin(t))
	require.NoError(t, err)
	log.now = func() time.Time { return fake.now }
	return log
}

func narrowing(operation string) *business.PolicyLogEntry {
	return &business.PolicyLogEntry{
		OperationID: operation,
		Decision:    business.PolicyLogNarrowed,
		SubjectKind: business.PolicyLogInstallation,
		SubjectID:   "installation-1",
		Policy:      map[string]any{"org_id": "org-1", "status": "revoked"},
		Actor:       "actor-1",
	}
}

func TestWitnessAppendReturnsTheVerifiedReceipt(t *testing.T) {
	fake := newFakeWitness(t)
	log := newTestWitnessLog(t, fake)

	receipt, err := log.Append(context.Background(), narrowing("uninstall-solution/1"))
	require.NoError(t, err)
	require.Equal(t, uint64(1), receipt.Sequence)
	require.True(t, receipt.AppendedAt.Equal(fake.now), "the receipt carries the WITNESS's clock")

	// The stored receipt is evidence anyone holding the pin can re-verify.
	var stored witnessReceiptDocument
	require.NoError(t, json.Unmarshal([]byte(receipt.Receipt), &stored))
	require.Equal(t, fake.pin(t).KeyID, stored.KeyID)
	require.Equal(t, uint64(1), stored.Seq)
	require.LessOrEqual(t, len(receipt.Receipt), 1024, "policy_log_commits.receipt holds at most 1024 characters")
	signature, err := base64.StdEncoding.DecodeString(stored.Signature)
	require.NoError(t, err)
	require.Equal(t, fake.records[0].GetReceipt().GetSignature(), signature)

	// The narrowing hash covers the whole entry, policy and actor included.
	other := narrowing("uninstall-solution/1")
	other.Actor = "actor-2"
	first, err := narrowingHash(narrowing("uninstall-solution/1"))
	require.NoError(t, err)
	second, err := narrowingHash(other)
	require.NoError(t, err)
	require.NotEqual(t, first, second)
	require.Equal(t, first, fake.records[0].GetEntry().GetNarrowingHash())
	require.Equal(t, "installation.narrowed", fake.records[0].GetEntry().GetKind())
}

// Each way a receipt can fail to be evidence, and each is refused.
func TestWitnessAppendRefusesEvidenceThatDoesNotVerify(t *testing.T) {
	_, stranger, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	for name, tamper := range map[string]func(*fakeWitness){
		"signed by another key":   func(f *fakeWitness) { f.signWith = stranger },
		"signature altered":       func(f *fakeWitness) { f.flipSignature = true },
		"witnesses other bytes":   func(f *fakeWitness) { f.forceEntryHash = bytes.Repeat([]byte{9}, sha256.Size) },
		"breaks the genesis link": func(f *fakeWitness) { f.forcePrev = bytes.Repeat([]byte{3}, sha256.Size) },
		// Signed, and over a chain hash that is not the hash of its own
		// sequence, previous hash and entry: not a link in any chain.
		"signs a chain hash it did not compute": func(f *fakeWitness) { f.forceChain = bytes.Repeat([]byte{4}, sha256.Size) },
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeWitness(t)
			log := newTestWitnessLog(t, fake)
			tamper(fake)
			receipt, err := log.Append(context.Background(), narrowing("op/"+name))
			require.ErrorIs(t, err, ErrPolicyLogWitnessEvidence)
			require.Nil(t, receipt)
		})
	}
}

// A receipt at or below a sequence this client already verified is a witness
// that has rolled back, however well it is signed.
func TestWitnessAppendRefusesASequenceRegression(t *testing.T) {
	fake := newFakeWitness(t)
	log := newTestWitnessLog(t, fake)
	for i := range 3 {
		_, err := log.Append(context.Background(), narrowing("op/"+string(rune('a'+i))))
		require.NoError(t, err)
	}
	fake.forceSeq = 2
	_, err := log.Append(context.Background(), narrowing("op/regressed"))
	require.ErrorIs(t, err, ErrPolicyLogWitnessEvidence)
	require.Contains(t, err.Error(), "rolled back")
}

// The next sequence must extend the chain this client verified.
func TestWitnessAppendRefusesAFork(t *testing.T) {
	fake := newFakeWitness(t)
	log := newTestWitnessLog(t, fake)
	_, err := log.Append(context.Background(), narrowing("op/1"))
	require.NoError(t, err)
	fake.forcePrev = bytes.Repeat([]byte{5}, sha256.Size)
	_, err = log.Append(context.Background(), narrowing("op/2"))
	require.ErrorIs(t, err, ErrPolicyLogWitnessEvidence)
	require.Contains(t, err.Error(), "forked")
}

// Unavailable means no sequence was assigned, so the identical entry is offered
// again — requested_at included, or the witness would read a different entry.
func TestWitnessAppendRetriesUnavailableWithTheIdenticalEntry(t *testing.T) {
	fake := newFakeWitness(t)
	log := newTestWitnessLog(t, fake)
	calls := 0
	log.now = func() time.Time { calls++; return fake.now.Add(time.Duration(calls) * time.Second) }
	fake.appendErrs = []error{status.Error(codes.Unavailable, "no sequence assigned")}

	receipt, err := log.Append(context.Background(), narrowing("op/retry"))
	require.NoError(t, err)
	require.Equal(t, uint64(1), receipt.Sequence)
	require.Len(t, fake.appendRequests, 2)
	require.True(t, proto.Equal(fake.appendRequests[0], fake.appendRequests[1]),
		"a retry must offer the identical entry")
}

// Every other answer is final: a conflict or a pending slot is not cleared by
// offering the same bytes again.
func TestWitnessAppendDoesNotRetryARefusal(t *testing.T) {
	for _, code := range []codes.Code{codes.AlreadyExists, codes.FailedPrecondition, codes.Aborted, codes.DataLoss, codes.InvalidArgument} {
		fake := newFakeWitness(t)
		log := newTestWitnessLog(t, fake)
		fake.appendErrs = []error{status.Error(code, "refused")}
		_, err := log.Append(context.Background(), narrowing("op/refused"))
		require.Error(t, err)
		require.Equal(t, code, status.Code(errors.Unwrap(err)), "the witness's code reaches the caller")
		require.Len(t, fake.appendRequests, 1, "%s is final and must not be retried", code)
	}
}

func TestWitnessEntriesReturnsVerifiedRecords(t *testing.T) {
	fake := newFakeWitness(t)
	writer := newTestWitnessLog(t, fake)
	for _, op := range []string{"op/1", "op/2", "op/3"} {
		_, err := writer.Append(context.Background(), narrowing(op))
		require.NoError(t, err)
	}

	// A different replica: it has verified nothing yet.
	reader := newTestWitnessLog(t, fake)
	records, err := reader.Entries(context.Background(), 0, 500)
	require.NoError(t, err)
	require.Len(t, records, 3)
	for i, record := range records {
		require.Equal(t, uint64(i+1), record.Sequence)
		require.Equal(t, business.PolicyLogInstallation, record.SubjectKind)
		require.Equal(t, business.PolicyLogNarrowed, record.Decision)
		require.Equal(t, "installation-1", record.SubjectID)
		require.NotEmpty(t, record.Receipt)
	}

	// Nothing new past the head is an empty page, not an error.
	records, err = reader.Entries(context.Background(), 3, 500)
	require.NoError(t, err)
	require.Empty(t, records)

	// Partial pages chain across the boundary.
	other := newTestWitnessLog(t, fake)
	records, err = other.Entries(context.Background(), 1, 1)
	require.NoError(t, err)
	require.Len(t, records, 1)
	records, err = other.Entries(context.Background(), 2, 500)
	require.NoError(t, err)
	require.Len(t, records, 1)
}

// Each way a read can fail to show the witness's current, unbroken chain.
func TestWitnessEntriesRefusesEvidenceThatDoesNotVerify(t *testing.T) {
	_, stranger, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	strangerPublic := stranger.Public().(ed25519.PublicKey)
	for name, tc := range map[string]struct {
		tamper func(*fakeWitness)
		after  uint64
		says   string
	}{
		"cursor signed by another key":     {tamper: func(f *fakeWitness) { f.signWith = stranger }},
		"cursor signature altered":         {tamper: func(f *fakeWitness) { f.flipSignature = true }},
		"cursor too old":                   {tamper: func(f *fakeWitness) { f.cursorIssuedAt = f.now.Add(-2 * time.Minute) }},
		"cursor from the future":           {tamper: func(f *fakeWitness) { f.cursorIssuedAt = f.now.Add(2 * time.Minute) }},
		"head below the reconciled cursor": {tamper: func(*fakeWitness) {}, after: 5, says: "rolled back"},
		"records withheld":                 {tamper: func(f *fakeWitness) { f.dropRecords = true }},
		"a record off the chain":           {tamper: func(f *fakeWitness) { f.breakChainAt = 2 }, says: "does not chain"},
		"a record signed by another key":   {tamper: func(f *fakeWitness) { f.resignAt, f.signWithAtRead = 2, stranger }, says: "record 2 is not signed"},
		"a record whose entry was altered": {tamper: func(f *fakeWitness) { f.alterEntryAt = 2 }, says: "does not hash"},
		"another key published":            {tamper: func(f *fakeWitness) { f.publishKey = strangerPublic }},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeWitness(t)
			writer := newTestWitnessLog(t, fake)
			for _, op := range []string{"op/1", "op/2", "op/3"} {
				_, err := writer.Append(context.Background(), narrowing(op))
				require.NoError(t, err)
			}
			reader := newTestWitnessLog(t, fake)
			tc.tamper(fake)
			records, err := reader.Entries(context.Background(), tc.after, 500)
			require.ErrorIs(t, err, ErrPolicyLogWitnessEvidence)
			require.Contains(t, err.Error(), tc.says)
			require.Nil(t, records)
		})
	}
}

// The published key is compared, never adopted: a mismatch is refused before a
// cursor is even asked for, and the pin still decides every later verification.
func TestWitnessNeverAdoptsThePublishedKey(t *testing.T) {
	fake := newFakeWitness(t)
	log := newTestWitnessLog(t, fake)
	_, stranger, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	fake.publishKey = stranger.Public().(ed25519.PublicKey)

	_, err = log.Entries(context.Background(), 0, 500)
	require.ErrorIs(t, err, ErrPolicyLogWitnessEvidence)
	require.Contains(t, err.Error(), "never adopted")
	require.Zero(t, fake.cursorCalls)
	require.Equal(t, fake.pin(t).KeyID, log.key.KeyID)
}

// A cursor below what this client verified is a rollback even when the host's
// persisted cursor has not caught up yet.
func TestWitnessEntriesRefusesAHeadBelowWhatWasVerified(t *testing.T) {
	fake := newFakeWitness(t)
	log := newTestWitnessLog(t, fake)
	for _, op := range []string{"op/1", "op/2"} {
		_, err := log.Append(context.Background(), narrowing(op))
		require.NoError(t, err)
	}
	fake.cursorHeadDelta = -1
	_, err := log.Entries(context.Background(), 0, 500)
	require.ErrorIs(t, err, ErrPolicyLogWitnessEvidence)
	require.Contains(t, err.Error(), "rolled back")
}

func TestPolicyLogWitnessKeyDocument(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	digest := sha256.Sum256(public)
	document := func(mutate func(map[string]any)) []byte {
		fields := map[string]any{
			"algorithm":  "Ed25519",
			"key_id":     hex.EncodeToString(digest[:]),
			"public_key": base64.StdEncoding.EncodeToString(public),
		}
		mutate(fields)
		encoded, err := json.Marshal(fields)
		require.NoError(t, err)
		return encoded
	}

	key, err := ParsePolicyLogWitnessKey(document(func(map[string]any) {}))
	require.NoError(t, err)
	require.Equal(t, hex.EncodeToString(digest[:]), key.KeyID)
	require.True(t, key.PublicKey.Equal(public))

	for name, mutate := range map[string]func(map[string]any){
		"another algorithm":                func(f map[string]any) { f["algorithm"] = "ECDSA" },
		"key id of another key":            func(f map[string]any) { f["key_id"] = hex.EncodeToString(make([]byte, 32)) },
		"short key":                        func(f map[string]any) { f["public_key"] = base64.StdEncoding.EncodeToString(public[:16]) },
		"a field this host does not model": func(f map[string]any) { f["rotates_to"] = "x" },
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParsePolicyLogWitnessKey(document(mutate))
			require.ErrorIs(t, err, ErrPolicyLogWitnessKeyUnusable)
		})
	}
}

// --- Through the protocol: what the client's verdict does to a narrowing. ---

// controlPlaneOnly runs a control-plane transaction as a plain call; every other
// Store method panics, because these tests reach nothing else.
type controlPlaneOnly struct{ business.Store }

func (controlPlaneOnly) WithControlPlane(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

// receiptLedger is the local half: the receipts recorded and the commits.
type receiptLedger struct {
	recorded  map[string]*business.PolicyLogReceipt
	committed map[string]bool
}

func newReceiptLedger() *receiptLedger {
	return &receiptLedger{recorded: map[string]*business.PolicyLogReceipt{}, committed: map[string]bool{}}
}

func (l *receiptLedger) RecordPolicyLogAppend(_ context.Context, receipt *business.PolicyLogReceipt, entry *business.PolicyLogEntry) (bool, error) {
	if _, ok := l.recorded[entry.OperationID]; ok {
		return false, nil
	}
	l.recorded[entry.OperationID] = receipt
	return true, nil
}

func (l *receiptLedger) CommitPolicyLogOperation(_ context.Context, operationID string, _ time.Time) error {
	if _, ok := l.recorded[operationID]; !ok {
		return errors.New("no append was recorded for " + operationID)
	}
	l.committed[operationID] = true
	return nil
}

func (l *receiptLedger) UncommittedPolicyLogOperations(context.Context) ([]*business.PolicyLogGap, error) {
	return nil, nil
}

func (l *receiptLedger) PolicyLogCursorState(context.Context) (uint64, *time.Time, error) {
	return 0, nil, nil
}

func (l *receiptLedger) AdvancePolicyLogCursor(context.Context, uint64, time.Time) error { return nil }

func TestANarrowingCommitsOnlyAgainstAVerifiedReceipt(t *testing.T) {
	_, stranger, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	for name, tc := range map[string]struct {
		tamper  func(*fakeWitness)
		commits bool
	}{
		"a receipt the pinned key signed": {tamper: func(*fakeWitness) {}, commits: true},
		"a bad signature":                 {tamper: func(f *fakeWitness) { f.flipSignature = true }},
		"the wrong key":                   {tamper: func(f *fakeWitness) { f.signWith = stranger }},
		"a sequence regression":           {tamper: func(f *fakeWitness) { f.forceSeq = 1 }},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeWitness(t)
			log := newTestWitnessLog(t, fake)
			// One earlier narrowing, so a regression has something to regress
			// below.
			_, err := log.Append(context.Background(), narrowing("op/earlier"))
			require.NoError(t, err)

			service, err := business.NewService(controlPlaneOnly{})
			require.NoError(t, err)
			ledger := newReceiptLedger()
			service.SetPolicyLog(log, ledger)

			tc.tamper(fake)
			applied := false
			entry := narrowing("op/narrowing")
			err = service.WithPolicyLoggedNarrowing(context.Background(), entry, func(context.Context) error {
				applied = true
				return nil
			})
			if tc.commits {
				require.NoError(t, err)
				require.True(t, applied, "a witnessed narrowing applies")
				require.True(t, ledger.committed[entry.OperationID], "and commits its receipt")
				require.Equal(t, uint64(2), ledger.recorded[entry.OperationID].Sequence)
				return
			}
			require.ErrorIs(t, err, business.ErrPolicyLogUnreachable)
			require.ErrorIs(t, err, ErrPolicyLogWitnessEvidence)
			require.False(t, applied, "nothing narrows against evidence that does not verify")
			require.Empty(t, ledger.recorded, "and no receipt is recorded, so no gap opens")
		})
	}
}
