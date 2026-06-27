package auth_test

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"accounts/pkg/auth"
)

// memoryStore is a concurrent-safe in-memory SessionStore used by unit tests
// in this and child packages. It enforces the same invariants as the real
// Postgres store: insert-only, revoke-by-family, constant-time lookup.
type memoryStore struct {
	mu      sync.Mutex
	records []auth.SessionRecord
}

func newMemoryStore() *memoryStore { return &memoryStore{} }

func (s *memoryStore) Insert(_ context.Context, rec *auth.SessionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, *rec)
	return nil
}

func (s *memoryStore) FindByRefreshHash(_ context.Context, hash []byte) (*auth.SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.records {
		if bytes.Equal(s.records[i].RefreshHash, hash) {
			r := s.records[i]
			return &r, nil
		}
	}
	return nil, auth.ErrRefreshRevoked
}

func (s *memoryStore) RevokeFamily(_ context.Context, familyID uuid.UUID, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for i := range s.records {
		if s.records[i].FamilyID == familyID && s.records[i].RevokedAt == nil {
			t := now
			s.records[i].RevokedAt = &t
			s.records[i].RevokedReason = reason
		}
	}
	return nil
}

func (s *memoryStore) RevokeByUserID(_ context.Context, userID uuid.UUID, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for i := range s.records {
		if s.records[i].UserID == userID && s.records[i].RevokedAt == nil {
			t := now
			s.records[i].RevokedAt = &t
			s.records[i].RevokedReason = reason
		}
	}
	return nil
}
