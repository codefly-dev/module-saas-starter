package auth_test

import (
	"bytes"
	"context"
	"errors"
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

func (s *memoryStore) RotateRefresh(
	_ context.Context,
	hash []byte,
	replacement func(current *auth.SessionRecord, authorization auth.RefreshAuthorization) (*auth.SessionRecord, error),
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.records {
		if !bytes.Equal(s.records[i].RefreshHash, hash) {
			continue
		}
		if s.records[i].RevokedAt != nil {
			if s.records[i].RevokedReason != "rotated" {
				return auth.ErrRefreshRevoked
			}
			now := time.Now()
			for j := range s.records {
				if s.records[j].UserID == s.records[i].UserID && s.records[j].RevokedAt == nil {
					t := now
					s.records[j].RevokedAt = &t
					s.records[j].RevokedReason = "refresh-reuse-all-sessions"
				}
			}
			return auth.ErrRefreshReuse
		}

		current := s.records[i]
		authorization := auth.RefreshAuthorization{
			OrgID:        current.OrgID,
			OrgRole:      current.OrgRole,
			PlatformRole: current.PlatformRole,
			MFAEnrolled: auth.Assurance{
				AuthenticationMethods: current.AuthenticationMethods,
				Level:                 current.AssuranceLevel,
				MFAVerifiedAt:         current.MFAVerifiedAt,
			}.HasMFAEvidence(),
		}
		next, err := replacement(&current, authorization)
		if err != nil {
			if reason, terminal := auth.RefreshRejectionReason(err); terminal {
				now := time.Now()
				for j := range s.records {
					if s.records[j].FamilyID == current.FamilyID && s.records[j].RevokedAt == nil {
						t := now
						s.records[j].RevokedAt = &t
						s.records[j].RevokedReason = reason
					}
				}
				return auth.ErrRefreshRevoked
			}
			return err
		}
		if next == nil || next.UserID != current.UserID || next.FamilyID != current.FamilyID ||
			next.ID == current.ID || bytes.Equal(next.RefreshHash, current.RefreshHash) {
			return errors.New("memory session store: invalid refresh replacement")
		}
		now := time.Now()
		for j := range s.records {
			if s.records[j].FamilyID == current.FamilyID && s.records[j].RevokedAt == nil {
				t := now
				s.records[j].RevokedAt = &t
				s.records[j].RevokedReason = "rotated"
			}
		}
		s.records = append(s.records, *next)
		return nil
	}
	return auth.ErrRefreshRevoked
}

func (s *memoryStore) ExchangeOrganization(
	_ context.Context,
	userID uuid.UUID,
	sessionID uuid.UUID,
	targetOrgID uuid.UUID,
	issue func(current *auth.SessionRecord, authorization auth.RefreshAuthorization) error,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.records {
		current := &s.records[i]
		if current.ID != sessionID || current.UserID != userID || current.RevokedAt != nil {
			continue
		}
		authorization := auth.RefreshAuthorization{
			OrgID:        targetOrgID,
			OrgRole:      current.OrgRole,
			PlatformRole: current.PlatformRole,
		}
		if err := issue(current, authorization); err != nil {
			return err
		}
		current.OrgID = authorization.OrgID
		current.OrgRole = authorization.OrgRole
		current.PlatformRole = authorization.PlatformRole
		return nil
	}
	return auth.ErrSessionUnavailable
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
