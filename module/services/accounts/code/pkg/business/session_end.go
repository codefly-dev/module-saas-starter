package business

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrNoOpenSession reports a session id that names no open session: unknown,
// ended (signed out, revoked) or past the moment it ends on its own.
var ErrNoOpenSession = errors.New("the bearer names no open session")

// ErrSessionEndIncomplete reports a session that was ended in the store while
// an access-revocation marker could not be written: nothing of it can be
// refreshed or reopened, but a bearer already issued stays accepted until it
// expires. The caller is told, because "ended" and "ended except for a few
// minutes" are different answers.
var ErrSessionEndIncomplete = errors.New("the session is ended, but a bearer already issued stays accepted until it expires: the revocation could not be recorded")

// SessionLifecycleStore is what ending a session and stating when one ends on
// its own need of the store. A session here is a row per bearer generation (a
// refresh rotates the row and keeps its family); a role view or a cast is a
// row and a family of its own, opened from the session the authority core's
// role-view record names as its parent.
type SessionLifecycleStore interface {
	// SessionEndsAt answers when the session id names ends on its own (its
	// family's absolute expiry, which a refresh never slides; a role view's
	// or a cast's own, which is its token's), and whether it is open now: its
	// row, or the row a refresh rotated it into, stands. A row stands when it
	// is not revoked, or was revoked only because its member's role in the
	// organisation changed: a role change leaves the session standing (it is
	// no longer refreshed, and the membership is read live), while a removal,
	// a sign-out or any other revocation ends it.
	SessionEndsAt(ctx context.Context, sessionID string) (endsAt time.Time, open bool, err error)
	// EndSessionTree revokes every standing row of the session's family and of
	// every session opened from it, transitively (its role views and casts,
	// and theirs), and returns what it revoked. The session it was itself
	// opened from is untouched.
	EndSessionTree(ctx context.Context, sessionID, reason string) ([]RevokedSession, error)
}

// SessionRevokedEnded is the reason recorded on the rows a session's End revoked.
const SessionRevokedEnded = "session_ended"

// SessionEndsAt is when the open session id names ends on its own. A session
// that is not open, or already past that moment, is ErrNoOpenSession: an ended
// or expired bearer is refused wherever a session is read.
func (s *Service) SessionEndsAt(ctx context.Context, sessionID string) (time.Time, error) {
	store, ok := s.store.(SessionLifecycleStore)
	if !ok {
		return time.Time{}, fmt.Errorf("this host's store (%T) states no session lifetimes", s.store)
	}
	var endsAt time.Time
	var open bool
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) (err error) {
		endsAt, open, err = store.SessionEndsAt(ctx, sessionID)
		return err
	}); err != nil {
		return time.Time{}, err
	}
	if !open || !time.Now().Before(endsAt) {
		return time.Time{}, ErrNoOpenSession
	}
	return endsAt, nil
}

// EndSession ends the session id names, of person, and with it every session
// opened from it: its role views and casts, and theirs. It is a sign-out, and
// the way back from a cast, whose parent session is untouched. The rows are
// revoked and the end recorded in one transaction; the access-revocation
// markers that make each bearer unauthenticated at the perimeter are written
// once it has committed, and a marker that cannot be written is reported
// (ErrSessionEndIncomplete) rather than swallowed: the caller asked for the
// bearer to stop working.
func (s *Service) EndSession(ctx context.Context, person, organization, sessionID string) error {
	store, ok := s.store.(SessionLifecycleStore)
	if !ok {
		return fmt.Errorf("this host's store (%T) ends no sessions", s.store)
	}
	var revoked []RevokedSession
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		if revoked, err = store.EndSessionTree(ctx, sessionID, SessionRevokedEnded); err != nil {
			return err
		}
		if len(revoked) == 0 {
			return ErrNoOpenSession
		}
		payload := map[string]any{"subject": sessionID, "session": sessionID, "actor": person, "ended": fmt.Sprint(len(revoked))}
		if organization != "" {
			payload["organization"] = organization
		}
		return s.emitTx(ctx, person, ActorTypeUser, EventAuthoritySessionEnded, "session", sessionID, "", payload)
	}); err != nil {
		return err
	}
	return s.publishSessionRevocations(ctx, revoked)
}

// publishSessionRevocations reports incomplete containment after the durable
// revocation committed, and attempts every marker even when one fails.
func (s *Service) publishSessionRevocations(ctx context.Context, revoked []RevokedSession) error {
	if s.minter == nil {
		return nil
	}
	var failed error
	for _, key := range accessRevocationKeys(revoked) {
		if err := s.minter.RevokeSessionAccess(ctx, key); err != nil {
			failed = errors.Join(failed, err)
		}
	}
	if failed != nil {
		return fmt.Errorf("%w: %v", ErrSessionEndIncomplete, failed)
	}
	return nil
}

// CheckCapabilitySession refuses ended interactive roots at the authority.
func (s *Service) CheckCapabilitySession(ctx context.Context, id string) error {
	store, ok := s.store.(interface {
		CheckCapabilitySession(context.Context, string) error
	})
	if !ok {
		return fmt.Errorf("host store cannot check capability sessions")
	}
	return s.store.WithControlPlane(ctx, func(ctx context.Context) error { return store.CheckCapabilitySession(ctx, id) })
}

// ReconcileSessionRevocations retries marker publication from durable session
// rows. A failed pass is retried on the next sweep, also after a restart.
func (s *Service) ReconcileSessionRevocations(ctx context.Context) error {
	store, ok := s.store.(interface {
		RevokedSessionMarkers(context.Context) ([]RevokedSession, error)
	})
	if !ok {
		return fmt.Errorf("host store cannot replay session revocations")
	}
	var rows []RevokedSession
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) (err error) { rows, err = store.RevokedSessionMarkers(ctx); return err }); err != nil {
		return err
	}
	return s.publishSessionRevocations(ctx, rows)
}
