package business

import "context"

// narrow applies a change that only reduces access — a team member or an
// organization member removed, a role, a scope grant, a share, an API key, a
// principal, a delegation or a platform role revoked, an installation narrowed
// or uninstalled, a solution target closed — directly, in one control-plane
// transaction that also writes its audit record: apply emits the record with
// emitTx, so the narrowing and its audit commit together or not at all.
//
// No witness is asked first (owner decision, 2026-10-27): a narrowing that
// waited on an external policy log could not be performed at all on a host
// with none, which left access that could never be removed. Widening paths are
// unchanged.
func (s *Service) narrow(ctx context.Context, apply func(ctx context.Context) error) error {
	return s.store.WithControlPlane(ctx, apply)
}
