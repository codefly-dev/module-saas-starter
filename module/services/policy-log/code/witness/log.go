package witness

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"policy-log/objectstore"
)

type head struct {
	Seq       uint64 `json:"seq"`
	ChainHash []byte `json:"chain_hash"`
}

type Log struct {
	store      objectstore.Store
	private    ed25519.PrivateKey
	public     ed25519.PublicKey
	gate       chan struct{}
	head       head
	generation int64
	records    map[uint64]Record
	operations map[string]uint64
}

// Open verifies the committed chain before the service may listen. A different
// signing key cannot reopen an existing log. The in-memory index is disposable:
// the immutable objects and generation-guarded head are the authority.
func Open(ctx context.Context, store objectstore.Store, key ed25519.PrivateKey) (*Log, error) {
	if store == nil || len(key) != ed25519.PrivateKeySize || !bytes.Equal(key, ed25519.NewKeyFromSeed(key.Seed())) {
		return nil, fmt.Errorf("policy log requires storage and a valid Ed25519 signing key")
	}
	l := &Log{store: store, private: bytes.Clone(key), public: bytes.Clone(key.Public().(ed25519.PublicKey)),
		gate: make(chan struct{}, 1), head: head{ChainHash: make([]byte, sha256.Size)},
		records: make(map[uint64]Record), operations: make(map[string]uint64)}
	if err := l.refresh(ctx); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Log) Keys() Key { return keyDocument(l.public) }

func (l *Log) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case l.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-l.gate
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func decode(data []byte, value any) error {
	if len(data) > objectstore.MaxObjectBytes {
		return ErrIntegrity
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return fmt.Errorf("%w: malformed object", ErrIntegrity)
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing object bytes", ErrIntegrity)
	}
	return nil
}

func (l *Log) checkRecord(r Record, seq uint64, prev []byte) error {
	h, err := EntryHash(r.Entry)
	if err != nil || r.Receipt.Seq != seq || !bytes.Equal(h, r.Receipt.EntryHash) ||
		!bytes.Equal(prev, r.Receipt.PrevHash) || !VerifyReceipt(l.public, r.Receipt) {
		return fmt.Errorf("%w: sequence %d", ErrIntegrity, seq)
	}
	return nil
}

func (l *Log) loadRecord(ctx context.Context, seq uint64) (Record, error) {
	obj, err := l.store.Read(ctx, EntryObject(seq))
	if err != nil {
		return Record{}, err
	}
	var r Record
	if err := decode(obj.Data, &r); err != nil {
		return Record{}, err
	}
	return r, ctx.Err()
}

// refresh never publishes a partial read, a lower cursor, or a changed prefix.
// Even an idempotent append re-reads the head: a cached receipt must not hide an
// unreachable or rolled-back authority store.
func (l *Log) refresh(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	obj, err := l.store.Read(ctx, HeadObject)
	if errors.Is(err, objectstore.ErrNotFound) {
		// Genesis is provisioned once in the independent trust domain. Treating
		// a deleted head as a new log could silently reuse old sequence numbers.
		return fmt.Errorf("%w: required head object is absent", ErrRollback)
	}
	if err != nil {
		return err
	}
	var next head
	if err := decode(obj.Data, &next); err != nil {
		return err
	}
	if obj.Generation <= 0 || len(next.ChainHash) != sha256.Size {
		return ErrIntegrity
	}
	if next.Seq < l.head.Seq || obj.Generation < l.generation ||
		(next.Seq == l.head.Seq && !bytes.Equal(next.ChainHash, l.head.ChainHash)) {
		return ErrRollback
	}
	if next.Seq == l.head.Seq {
		l.generation = obj.Generation
		return ctx.Err()
	}
	prev := l.head.ChainHash
	var pending []Record
	seen := make(map[string]bool)
	for seq := l.head.Seq + 1; ; seq++ {
		r, err := l.loadRecord(ctx, seq)
		if err != nil {
			return fmt.Errorf("read committed sequence %d: %w", seq, err)
		}
		if err := l.checkRecord(r, seq, prev); err != nil {
			return err
		}
		if _, exists := l.operations[r.Entry.OperationID]; exists || seen[r.Entry.OperationID] {
			return fmt.Errorf("%w: duplicate operation id", ErrIntegrity)
		}
		seen[r.Entry.OperationID] = true
		pending = append(pending, r)
		prev = r.Receipt.ChainHash
		if seq == next.Seq {
			break
		}
	}
	if !bytes.Equal(next.ChainHash, prev) {
		return ErrIntegrity
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, r := range pending {
		l.remember(r)
	}
	l.head, l.generation = next, obj.Generation
	return nil
}

func (l *Log) remember(r Record) {
	l.records[r.Receipt.Seq] = r
	l.operations[r.Entry.OperationID] = r.Receipt.Seq
}

func cloneReceipt(r Receipt) Receipt {
	r.EntryHash, r.PrevHash, r.ChainHash, r.Signature = bytes.Clone(r.EntryHash), bytes.Clone(r.PrevHash), bytes.Clone(r.ChainHash), bytes.Clone(r.Signature)
	return r
}

func cloneRecord(r Record) Record {
	r.Entry.NarrowingHash = bytes.Clone(r.Entry.NarrowingHash)
	r.Receipt = cloneReceipt(r.Receipt)
	return r
}

// Append's budget includes queueing and refreshing. There is one attempt at
// each conditional write, with no detached writer and no re-mint on conflict.
func (l *Log) Append(ctx context.Context, entry Entry) (*Receipt, error) {
	ctx, cancel := context.WithTimeout(ctx, AppendDeadline)
	defer cancel()
	if err := l.lock(ctx); err != nil {
		return nil, err
	}
	defer func() { <-l.gate }()
	if err := entry.Validate(); err != nil {
		return nil, err
	}
	entry.NarrowingHash = bytes.Clone(entry.NarrowingHash)
	hash, err := EntryHash(entry)
	if err != nil {
		return nil, err
	}
	if err := l.refresh(ctx); err != nil {
		return nil, err
	}
	if seq, ok := l.operations[entry.OperationID]; ok {
		r := l.records[seq].Receipt
		if !bytes.Equal(hash, r.EntryHash) {
			return nil, ErrConflict
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		copy := cloneReceipt(r)
		return &copy, nil
	}
	if l.head.Seq == math.MaxUint64 {
		return nil, fmt.Errorf("%w: sequence exhausted", ErrIntegrity)
	}
	seq := l.head.Seq + 1
	r, err := l.loadRecord(ctx, seq)
	switch {
	case err == nil:
		// A preceding call may have persisted this slot but lost its head write.
		// Only the identical operation may explicitly complete it. Never delete
		// the slot, silently skip it, or complete a different operation late.
		if err := l.checkRecord(r, seq, l.head.ChainHash); err != nil {
			return nil, err
		}
		if r.Entry.OperationID != entry.OperationID {
			return nil, ErrPending
		}
		if !bytes.Equal(hash, r.Receipt.EntryHash) {
			return nil, ErrConflict
		}
	case errors.Is(err, objectstore.ErrNotFound):
		r = Record{Entry: entry, Receipt: Receipt{Seq: seq, EntryHash: hash, PrevHash: bytes.Clone(l.head.ChainHash),
			ChainHash: ChainHash(seq, l.head.ChainHash, hash), IssuedAt: time.Now().UTC()}}
		r.Receipt.Signature = ed25519.Sign(l.private, ReceiptBytes(r.Receipt))
		body, err := json.Marshal(r)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, err := l.store.Write(ctx, EntryObject(seq), body, 0); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	next := head{Seq: seq, ChainHash: bytes.Clone(r.Receipt.ChainHash)}
	body, err := json.Marshal(next)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	generation, err := l.store.Write(ctx, HeadObject, body, l.generation)
	if err != nil {
		return nil, err
	}
	l.head, l.generation = next, generation
	l.remember(r)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	copy := cloneReceipt(r.Receipt)
	return &copy, nil
}

func (l *Log) Cursor(ctx context.Context) (*Cursor, error) {
	ctx, cancel := context.WithTimeout(ctx, AppendDeadline)
	defer cancel()
	if err := l.lock(ctx); err != nil {
		return nil, err
	}
	defer func() { <-l.gate }()
	if err := l.refresh(ctx); err != nil {
		return nil, err
	}
	c := &Cursor{HeadSeq: l.head.Seq, ChainHash: bytes.Clone(l.head.ChainHash), IssuedAt: time.Now().UTC()}
	c.Signature = ed25519.Sign(l.private, CursorBytes(*c))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c, nil
}

// Read starts at an inclusive sequence (1 is genesis). Asking beyond head+1
// refuses rather than making a restored store look like a quiet log. Callers
// persist their verified sequence and chain hash across replicas and restarts.
func (l *Log) Read(ctx context.Context, from uint64, limit uint32) ([]Record, error) {
	ctx, cancel := context.WithTimeout(ctx, AppendDeadline)
	defer cancel()
	if from == 0 || limit == 0 || limit > MaxReadLimit {
		return nil, ErrInvalid
	}
	if err := l.lock(ctx); err != nil {
		return nil, err
	}
	defer func() { <-l.gate }()
	if err := l.refresh(ctx); err != nil {
		return nil, err
	}
	if from > l.head.Seq && from-l.head.Seq > 1 {
		return nil, ErrRollback
	}
	records := make([]Record, 0, limit)
	for seq := from; seq <= l.head.Seq && uint32(len(records)) < limit; seq++ {
		records = append(records, cloneRecord(l.records[seq]))
		if seq == math.MaxUint64 {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return records, nil
}
