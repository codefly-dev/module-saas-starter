package business

import (
	"fmt"

	"github.com/google/uuid"
)

// NewID returns a fresh UUID v7 — time-ordered, non-guessable.
// Panics only if the crypto/rand source fails, which is unrecoverable.
func NewID() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		panic(fmt.Sprintf("business: failed to generate uuid v7: %v", err))
	}
	return id
}

// NewIDString returns NewID as a string. Use only at boundaries where a
// uuid.UUID isn't accepted (proto fields, SQL text columns).
func NewIDString() string {
	return NewID().String()
}

// ParseID parses a string into a uuid.UUID, returning an error for empty or
// malformed input. Prefer this over uuid.Parse to get consistent error shape.
func ParseID(s string) (uuid.UUID, error) {
	if s == "" {
		return uuid.Nil, fmt.Errorf("empty id")
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid id %q: %w", s, err)
	}
	return id, nil
}
