package business

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Enforcement at USE: every capability decision re-reads the live authority
// rather than trusting what the credential sealed.
//
// WHY BOTH HALVES BIND. A credential seals a ceiling at mint time, and the
// declared registry says what a module principal may ever do. Neither alone is
// enough:
//
//   - re-reading alone means a WIDENED grant, or a database restored to a
//     broader state, retroactively widens a credential already in flight;
//   - sealing alone means a NARROWED grant does not take effect until the
//     outstanding credential expires.
//
// So the effective authority is the intersection — `min(sealed, live)` — and the
// live half is this file. Narrowing takes effect immediately through the
// re-read; widening never reaches a credential already issued, because the
// sealed ceiling bounds it.
//
// THE REVOCATION PREDICATE IS A CONJUNCTION, and each term closes a different
// hole:
//
//   - INSTALLATION FRESHNESS. The organisation's installation must still be
//     active, and its revision must match what the credential was minted
//     against. Without the revision, an uninstall-and-reinstall produces a new
//     installation that an old credential would satisfy.
//   - PRODUCER EPOCH. Bumped by any narrowing of the principal's envelope.
//     Without it, a narrowing revokes only the credential it happened to be
//     holding, and the replacement is reminted with the OLD authority — the
//     narrowing then takes effect for exactly one credential's lifetime and then
//     undoes itself.
//   - BINDING REVISION AND INCARNATION. For an operation context, the exact
//     binding the credential named, at the revision it named, bound to the
//     execution it was minted for. Verification is an EXACT LOOKUP of that
//     binding, never a search of the principal's bindings for one that happens
//     to contain the presented scopes.
//
// WHAT IS NOT BUILT YET is named here rather than implied, because this file is
// what a reader will check: the sealed half needs core's Work Context, which
// this host has not cut over to, so `SealedAuthority` below is populated from
// the credential only where the credential carries it. Until the cutover, the
// intersection is the live half AND whatever the credential could carry — which
// is strictly stronger than what preceded it (nothing) and strictly weaker than
// the target.

// ErrModuleAuthorityStale reports that a capability's live authority no longer
// matches what it was minted against.
//
// Distinct from "not a registered module principal": that one is an identity
// this host never knew, while this is an identity whose authority MOVED. A
// client's right response differs — the first is terminal, the second is
// re-mint-once.
var ErrModuleAuthorityStale = errors.New("module capability authority has moved since it was minted")

// ErrModuleInstallationInactive reports that the organisation's installation for
// this principal is revoked or absent.
var ErrModuleInstallationInactive = errors.New("module principal has no active installation in this organization")

// statusWrapped carries a gRPC status AND wraps a sentinel error.
//
// Both are needed and neither substitutes for the other. The adapter layer turns
// the status into a wire code, while callers inside this package branch on the
// sentinel with errors.Is — a deferred worker deciding whether work is terminal
// is the case that matters. `status.Errorf("%s", sentinel)` FORMATS the sentinel
// into the message and does not wrap it, so errors.Is returns false while the
// text looks right: the first version of this file did exactly that, and the
// tests caught it only because they asserted on the chain rather than on the
// string.
type statusWrapped struct {
	status *status.Status
	cause  error
}

func (e *statusWrapped) Error() string              { return e.status.Message() }
func (e *statusWrapped) Unwrap() error              { return e.cause }
func (e *statusWrapped) GRPCStatus() *status.Status { return e.status }

// wrapStatus builds an error carrying both a code and a cause.
func wrapStatus(code codes.Code, cause error, format string, args ...any) error {
	message := fmt.Sprintf("%s: %s", cause, fmt.Sprintf(format, args...))
	return &statusWrapped{status: status.New(code, message), cause: cause}
}

// ModuleCapabilityAuthority is the authority one capability decision may act on:
// the declared ceiling, and the live facts it was re-read against.
type ModuleCapabilityAuthority struct {
	Caller ModuleCaller
	Grant  ModulePrincipalGrant

	// InstallationID and InstallationRevision are the live installation this
	// capability acts under. Carried so a caller that needs the installation —
	// to scope a queue read, to attribute a produced record — uses the one that
	// was CHECKED rather than re-reading and possibly getting a different one.
	InstallationID       string
	InstallationRevision uint64

	// ProducerEpoch is the principal's envelope epoch as re-read. A deferred
	// producer records it so work it enqueues can be refused later if the
	// epoch has moved by the time the work runs.
	ProducerEpoch uint64
}

// ModuleAuthorityStore is the live authority read.
//
// One method, deliberately: every term of the revocation predicate is read in
// ONE statement, because reading them separately lets a revoke land between two
// reads and produce a decision describing a state that never existed.
type ModuleAuthorityStore interface {
	// LiveModuleAuthority reads the installation and epoch facts for one module
	// principal in one organisation, at one snapshot.
	//
	// Returns ErrModuleInstallationInactive when there is no active
	// installation, rather than zero values: a capability for an uninstalled
	// solution must be refused, and zero values would read as "revision 0",
	// which a credential minted before any revision could match.
	LiveModuleAuthority(ctx context.Context, principalID, orgID string) (*LiveModuleAuthority, error)
}

// LiveModuleAuthority is what the live read answers.
type LiveModuleAuthority struct {
	InstallationID       string
	InstallationRevision uint64
	ProducerEpoch        uint64
}

// AuthorizeModuleCapability is the one place a capability decision is made.
//
// Every capability path calls this — the full inventory, enumerated and enforced
// by TestEveryModuleCapabilityPathReReadsLiveAuthority. That test exists because
// the alternative is a convention: 25 call sites that each remember to re-read,
// and a 26th added next month that does not. A coverage gate is the only thing
// that makes "every path" true rather than aspirational.
//
// The declared grant is resolved FIRST and the live read second. That order is
// deliberate: an unknown principal must be refused without a database round
// trip, both because it is cheaper and because an unauthenticated caller must
// not be able to make this host do work.
func (s *Service) AuthorizeModuleCapability(
	ctx context.Context, caller ModuleCaller,
) (*ModuleCapabilityAuthority, error) {
	grant, err := s.moduleGrant(caller)
	if err != nil {
		return nil, err
	}
	if s.moduleAuthority == nil {
		// Fail closed. A host with no live authority read cannot honour a
		// narrowing, so it must not authorize capabilities at all rather than
		// authorize them on the declared ceiling alone — which is the state this
		// file exists to end.
		return nil, status.Error(codes.FailedPrecondition,
			"this host cannot re-read live module authority, so no capability may be exercised")
	}

	live, err := s.moduleAuthority.LiveModuleAuthority(ctx, caller.PrincipalID, caller.BoundOrg)
	if err != nil {
		if errors.Is(err, ErrModuleInstallationInactive) {
			return nil, wrapStatus(codes.PermissionDenied, ErrModuleInstallationInactive,
				"principal %s in organization %s", caller.PrincipalID, caller.BoundOrg)
		}
		// The live read failed. NOT a denial and NOT an allow: the host does not
		// know whether this capability is still authorized, so it answers
		// Unavailable and the caller retries. Treating it as a denial would make
		// a database blip look like mass revocation; as an allow, it would be
		// the check not running.
		return nil, status.Errorf(codes.Unavailable, "cannot re-read live module authority: %v", err)
	}

	authority := &ModuleCapabilityAuthority{
		Caller:               caller,
		Grant:                grant,
		InstallationID:       live.InstallationID,
		InstallationRevision: live.InstallationRevision,
		ProducerEpoch:        live.ProducerEpoch,
	}
	if err := authority.checkAgainstSealed(caller.Sealed); err != nil {
		return nil, err
	}
	return authority, nil
}

// checkAgainstSealed is the intersection: the live facts must still match what
// the credential was minted against.
//
// An ABSENT sealed value is not a match and not a mismatch — it is a credential
// that carries no claim about that term, which is the state every credential is
// in until the Work Context cutover. Those are skipped rather than defaulted to
// zero, and the difference matters: zero would compare equal to a real
// installation revision of 0 and would therefore PASS for exactly the
// credentials that claim nothing.
//
// That asymmetry is the one trap in this function. "Treat missing as zero" is
// the shorter implementation and it silently admits every unsealed credential
// while looking like a check.
func (a *ModuleCapabilityAuthority) checkAgainstSealed(sealed *SealedModuleAuthority) error {
	if sealed == nil {
		return nil
	}
	if sealed.InstallationID != nil && *sealed.InstallationID != a.InstallationID {
		return wrapStatus(codes.Unauthenticated, ErrModuleAuthorityStale,
			"sealed for installation %s, live installation is %s",
			*sealed.InstallationID, a.InstallationID)
	}
	if sealed.InstallationRevision != nil && *sealed.InstallationRevision != a.InstallationRevision {
		return wrapStatus(codes.Unauthenticated, ErrModuleAuthorityStale,
			"sealed at installation revision %d, live revision is %d",
			*sealed.InstallationRevision, a.InstallationRevision)
	}
	if sealed.ProducerEpoch != nil && *sealed.ProducerEpoch != a.ProducerEpoch {
		return wrapStatus(codes.Unauthenticated, ErrModuleAuthorityStale,
			"sealed at producer epoch %d, live epoch is %d",
			*sealed.ProducerEpoch, a.ProducerEpoch)
	}
	return nil
}

// SealedModuleAuthority is what a credential claimed at mint.
//
// Every field is a POINTER, and that is the whole design of this type. A
// credential that carries no installation revision is making no claim about one,
// and a value type could not express that: zero would be indistinguishable from
// "revision 0", which is a real revision a credential could legitimately have
// been sealed at. Core's own seal made the same change for the same reason —
// `build_incarnation` and `image_digest` became optional precisely so "bears no
// execution" and "bears execution zero" stopped being the same bytes.
//
// So: ask about presence, never compare zero.
type SealedModuleAuthority struct {
	InstallationID       *string
	InstallationRevision *uint64
	ProducerEpoch        *uint64
	// BindingID and BindingRevision are set for an operation context only.
	// Verification against them is an EXACT lookup of that binding — never a
	// search of the principal's bindings for one containing the presented
	// scopes, which is what this host did and what must go.
	BindingID       *string
	BindingRevision *uint64
}

// ExactOperationBinding resolves the one binding a credential named.
//
// The name says what it is NOT: a search. `operationScopesSubset` walked the
// principal's bindings looking for one whose scopes contained the presented set,
// which means a credential naming a narrow binding was satisfied by any WIDER
// binding the principal also held — so narrowing one binding achieved nothing
// while a broader one survived.
//
// An absent sealed binding id is refused rather than falling back to a search.
// The fallback is the whole defect: it is what makes the exact lookup optional,
// and an optional exact lookup is a search with extra steps.
func (s *Service) ExactOperationBinding(
	ctx context.Context, authority *ModuleCapabilityAuthority, sealed *SealedModuleAuthority,
) (*ModuleOperationBinding, error) {
	if sealed == nil || sealed.BindingID == nil || *sealed.BindingID == "" {
		return nil, status.Error(codes.Unauthenticated,
			"this capability names no operation binding, and a binding is resolved by exact lookup rather than by searching for one with compatible scopes")
	}
	if s.moduleOperationBindings == nil {
		return nil, status.Error(codes.FailedPrecondition,
			"this host cannot resolve an operation binding, so no operation capability may be exercised")
	}
	binding, err := s.moduleOperationBindings.OperationBindingByID(
		ctx, authority.Caller.PrincipalID, *sealed.BindingID)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "cannot resolve operation binding: %v", err)
	}
	if binding == nil {
		return nil, status.Errorf(codes.PermissionDenied,
			"operation binding %s is not held by principal %s", *sealed.BindingID, authority.Caller.PrincipalID)
	}
	if sealed.BindingRevision != nil && *sealed.BindingRevision != binding.Revision {
		return nil, wrapStatus(codes.Unauthenticated, ErrModuleAuthorityStale,
			"sealed at binding revision %d, live revision is %d",
			*sealed.BindingRevision, binding.Revision)
	}
	return binding, nil
}

// ModuleOperationBinding is one binding as the exact lookup returns it.
type ModuleOperationBinding struct {
	BindingID string
	Revision  uint64
	Audience  string
	Scopes    []string
}

// ModuleOperationBindingStore is the exact-lookup surface.
//
// By ID and principal, with no listing method — deliberately. A `List` here
// would be the search this replaces, available to whoever reached for it next.
type ModuleOperationBindingStore interface {
	OperationBindingByID(ctx context.Context, principalID, bindingID string) (*ModuleOperationBinding, error)
}

// DeferredWorkAuthority is what a deferred producer records so the work it
// enqueues can be re-checked when it RUNS.
//
// Deferred work is the case the live re-read alone cannot cover: the authority
// was current when the job was enqueued and the job runs later, so "re-read at
// use" has to mean at the moment of USE and not at the moment of enqueue. The
// producer therefore stamps what it was authorized under, and the worker refuses
// work whose stamp no longer matches.
type DeferredWorkAuthority struct {
	PrincipalID          string
	OrganizationID       string
	InstallationID       string
	InstallationRevision uint64
	ProducerEpoch        uint64
}

// StampDeferredWork captures the authority a producer acted under.
func (a *ModuleCapabilityAuthority) StampDeferredWork() DeferredWorkAuthority {
	return DeferredWorkAuthority{
		PrincipalID:          a.Caller.PrincipalID,
		OrganizationID:       a.Caller.BoundOrg,
		InstallationID:       a.InstallationID,
		InstallationRevision: a.InstallationRevision,
		ProducerEpoch:        a.ProducerEpoch,
	}
}

// ErrDeferredWorkRevoked reports that work was enqueued under authority that has
// since moved.
//
// Its TERMINAL OUTCOME is defined here rather than left to each worker, because
// the alternative is that each one guesses. Work whose authority was revoked is
// not retried and not silently dropped: it is failed with this error, which a
// worker records as a terminal state. Retrying would mean asking the same
// question forever, and dropping it would lose the evidence that something was
// enqueued under authority that no longer exists.
var ErrDeferredWorkRevoked = errors.New("deferred work was enqueued under authority that has since been revoked")

// AuthorizeDeferredWork re-checks a stamp when the work runs.
//
// Already-leased work gets its outcome from here: if the authority has moved, the
// work fails terminally rather than being retried, because the authority will
// not come back by waiting.
func (s *Service) AuthorizeDeferredWork(
	ctx context.Context, stamp DeferredWorkAuthority,
) (*ModuleCapabilityAuthority, error) {
	sealed := &SealedModuleAuthority{
		InstallationID:       &stamp.InstallationID,
		InstallationRevision: &stamp.InstallationRevision,
		ProducerEpoch:        &stamp.ProducerEpoch,
	}
	authority, err := s.AuthorizeModuleCapability(ctx, ModuleCaller{
		PrincipalID: stamp.PrincipalID,
		BoundOrg:    stamp.OrganizationID,
		Sealed:      sealed,
	})
	if err != nil {
		if errors.Is(err, ErrModuleAuthorityStale) ||
			status.Code(err) == codes.PermissionDenied ||
			status.Code(err) == codes.Unauthenticated {
			return nil, fmt.Errorf("%w: %w", ErrDeferredWorkRevoked, err)
		}
		// Unavailable: the host could not check. The work is retryable, because
		// nothing was decided.
		return nil, err
	}
	return authority, nil
}
