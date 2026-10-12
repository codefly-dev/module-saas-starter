// Package objectstore is the policy log's only persistence capability.
package objectstore

import (
	"context"
	"errors"
)

var (
	ErrNotFound     = errors.New("object not found")
	ErrPrecondition = errors.New("object generation precondition lost")
)

const MaxObjectBytes = 64 << 10

const HeadObject = "head.json"

type Object struct {
	Data       []byte
	Generation int64
}

// Store must provide strongly consistent reads and atomic generation checks.
// Write with generation zero creates an absent object; a positive generation
// replaces exactly that generation. Neither method retries a write. Both must
// honor cancellation, including immediately before a mutation. There is no
// delete, unconditional overwrite, listing, or database capability.
type Store interface {
	Read(context.Context, string) (Object, error)
	Write(context.Context, string, []byte, int64) (int64, error)
}
