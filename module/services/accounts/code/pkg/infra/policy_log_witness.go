package infra

// The policy log's transport: a client for the module's independent signed
// witness (services/policy-log).
//
// WHY THIS IS A RECEIPT AND NOT A HOST DECISION. `business.PolicyLog.Append`
// must return a token the LOG minted and a sequence the LOG assigned, because
// that is the whole difference between "the log witnessed this narrowing" and
// "this host decided it". The witness assigns a global sequence, chains every
// entry to the one before it, and signs the receipt with an Ed25519 key that
// lives in its own trust domain — this host never holds it. What this client
// adds is VERIFICATION: every receipt and every cursor is checked against a key
// this host PINNED, so nothing the network says can make a receipt this host
// would have had to invent.
//
// WHERE TRUST COMES FROM. The pinned key is the trust-anchor document
// composition delivers beside the solution-host anchor,
// `policy_log_public_key.json` under SolutionHostTrustAnchorPath. The witness's
// own `Keys` operation is DISCOVERY: it is read only to name a mismatch
// precisely, and never adopted. A client that trusted the key the witness
// published would accept any witness that published a key — including one an
// attacker stood up — which is the trust-on-first-use hole the pin exists to
// close.
//
// HOW THIS HOST AUTHENTICATES TO THE WITNESS. It does not, in the application,
// and that is the witness's own contract rather than an omission here: its
// listener registers the four witness operations and nothing else, and its
// README requires composition to admit only the host's declared client through
// workload-authenticated mesh policy. A second credential minted by this host
// would be a second identity scheme the witness does not check. The mesh
// carries this workload's identity; the pin carries the witness's.
//
// WHAT THE WITNESS HOLDS. Opaque identifiers and a digest, never the narrowing:
// the entry carries the operation id, a kind naming the subject kind and the
// decision, the subject id, and the SHA-256 of this host's canonical encoding
// of the whole PolicyLogEntry (narrowingHash below). A record read back can
// therefore name the gap it is — which is all reconciliation does with it, see
// business.ReconcilePolicyLog — but cannot reconstruct the policy or the actor.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"accounts/pkg/business"
	policylogv1 "accounts/pkg/gen/saas/policylog/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// policyLogWitnessKeyFileName is the pinned witness key's name inside the trust
// anchor. Fixed, like the anchor's location and the other documents in it: the
// anchor is written by one platform-owned path, and a configurable name is one
// more thing a composition could point elsewhere.
const policyLogWitnessKeyFileName = "policy_log_public_key.json"

// ErrPolicyLogWitnessKeyUnusable reports a pinned key that was delivered and
// cannot be used. A host holding one must not start: it would refuse every
// narrowing for a reason nobody could see.
var ErrPolicyLogWitnessKeyUnusable = errors.New("pinned policy log witness key is unusable")

// ErrPolicyLogWitnessEvidence reports evidence the witness returned that does
// not verify under the pinned key, does not describe the bytes this host
// offered, or contradicts what this client has already verified.
//
// It is never treated as a refusal by the log. A refusal is a decision; this is
// a witness this host cannot believe, so the narrowing does not proceed and
// reconciliation does not advance — and the serving gate's staleness window
// closes on its own.
var ErrPolicyLogWitnessEvidence = errors.New("policy log witness evidence does not verify")

// PolicyLogWitnessKey is the pinned public half of the witness signing key.
type PolicyLogWitnessKey struct {
	KeyID     string
	PublicKey ed25519.PublicKey
}

// policyLogWitnessKeyDocument is the trust-anchor document's wire form, the
// schema the witness's Keys operation also uses. public_key is standard base64
// of the raw 32-byte key, which is what encoding/json does with []byte.
type policyLogWitnessKeyDocument struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id"`
	PublicKey []byte `json:"public_key"`
}

// ReadPolicyLogWitnessKey reads the pinned witness key from the trust anchor.
//
// The second return says whether a key is DELIVERED at all. False with no error
// is a host composition did not give a witness to; an error is a host that must
// not start, because the key is there and says something this host cannot use.
func ReadPolicyLogWitnessKey() (PolicyLogWitnessKey, bool, error) {
	path := filepath.Join(SolutionHostTrustAnchorPath, policyLogWitnessKeyFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return PolicyLogWitnessKey{}, false, nil
		}
		return PolicyLogWitnessKey{}, false, fmt.Errorf("%w: %s is present on the trust anchor and cannot be read: %w",
			ErrPolicyLogWitnessKeyUnusable, path, err)
	}
	key, err := ParsePolicyLogWitnessKey(data)
	if err != nil {
		return PolicyLogWitnessKey{}, false, fmt.Errorf("%s: %w", path, err)
	}
	return key, true, nil
}

// ParsePolicyLogWitnessKey decodes and checks one key document.
//
// Strict: an unknown field is a version step, not something to ignore, and a
// key id that is not the digest of the key beside it is a document that names
// one key and pins another.
func ParsePolicyLogWitnessKey(data []byte) (PolicyLogWitnessKey, error) {
	var document policyLogWitnessKeyDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return PolicyLogWitnessKey{}, fmt.Errorf("%w: does not decode: %w", ErrPolicyLogWitnessKeyUnusable, err)
	}
	if decoder.More() {
		return PolicyLogWitnessKey{}, fmt.Errorf("%w: trailing content after the key document", ErrPolicyLogWitnessKeyUnusable)
	}
	return policyLogWitnessKeyFrom(document.Algorithm, document.KeyID, document.PublicKey)
}

func policyLogWitnessKeyFrom(algorithm, keyID string, public []byte) (PolicyLogWitnessKey, error) {
	if algorithm != "Ed25519" {
		return PolicyLogWitnessKey{}, fmt.Errorf("%w: algorithm %q, want Ed25519", ErrPolicyLogWitnessKeyUnusable, algorithm)
	}
	if len(public) != ed25519.PublicKeySize {
		return PolicyLogWitnessKey{}, fmt.Errorf("%w: public key is %d bytes, want %d",
			ErrPolicyLogWitnessKeyUnusable, len(public), ed25519.PublicKeySize)
	}
	digest := sha256.Sum256(public)
	if keyID != hex.EncodeToString(digest[:]) {
		return PolicyLogWitnessKey{}, fmt.Errorf("%w: key_id is not the lowercase SHA-256 hex of the public key",
			ErrPolicyLogWitnessKeyUnusable)
	}
	return PolicyLogWitnessKey{KeyID: keyID, PublicKey: bytes.Clone(public)}, nil
}

// policyLogWitnessAttempts bounds how often one Append offers the identical
// entry. Only Unavailable is retried: the witness answers it exactly when no
// sequence was assigned and no receipt exists, so the retry cannot mint a
// second entry — and a retry of the identical bytes returns the original
// receipt if an earlier attempt did commit. Every other answer is final.
const policyLogWitnessAttempts = 3

// policyLogWitnessRetryPause is the pause between those attempts. All of them
// run inside the caller's five-second append budget.
const policyLogWitnessRetryPause = 100 * time.Millisecond

// policyLogCursorSkew is how far a signed cursor's issued_at may sit from this
// host's clock. A valid signature cannot tell a historical cursor from a
// current one, so without a freshness bound a replayed old cursor would keep
// `reached_at` fresh while the witness was gone. The bound is the staleness
// window itself: a cursor older than that proves nothing the gate would accept.
const policyLogCursorSkew = business.PolicyLogStaleAfter

// WitnessPolicyLog is business.PolicyLog over the witness's gRPC API.
type WitnessPolicyLog struct {
	client policylogv1.WitnessServiceClient
	key    PolicyLogWitnessKey
	now    func() time.Time

	mu sync.Mutex
	// highest and chain are the highest sequence this client has VERIFIED, from
	// a receipt, a record or a cursor, and the chain hash at it. They only move
	// forward: a witness that later answers below them, or with a different
	// hash at the same sequence, has rolled back or forked, and is refused.
	highest uint64
	chain   []byte
	// keyMatched records that the witness's published key equals the pin. It
	// decides nothing about verification, which always uses the pin; it is what
	// lets a mismatch be reported as one rather than as a bad signature.
	keyMatched bool
}

// NewWitnessPolicyLog builds the client. The key must be the pinned one.
func NewWitnessPolicyLog(client policylogv1.WitnessServiceClient, key PolicyLogWitnessKey) (*WitnessPolicyLog, error) {
	if client == nil {
		return nil, errors.New("policy log witness client is required")
	}
	if _, err := policyLogWitnessKeyFrom("Ed25519", key.KeyID, key.PublicKey); err != nil {
		return nil, err
	}
	return &WitnessPolicyLog{client: client, key: key, now: func() time.Time { return time.Now().UTC() }}, nil
}

var _ business.PolicyLog = (*WitnessPolicyLog)(nil)

// Append offers one entry and returns its verified receipt.
func (l *WitnessPolicyLog) Append(ctx context.Context, entry *business.PolicyLogEntry) (*business.PolicyLogReceipt, error) {
	if err := entry.Validate(); err != nil {
		return nil, err
	}
	// requested_at is fixed ONCE per operation: it is covered by the entry hash,
	// so every retry below must carry the identical value or the witness would
	// read the retry as a different entry under the same operation id.
	wire, err := witnessEntry(entry, l.now())
	if err != nil {
		return nil, err
	}
	expected, err := witnessEntryHash(wire)
	if err != nil {
		return nil, err
	}

	// Anything this client verified BEFORE offering the entry precedes it in
	// the log, so the receipt's sequence must be above it.
	floor, floorChain := l.verified()

	var response *policylogv1.AppendResponse
	for attempt := 1; ; attempt++ {
		response, err = l.client.Append(ctx, &policylogv1.AppendRequest{Entry: wire})
		if err == nil || status.Code(err) != codes.Unavailable || attempt == policyLogWitnessAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(policyLogWitnessRetryPause):
		}
	}
	if err != nil {
		return nil, fmt.Errorf("policy log witness append: %w", err)
	}

	receipt, err := receiptFrom(response.GetReceipt())
	if err != nil {
		return nil, err
	}
	if !verifyWitnessReceipt(l.key.PublicKey, receipt) {
		return nil, fmt.Errorf("%w: the receipt for operation %q is not signed by the pinned key %s",
			ErrPolicyLogWitnessEvidence, entry.OperationID, l.key.KeyID)
	}
	if !bytes.Equal(receipt.entryHash, expected) {
		return nil, fmt.Errorf("%w: the receipt for operation %q witnesses different bytes from the ones offered",
			ErrPolicyLogWitnessEvidence, entry.OperationID)
	}
	if receipt.seq <= floor {
		return nil, fmt.Errorf("%w: the receipt for operation %q has sequence %d, at or below %d already verified; the witness has rolled back",
			ErrPolicyLogWitnessEvidence, entry.OperationID, receipt.seq, floor)
	}
	if receipt.seq == floor+1 && !bytes.Equal(receipt.prevHash, floorChain) {
		return nil, fmt.Errorf("%w: the receipt for operation %q does not chain to sequence %d as verified; the witness has forked",
			ErrPolicyLogWitnessEvidence, entry.OperationID, floor)
	}
	l.observe(receipt.seq, receipt.chainHash)

	encoded, err := encodeWitnessReceipt(l.key.KeyID, receipt)
	if err != nil {
		return nil, err
	}
	return &business.PolicyLogReceipt{Receipt: encoded, Sequence: receipt.seq, AppendedAt: receipt.issuedAt}, nil
}

// Entries returns the verified records after a sequence, and is what refreshes
// the serving gate's `reached_at`: it succeeds only when the witness answered
// with a signed, fresh cursor at or above everything this host has accounted
// for, and every record it returned verifies and chains.
func (l *WitnessPolicyLog) Entries(ctx context.Context, afterSequence uint64, limit int) ([]*business.PolicyLogRecord, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("policy log read limit must be positive, got %d", limit)
	}
	if err := l.checkPublishedKey(ctx); err != nil {
		return nil, err
	}

	cursorResponse, err := l.client.Cursor(ctx, &policylogv1.CursorRequest{})
	if err != nil {
		return nil, fmt.Errorf("policy log witness cursor: %w", err)
	}
	cursor, err := cursorFrom(cursorResponse.GetCursor())
	if err != nil {
		return nil, err
	}
	if !verifyWitnessCursor(l.key.PublicKey, cursor) {
		return nil, fmt.Errorf("%w: the cursor is not signed by the pinned key %s", ErrPolicyLogWitnessEvidence, l.key.KeyID)
	}
	if skew := l.now().Sub(cursor.issuedAt); skew > policyLogCursorSkew || skew < -policyLogCursorSkew {
		return nil, fmt.Errorf("%w: the cursor was issued at %s, outside %s of this host's clock, so it does not show the witness is reachable now",
			ErrPolicyLogWitnessEvidence, cursor.issuedAt.Format(time.RFC3339Nano), policyLogCursorSkew)
	}
	if cursor.headSeq < afterSequence {
		return nil, fmt.Errorf("%w: the witness head is %d, below the %d this host has reconciled; the log has rolled back",
			ErrPolicyLogWitnessEvidence, cursor.headSeq, afterSequence)
	}
	floor, floorChain := l.verified()
	if cursor.headSeq < floor {
		return nil, fmt.Errorf("%w: the witness head is %d, below the %d this client has verified; the log has rolled back",
			ErrPolicyLogWitnessEvidence, cursor.headSeq, floor)
	}
	if cursor.headSeq == floor && floor != 0 && !bytes.Equal(cursor.chainHash, floorChain) {
		return nil, fmt.Errorf("%w: the witness head at %d names a different chain hash from the one verified; the log has forked",
			ErrPolicyLogWitnessEvidence, floor)
	}
	if cursor.headSeq == afterSequence {
		l.observe(cursor.headSeq, cursor.chainHash)
		return []*business.PolicyLogRecord{}, nil
	}

	if limit > policyLogWitnessMaxRead {
		limit = policyLogWitnessMaxRead
	}
	readResponse, err := l.client.Read(ctx, &policylogv1.ReadRequest{FromSeq: afterSequence + 1, Limit: uint32(limit)})
	if err != nil {
		return nil, fmt.Errorf("policy log witness read: %w", err)
	}
	if len(readResponse.GetRecords()) == 0 {
		// The head says there is more and the page says there is nothing: a
		// witness withholding entries reads exactly like a quiet log otherwise.
		return nil, fmt.Errorf("%w: the witness head is %d but a read from %d returned nothing",
			ErrPolicyLogWitnessEvidence, cursor.headSeq, afterSequence+1)
	}

	// The chain hash the first record must extend, when this client knows it.
	var previous []byte
	switch afterSequence {
	case 0:
		previous = make([]byte, sha256.Size)
	case floor:
		previous = floorChain
	}
	records := make([]*business.PolicyLogRecord, 0, len(readResponse.GetRecords()))
	var last *witnessReceipt
	for index, wire := range readResponse.GetRecords() {
		want := afterSequence + 1 + uint64(index)
		record, receipt, err := l.verifiedRecord(wire, want, previous)
		if err != nil {
			return nil, err
		}
		if want == floor && !bytes.Equal(receipt.chainHash, floorChain) {
			return nil, fmt.Errorf("%w: record %d names a different chain hash from the one verified; the log has forked",
				ErrPolicyLogWitnessEvidence, want)
		}
		if want == cursor.headSeq && !bytes.Equal(receipt.chainHash, cursor.chainHash) {
			return nil, fmt.Errorf("%w: record %d does not end the chain the signed cursor names", ErrPolicyLogWitnessEvidence, want)
		}
		previous = receipt.chainHash
		last = receipt
		records = append(records, record)
	}
	l.observe(last.seq, last.chainHash)
	return records, nil
}

// policyLogWitnessMaxRead is the witness's own page bound (witness.MaxReadLimit).
const policyLogWitnessMaxRead = 500

func (l *WitnessPolicyLog) verifiedRecord(
	wire *policylogv1.Record, want uint64, previous []byte,
) (*business.PolicyLogRecord, *witnessReceipt, error) {
	receipt, err := receiptFrom(wire.GetReceipt())
	if err != nil {
		return nil, nil, err
	}
	if receipt.seq != want {
		return nil, nil, fmt.Errorf("%w: expected record %d, the witness returned %d", ErrPolicyLogWitnessEvidence, want, receipt.seq)
	}
	if !verifyWitnessReceipt(l.key.PublicKey, receipt) {
		return nil, nil, fmt.Errorf("%w: record %d is not signed by the pinned key %s", ErrPolicyLogWitnessEvidence, want, l.key.KeyID)
	}
	entryHash, err := witnessEntryHash(wire.GetEntry())
	if err != nil {
		return nil, nil, fmt.Errorf("%w: record %d: %w", ErrPolicyLogWitnessEvidence, want, err)
	}
	if !bytes.Equal(entryHash, receipt.entryHash) {
		return nil, nil, fmt.Errorf("%w: record %d's entry does not hash to the digest its receipt signs", ErrPolicyLogWitnessEvidence, want)
	}
	if previous != nil && !bytes.Equal(receipt.prevHash, previous) {
		return nil, nil, fmt.Errorf("%w: record %d does not chain to the record before it", ErrPolicyLogWitnessEvidence, want)
	}
	kind, decision, err := parseWitnessKind(wire.GetEntry().GetKind())
	if err != nil {
		// A record this host cannot name is a gap no reconciliation could
		// act on. Refusing it keeps the cursor below it, so the staleness
		// window closes rather than the host serving past an entry it skipped.
		return nil, nil, fmt.Errorf("%w: record %d: %w", ErrPolicyLogWitnessEvidence, want, err)
	}
	encoded, err := encodeWitnessReceipt(l.key.KeyID, receipt)
	if err != nil {
		return nil, nil, err
	}
	return &business.PolicyLogRecord{
		// Actor, Policy, Supersedes and EnvelopeRevision are NOT here: the
		// witness holds their digest, never their bytes. Reconciliation needs
		// only the operation and its subject to name a gap.
		PolicyLogEntry: business.PolicyLogEntry{
			OperationID: wire.GetEntry().GetOperationId(),
			Decision:    decision,
			SubjectKind: kind,
			SubjectID:   wire.GetEntry().GetSubject(),
		},
		Receipt:  encoded,
		Sequence: receipt.seq,
		LoggedAt: receipt.issuedAt,
	}, receipt, nil
}

// checkPublishedKey compares the witness's published key with the pin until
// they have been seen to match once. It never replaces the pin.
func (l *WitnessPolicyLog) checkPublishedKey(ctx context.Context) error {
	l.mu.Lock()
	matched := l.keyMatched
	l.mu.Unlock()
	if matched {
		return nil
	}
	response, err := l.client.Keys(ctx, &policylogv1.KeysRequest{})
	if err != nil {
		return fmt.Errorf("policy log witness keys: %w", err)
	}
	published := response.GetKey()
	if published.GetKeyId() != l.key.KeyID || !bytes.Equal(published.GetPublicKey(), l.key.PublicKey) {
		return fmt.Errorf("%w: the witness publishes key %q and this host pins %q; nothing it signs can verify here, and a published key is never adopted in place of the pin",
			ErrPolicyLogWitnessEvidence, published.GetKeyId(), l.key.KeyID)
	}
	l.mu.Lock()
	l.keyMatched = true
	l.mu.Unlock()
	return nil
}

// verified returns the highest verified sequence and its chain hash. Before
// anything is verified that is sequence 0, whose chain hash is the witness's
// genesis value of 32 zero bytes — so the first receipt a fresh witness issues
// must chain to it.
func (l *WitnessPolicyLog) verified() (uint64, []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.highest == 0 {
		return 0, make([]byte, sha256.Size)
	}
	return l.highest, bytes.Clone(l.chain)
}

func (l *WitnessPolicyLog) observe(seq uint64, chain []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if seq > l.highest {
		l.highest, l.chain = seq, bytes.Clone(chain)
	}
}

// --- The canonical encodings. ---
//
// These restate the witness's published contract (its README "Canonical
// bytes", and witness/contract.go) rather than importing it: the witness is a
// separate Go module this service cannot link, which is the same reason its
// proto is copied here. The integration test verifies receipts the REAL witness
// signed with these functions, so the restatement is checked against the
// implementation it restates rather than against itself.

type witnessReceipt struct {
	seq       uint64
	entryHash []byte
	prevHash  []byte
	chainHash []byte
	issuedAt  time.Time
	signature []byte
}

type witnessCursor struct {
	headSeq   uint64
	chainHash []byte
	issuedAt  time.Time
	signature []byte
}

func put64(b *bytes.Buffer, n uint64) {
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], n)
	b.Write(raw[:])
}

func put32(b *bytes.Buffer, n uint32) {
	var raw [4]byte
	binary.BigEndian.PutUint32(raw[:], n)
	b.Write(raw[:])
}

func putString(b *bytes.Buffer, s string) {
	put32(b, uint32(len(s)))
	b.WriteString(s)
}

func putTime(b *bytes.Buffer, t time.Time) {
	put64(b, uint64(t.Unix()))
	put32(b, uint32(t.Nanosecond()))
}

// witnessEntryHash is entry_hash over a wire entry, after the checks the
// witness's own Entry.Validate makes on the parts the hash covers.
func witnessEntryHash(entry *policylogv1.Entry) ([]byte, error) {
	if entry == nil {
		return nil, errors.New("policy log entry is absent")
	}
	if len(entry.GetNarrowingHash()) != sha256.Size {
		return nil, errors.New("policy log entry carries no SHA-256 narrowing hash")
	}
	if err := entry.GetRequestedAt().CheckValid(); err != nil {
		return nil, fmt.Errorf("policy log entry has no valid requested_at: %w", err)
	}
	b := bytes.NewBufferString("policy-log.entry.v1\x00")
	for _, s := range []string{entry.GetOperationId(), entry.GetKind(), entry.GetSubject(), entry.GetTenant()} {
		putString(b, s)
	}
	b.Write(entry.GetNarrowingHash())
	putTime(b, entry.GetRequestedAt().AsTime())
	digest := sha256.Sum256(b.Bytes())
	return digest[:], nil
}

func witnessChainHash(seq uint64, prev, entry []byte) []byte {
	b := bytes.NewBufferString("policy-log.chain.v1\x00")
	put64(b, seq)
	b.Write(prev)
	b.Write(entry)
	digest := sha256.Sum256(b.Bytes())
	return digest[:]
}

func witnessReceiptBytes(r *witnessReceipt) []byte {
	b := bytes.NewBufferString("policy-log.receipt.v1\x00")
	put64(b, r.seq)
	b.Write(r.entryHash)
	b.Write(r.prevHash)
	b.Write(r.chainHash)
	putTime(b, r.issuedAt)
	return b.Bytes()
}

func witnessCursorBytes(c *witnessCursor) []byte {
	b := bytes.NewBufferString("policy-log.cursor.v1\x00")
	put64(b, c.headSeq)
	b.Write(c.chainHash)
	putTime(b, c.issuedAt)
	return b.Bytes()
}

// verifyWitnessReceipt checks the signature AND the chain link the signature
// covers: a receipt whose chain_hash is not the hash of its own sequence,
// previous hash and entry hash is not a link in any chain, however it is signed.
func verifyWitnessReceipt(key ed25519.PublicKey, r *witnessReceipt) bool {
	return len(key) == ed25519.PublicKeySize && r.seq != 0 &&
		bytes.Equal(r.chainHash, witnessChainHash(r.seq, r.prevHash, r.entryHash)) &&
		ed25519.Verify(key, witnessReceiptBytes(r), r.signature)
}

func verifyWitnessCursor(key ed25519.PublicKey, c *witnessCursor) bool {
	return len(key) == ed25519.PublicKeySize &&
		(c.headSeq != 0 || bytes.Equal(c.chainHash, make([]byte, sha256.Size))) &&
		ed25519.Verify(key, witnessCursorBytes(c), c.signature)
}

func receiptFrom(wire *policylogv1.Receipt) (*witnessReceipt, error) {
	if wire == nil {
		return nil, fmt.Errorf("%w: the witness returned no receipt", ErrPolicyLogWitnessEvidence)
	}
	if len(wire.GetEntryHash()) != sha256.Size || len(wire.GetPrevHash()) != sha256.Size ||
		len(wire.GetChainHash()) != sha256.Size || len(wire.GetSignature()) != ed25519.SignatureSize {
		return nil, fmt.Errorf("%w: the receipt's digests or signature have the wrong length", ErrPolicyLogWitnessEvidence)
	}
	if err := wire.GetIssuedAt().CheckValid(); err != nil {
		return nil, fmt.Errorf("%w: the receipt has no valid issued_at", ErrPolicyLogWitnessEvidence)
	}
	return &witnessReceipt{
		seq:       wire.GetSeq(),
		entryHash: bytes.Clone(wire.GetEntryHash()),
		prevHash:  bytes.Clone(wire.GetPrevHash()),
		chainHash: bytes.Clone(wire.GetChainHash()),
		issuedAt:  wire.GetIssuedAt().AsTime(),
		signature: bytes.Clone(wire.GetSignature()),
	}, nil
}

func cursorFrom(wire *policylogv1.Cursor) (*witnessCursor, error) {
	if wire == nil {
		return nil, fmt.Errorf("%w: the witness returned no cursor", ErrPolicyLogWitnessEvidence)
	}
	if len(wire.GetChainHash()) != sha256.Size || len(wire.GetSignature()) != ed25519.SignatureSize {
		return nil, fmt.Errorf("%w: the cursor's digest or signature has the wrong length", ErrPolicyLogWitnessEvidence)
	}
	if err := wire.GetIssuedAt().CheckValid(); err != nil {
		return nil, fmt.Errorf("%w: the cursor has no valid issued_at", ErrPolicyLogWitnessEvidence)
	}
	return &witnessCursor{
		headSeq:   wire.GetHeadSeq(),
		chainHash: bytes.Clone(wire.GetChainHash()),
		issuedAt:  wire.GetIssuedAt().AsTime(),
		signature: bytes.Clone(wire.GetSignature()),
	}, nil
}

// witnessKind names the subject kind and the decision in the entry's kind, so a
// record read back can be named as the gap it is. Both are closed sets.
func witnessKind(entry *business.PolicyLogEntry) string {
	return string(entry.SubjectKind) + "." + string(entry.Decision)
}

func parseWitnessKind(kind string) (business.PolicyLogSubjectKind, business.PolicyLogDecision, error) {
	cut := strings.LastIndex(kind, ".")
	if cut <= 0 {
		return "", "", fmt.Errorf("entry kind %q is not one this host writes", kind)
	}
	subject := business.PolicyLogSubjectKind(kind[:cut])
	decision := business.PolicyLogDecision(kind[cut+1:])
	if !subject.Valid() || (decision != business.PolicyLogNarrowed && decision != business.PolicyLogRegranted) {
		return "", "", fmt.Errorf("entry kind %q is not one this host writes", kind)
	}
	return subject, decision, nil
}

// witnessEntry is the wire entry for one operation, at one requested_at.
//
// Tenant is empty on purpose: the witness's tenant grants no authority and
// scopes nothing, and an organisation id is domain content the narrowing hash
// already covers.
func witnessEntry(entry *business.PolicyLogEntry, requestedAt time.Time) (*policylogv1.Entry, error) {
	digest, err := narrowingHash(entry)
	if err != nil {
		return nil, err
	}
	return &policylogv1.Entry{
		OperationId:   entry.OperationID,
		Kind:          witnessKind(entry),
		Subject:       entry.SubjectID,
		NarrowingHash: digest,
		RequestedAt:   timestamppb.New(requestedAt),
	}, nil
}

// narrowingHash is the SHA-256 of this host's canonical encoding of the whole
// operation. Every field of the entry is covered, so the receipt binds the
// policy and the actor even though the witness never sees either.
//
//	SHA256("accounts.policy-log.narrowing.v1\0" ||
//	  S(operation_id) || S(decision) || S(subject_kind) || S(subject_id) ||
//	  S(supersedes) || U64(envelope_revision) || S(actor) || S(policy_json))
//
// policy_json is encoding/json's rendering of the policy map, whose object keys
// are sorted at every depth, so equal policies encode to equal bytes.
func narrowingHash(entry *business.PolicyLogEntry) ([]byte, error) {
	policy, err := json.Marshal(entry.Policy)
	if err != nil {
		return nil, fmt.Errorf("policy log entry %q: policy does not encode: %w", entry.OperationID, err)
	}
	b := bytes.NewBufferString("accounts.policy-log.narrowing.v1\x00")
	putString(b, entry.OperationID)
	putString(b, string(entry.Decision))
	putString(b, string(entry.SubjectKind))
	putString(b, entry.SubjectID)
	putString(b, entry.Supersedes)
	put64(b, entry.EnvelopeRevision)
	putString(b, entry.Actor)
	putString(b, string(policy))
	digest := sha256.Sum256(b.Bytes())
	return digest[:], nil
}

// witnessReceiptDocument is the receipt as this host stores it: the witness's
// own fields plus the id of the key that verified it, so the stored row is
// evidence anyone holding the pinned key can re-verify offline.
type witnessReceiptDocument struct {
	Version   int    `json:"v"`
	KeyID     string `json:"key_id"`
	Seq       uint64 `json:"seq"`
	EntryHash string `json:"entry_hash"`
	PrevHash  string `json:"prev_hash"`
	ChainHash string `json:"chain_hash"`
	IssuedAt  string `json:"issued_at"`
	Signature string `json:"signature"`
}

func encodeWitnessReceipt(keyID string, r *witnessReceipt) (string, error) {
	encoded, err := json.Marshal(witnessReceiptDocument{
		Version:   1,
		KeyID:     keyID,
		Seq:       r.seq,
		EntryHash: base64.StdEncoding.EncodeToString(r.entryHash),
		PrevHash:  base64.StdEncoding.EncodeToString(r.prevHash),
		ChainHash: base64.StdEncoding.EncodeToString(r.chainHash),
		IssuedAt:  r.issuedAt.UTC().Format(time.RFC3339Nano),
		Signature: base64.StdEncoding.EncodeToString(r.signature),
	})
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
