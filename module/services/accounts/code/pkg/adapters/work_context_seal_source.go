package adapters

import (
	"context"
	"errors"

	"accounts/pkg/business"

	"github.com/codefly-dev/core/workcontext"
)

// The host as core's SealSource, which is what makes the Work Context cutover
// possible at all.
//
// Core v0.9.0's Authenticator REQUIRES a SealSource — its own type comment says
// there is "no mode in which the seal comparison is skipped, or in which the
// authorization revision is assumed, by any entrypoint". So the host cannot
// verify a core capability without answering, live, the four questions a seal is
// compared against. This is the adapter that answers them from what the host
// already knows.
//
// EVERY METHOD IS A LIVE READ, and that is the point rather than an
// implementation detail. A seal records what was true at mint; these answer what
// is true now; and the difference is every revocation that reaches a capability
// already in flight. An implementation that cached, or that derived an answer
// from the capability being checked, would make the comparison a tautology —
// which is the defect three review rounds of this PR have caught in other
// places.

// WorkContextSealSource answers core's seal comparisons from the host's own
// state.
type WorkContextSealSource struct {
	authority business.ModuleAuthorityStore
	builds    business.ExecutionAuthority
	bindings  business.ModuleOperationBindingStore
	// tenant is the organisation these reads are scoped to.
	//
	// Core's SealSource takes no organisation: it asks about a principal and an
	// installation, because in its model those identify the authority. The host
	// needs one to read `installations` and `principal_authorization_revisions`,
	// both of which are per-organisation — so the scope is bound when the source
	// is constructed, for the tenant the authenticator serves, rather than
	// guessed per call from the capability being verified. Reading it from the
	// capability would let the capability choose the scope its own seal is
	// compared in.
	tenant string
}

// NewWorkContextSealSource binds a seal source to one tenant.
func NewWorkContextSealSource(
	tenant string,
	authority business.ModuleAuthorityStore,
	builds business.ExecutionAuthority,
	bindings business.ModuleOperationBindingStore,
) *WorkContextSealSource {
	return &WorkContextSealSource{
		authority: authority, builds: builds, bindings: bindings, tenant: tenant,
	}
}

// Seal answers the live installation a capability names.
//
// Core compares the returned installation id and revision against the seal's.
// `ErrModuleInstallationInactive` becomes an error rather than a zero Seal: a
// zero revision is a REAL revision a fresh organisation has, so returning one
// for a revoked installation would compare equal to a capability sealed before
// any revision existed.
func (s *WorkContextSealSource) Seal(
	ctx context.Context, principalID, installationID string,
) (workcontext.Seal, error) {
	if s.authority == nil {
		return workcontext.Seal{}, errors.New("no live authority read is wired, so no seal can be answered")
	}
	live, err := s.authority.LiveModuleAuthority(ctx, principalID, s.tenant, installationID)
	if err != nil {
		return workcontext.Seal{}, err
	}
	return workcontext.Seal{
		InstallationID:       live.InstallationID,
		InstallationRevision: live.InstallationRevision,
	}, nil
}

// PrincipalEpoch answers the principal's current envelope epoch.
//
// Read with no installation, because the epoch is a property of the principal in
// the organisation and not of any installation — passing one would make a
// principal's epoch unanswerable for a capability that names no installation,
// which is every capability until the mint seals one.
func (s *WorkContextSealSource) PrincipalEpoch(
	ctx context.Context, principalID string,
) (uint64, error) {
	if s.authority == nil {
		return 0, errors.New("no live authority read is wired, so no principal epoch can be answered")
	}
	live, err := s.authority.LiveModuleAuthority(ctx, principalID, s.tenant, "")
	if err != nil {
		return 0, err
	}
	return live.ProducerEpoch, nil
}

// ApprovedBuild answers what this host approves for a principal.
//
// Core's own documentation puts the monotonicity contract on the IMPLEMENTER:
// it compares for equality and cannot detect a source moving backwards. That
// contract is kept by `business.MonotonicApprovedBuilds`, including the clause
// core's own in-memory source missed — a digest change must come with an
// incarnation increase, or a swap-back at a fixed counter revives every
// capability the move away had revoked.
//
// The three states map onto core's: a record answers; a known principal bearing
// none is `ErrNoApprovedBuild`, which core reads as "bears no execution" and
// which is NOT a refusal; and an unknown principal is an error, because a
// capability for an identity this host has never heard of must not be admitted
// as merely unbound.
func (s *WorkContextSealSource) ApprovedBuild(
	ctx context.Context, principalID string,
) (string, uint64, error) {
	if s.builds == nil {
		return "", 0, errors.New("no approved-build authority is wired, so no execution can be answered")
	}
	digest, incarnation, err := s.builds.ApprovedBuild(ctx, principalID)
	switch {
	case errors.Is(err, business.ErrNoApprovedBuild):
		// Translated to core's sentinel, which its mint and verify branch on.
		// Returning the host's own error here would read to core as a failure
		// to answer rather than as "bears none", and those are different.
		return "", 0, workcontext.ErrNoApprovedBuild
	case err != nil:
		return "", 0, err
	}
	return string(digest), incarnation, nil
}

// OperationBinding resolves one binding by its id, exactly.
//
// By id and nothing else, which is core's contract and the host's own rule: the
// search this replaces walked a principal's bindings for one whose scopes
// contained the presented set, so a capability naming a narrow binding was
// satisfied by any wider binding the principal also held.
//
// The host's store takes a principal as well, because a binding is held BY a
// principal and a lookup that ignored it would resolve another principal's
// binding by id. Core asks without one, so the binding's own principal is
// returned for core to compare — the record answers who holds it rather than the
// caller asserting it.
func (s *WorkContextSealSource) OperationBinding(
	ctx context.Context, bindingID string,
) (workcontext.OperationBinding, error) {
	if s.bindings == nil {
		return workcontext.OperationBinding{}, errors.New("no operation-binding store is wired, so no binding can be resolved")
	}
	binding, err := s.bindings.OperationBindingByID(ctx, "", bindingID)
	if err != nil {
		return workcontext.OperationBinding{}, err
	}
	if binding == nil {
		return workcontext.OperationBinding{}, workcontext.ErrNoBinding
	}
	return workcontext.OperationBinding{
		ID:       binding.BindingID,
		Revision: binding.Revision,
	}, nil
}

var _ workcontext.SealSource = (*WorkContextSealSource)(nil)
