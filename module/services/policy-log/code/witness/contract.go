// Package witness implements an external, signed, append-only policy log.
package witness

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"

	"policy-log/objectstore"
)

const (
	AppendDeadline = 5 * time.Second
	MaxReadLimit   = 500
	HeadObject     = objectstore.HeadObject
)

var (
	ErrInvalid   = errors.New("invalid policy log request")
	ErrConflict  = errors.New("operation id already names a different entry")
	ErrIntegrity = errors.New("policy log integrity check failed")
	ErrRollback  = errors.New("policy log head moved backwards or changed history")
	ErrPending   = errors.New("next sequence holds an uncommitted operation; retry that exact operation")
)

// Entry carries opaque identifiers, never domain content. NarrowingHash is the
// SHA-256 of the caller's canonical operation bytes. RequestedAt belongs to that
// operation and must remain unchanged on a retry; it is not a request deadline.
type Entry struct {
	OperationID   string    `json:"operation_id"`
	Kind          string    `json:"kind"`
	Subject       string    `json:"subject"`
	Tenant        string    `json:"tenant"`
	NarrowingHash []byte    `json:"narrowing_hash"`
	RequestedAt   time.Time `json:"requested_at"`
}

type Receipt struct {
	Seq       uint64    `json:"seq"`
	EntryHash []byte    `json:"entry_hash"`
	PrevHash  []byte    `json:"prev_hash"`
	ChainHash []byte    `json:"chain_hash"`
	IssuedAt  time.Time `json:"issued_at"`
	Signature []byte    `json:"signature"`
}

type Record struct {
	Entry   Entry   `json:"entry"`
	Receipt Receipt `json:"receipt"`
}

type Cursor struct {
	HeadSeq   uint64    `json:"head_seq"`
	ChainHash []byte    `json:"chain_hash"`
	IssuedAt  time.Time `json:"issued_at"`
	Signature []byte    `json:"signature"`
}

// Key is also the schema of the public trust-anchor document. A network key
// response is discovery, never authority to replace a caller's pinned key.
type Key struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id"`
	PublicKey []byte `json:"public_key"`
}

func validTime(t time.Time) bool { return !t.IsZero() && t.Year() >= 1 && t.Year() <= 9999 }

func (e Entry) Validate() error {
	for _, field := range []struct {
		value    string
		required bool
		max      int
	}{
		{e.OperationID, true, 1024}, {e.Kind, true, 128}, {e.Subject, true, 1024}, {e.Tenant, false, 1024},
	} {
		if (field.required && field.value == "") || len(field.value) > field.max || !utf8.ValidString(field.value) {
			return fmt.Errorf("%w: identifier is missing, oversized or not UTF-8", ErrInvalid)
		}
		for _, r := range field.value {
			if unicode.IsControl(r) {
				return fmt.Errorf("%w: control character in identifier", ErrInvalid)
			}
		}
	}
	if len(e.NarrowingHash) != sha256.Size || !validTime(e.RequestedAt) {
		return fmt.Errorf("%w: SHA-256 digest and requested_at are required", ErrInvalid)
	}
	return nil
}

// Canonical encodings use domain separation, big-endian unsigned integers,
// u32-length-prefixed UTF-8 strings, raw 32-byte digests, and a timestamp encoded
// as Unix seconds (int64's two's-complement bits) then u32 nanoseconds. There is
// no JSON serialization or protobuf field-order dependency in a signature.
func put64(b *bytes.Buffer, n uint64)     { _ = binary.Write(b, binary.BigEndian, n) }
func put32(b *bytes.Buffer, n uint32)     { _ = binary.Write(b, binary.BigEndian, n) }
func putString(b *bytes.Buffer, s string) { put32(b, uint32(len(s))); b.WriteString(s) }
func putTime(b *bytes.Buffer, t time.Time) {
	put64(b, uint64(t.Unix()))
	put32(b, uint32(t.Nanosecond()))
}

func EntryHash(e Entry) ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	b := bytes.NewBufferString("policy-log.entry.v1\x00")
	for _, s := range []string{e.OperationID, e.Kind, e.Subject, e.Tenant} {
		putString(b, s)
	}
	b.Write(e.NarrowingHash)
	putTime(b, e.RequestedAt)
	h := sha256.Sum256(b.Bytes())
	return h[:], nil
}

func ChainHash(seq uint64, prev, entry []byte) []byte {
	b := bytes.NewBufferString("policy-log.chain.v1\x00")
	put64(b, seq)
	b.Write(prev)
	b.Write(entry)
	h := sha256.Sum256(b.Bytes())
	return h[:]
}

func ReceiptBytes(r Receipt) []byte {
	b := bytes.NewBufferString("policy-log.receipt.v1\x00")
	put64(b, r.Seq)
	b.Write(r.EntryHash)
	b.Write(r.PrevHash)
	b.Write(r.ChainHash)
	putTime(b, r.IssuedAt)
	return b.Bytes()
}

func CursorBytes(c Cursor) []byte {
	b := bytes.NewBufferString("policy-log.cursor.v1\x00")
	put64(b, c.HeadSeq)
	b.Write(c.ChainHash)
	putTime(b, c.IssuedAt)
	return b.Bytes()
}

func VerifyReceipt(key ed25519.PublicKey, r Receipt) bool {
	return len(key) == ed25519.PublicKeySize && r.Seq != 0 && validTime(r.IssuedAt) &&
		len(r.EntryHash) == sha256.Size && len(r.PrevHash) == sha256.Size && len(r.ChainHash) == sha256.Size &&
		bytes.Equal(r.ChainHash, ChainHash(r.Seq, r.PrevHash, r.EntryHash)) && ed25519.Verify(key, ReceiptBytes(r), r.Signature)
}

func VerifyCursor(key ed25519.PublicKey, c Cursor) bool {
	return len(key) == ed25519.PublicKeySize && len(c.ChainHash) == sha256.Size && validTime(c.IssuedAt) &&
		(c.HeadSeq != 0 || bytes.Equal(c.ChainHash, make([]byte, sha256.Size))) && ed25519.Verify(key, CursorBytes(c), c.Signature)
}

func EntryObject(seq uint64) string { return fmt.Sprintf("entries/%020d.json", seq) }

func keyDocument(public ed25519.PublicKey) Key {
	id := sha256.Sum256(public)
	return Key{Algorithm: "Ed25519", KeyID: hex.EncodeToString(id[:]), PublicKey: bytes.Clone(public)}
}
