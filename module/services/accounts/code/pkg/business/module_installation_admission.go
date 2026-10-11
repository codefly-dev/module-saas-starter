package business

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A module capability NAMES the installation it acts under, and this is where
// the host decides whether it may.
//
// THE MODEL, and why the alternative had to be refused. Core's mint requires an
// installation id — "a capability is sealed to an installation, so one must be
// named", because authority is held THROUGH an installation and a capability
// naming none is bound to nothing. The host had nothing to pass: a module
// principal's declared grant carries a prefix, queues, namespaces, resources and
// audiences, and nothing installation-shaped, because an installation is an
// organisation's consent to a solution TARGET and the module principal is not a
// party to it.
//
// The resolution is not to invent one. A synthetic installation, or the first
// active installation in the tenant, would fill the seal from the host's own view
// of state — and then every downstream check compares a value to itself, which is
// the tautology that passes for every caller. So the REQUEST carries it:
//
//   - a viewer-driven call names the viewer's installation of the solution;
//   - a module-to-module call carries forward the installation its own
//     capability was sealed to, so authority narrows along the chain and never
//     hops to another organisation's consent.
//
// `ModulePrincipalGrant` stays a CEILING rather than becoming a directory. It
// says what this module may ever do; it does not say which installations exist,
// and a registry that answered that question would be a second source of truth
// for consent.
//
// WHAT MAKES THIS THE INSTALLATION-FRESHNESS TERM MADE REAL: once the seal names
// an installation, uninstalling revokes every capability sealed to it on its next
// use, because the live re-read resolves that exact installation and finds it
// inactive. Before this, nothing related the two and the term was unanswerable.

// ErrModuleInstallationNotNamed reports a capability request that names no
// installation.
//
// Refused rather than defaulted. A default would be the host choosing whose
// consent the module acts under, which is the one decision the request exists to
// carry.
var ErrModuleInstallationNotNamed = errors.New("a module capability must name the installation it acts under")

// ErrModuleNotComposedBySolution reports an installation whose solution never
// composed this module.
//
// This is the authorisation step people expect the registry to do and it cannot:
// the declared registry is a ceiling over what a module may do anywhere, while
// THIS says the organisation's consent covers this module at all. A module may be
// perfectly well declared and still have no business acting under a given
// installation.
var ErrModuleNotComposedBySolution = errors.New("the installed solution does not compose this module")

// ModuleInstallationAdmission is what the host established about a named
// installation, and what it will seal.
type ModuleInstallationAdmission struct {
	InstallationID       string
	InstallationRevision uint64
	ProducerEpoch        uint64
	// TargetID and BindingID are the solution target the installation consents
	// to and the presence binding that target records. Carried because the
	// refusal messages need them: "this module is not composed by that solution"
	// is only actionable if the operator can see which solution.
	TargetID  string
	BindingID string
}

// ModuleInstallationResolver resolves a named installation to the solution that
// consented to it, and to the modules that solution composed.
//
// One method, and one snapshot. Resolving the installation, then its target,
// then the target's applied document in three reads lets an uninstall or a new
// generation land between them and produce an admission describing a state that
// never existed.
type ModuleInstallationResolver interface {
	// ResolveModuleInstallation reads the installation, its live target, and the
	// module names the target's APPLIED presence document pins.
	//
	// The applied document rather than the desired one: a module may act under
	// what the host actually reconciled, never under what delivery merely
	// proposed. Returns ErrModuleInstallationInactive when the installation is
	// revoked, absent, or another organisation's.
	ResolveModuleInstallation(ctx context.Context, orgID, installationID string) (*ResolvedModuleInstallation, error)
}

// ResolvedModuleInstallation is one installation with the solution behind it.
type ResolvedModuleInstallation struct {
	InstallationID       string
	InstallationRevision uint64
	TargetID             string
	BindingID            string
	// ComposedModules are the module names the applied presence document pins.
	// Empty is meaningful: a solution that composed nothing admits no module,
	// and treating empty as "any" would make an undeclared solution the most
	// permissive one.
	ComposedModules []string
}

// AdmitModuleUnderInstallation decides whether this module may act under the
// installation its request names.
//
// The order is cheapest-and-most-terminal first, so a caller reads the real
// cause: an unnamed installation is refused before any database work; an unknown
// principal before the installation is resolved; and composition is checked
// before the epoch, because "your solution does not include this module" is a
// permanent answer while a stale epoch is a re-mint.
func (s *Service) AdmitModuleUnderInstallation(
	ctx context.Context, caller ModuleCaller, installationID string,
) (*ModuleInstallationAdmission, error) {
	if installationID == "" {
		return nil, wrapStatus(codes.InvalidArgument, ErrModuleInstallationNotNamed,
			"principal %s", caller.PrincipalID)
	}
	grant, err := s.moduleGrant(caller)
	if err != nil {
		return nil, err
	}
	if s.moduleInstallations == nil {
		return nil, status.Error(codes.FailedPrecondition,
			"this host cannot resolve a module installation, so no module capability may be minted")
	}

	resolved, err := s.moduleInstallations.ResolveModuleInstallation(ctx, caller.BoundOrg, installationID)
	if err != nil {
		if errors.Is(err, ErrModuleInstallationInactive) {
			return nil, wrapStatus(codes.PermissionDenied, ErrModuleInstallationInactive,
				"installation %s in organization %s", installationID, caller.BoundOrg)
		}
		return nil, status.Errorf(codes.Unavailable, "cannot resolve module installation: %v", err)
	}

	// The solution must actually compose this module. The module's declared
	// prefix is the name a composition pins it under, which is what makes this
	// comparable at all.
	if !composesModule(resolved.ComposedModules, grant.Prefix) {
		return nil, wrapStatus(codes.PermissionDenied, ErrModuleNotComposedBySolution,
			"module %q, installation %s, solution binding %s",
			grant.Prefix, installationID, resolved.BindingID)
	}

	live, err := s.liveAuthorityFor(ctx, caller, installationID)
	if err != nil {
		return nil, err
	}

	return &ModuleInstallationAdmission{
		InstallationID:       resolved.InstallationID,
		InstallationRevision: resolved.InstallationRevision,
		ProducerEpoch:        live.ProducerEpoch,
		TargetID:             resolved.TargetID,
		BindingID:            resolved.BindingID,
	}, nil
}

// liveAuthorityFor reads the producer epoch for this caller under this
// installation.
//
// Separate from the resolve so the two refusals stay attributable: "your
// solution does not compose this module" and "your authority has moved" are
// different facts with different remedies.
func (s *Service) liveAuthorityFor(
	ctx context.Context, caller ModuleCaller, installationID string,
) (*LiveModuleAuthority, error) {
	if s.moduleAuthority == nil {
		return nil, status.Error(codes.FailedPrecondition,
			"this host cannot re-read live module authority, so no capability may be minted")
	}
	live, err := s.moduleAuthority.LiveModuleAuthority(
		ctx, caller.PrincipalID, caller.BoundOrg, installationID)
	if err != nil {
		if errors.Is(err, ErrModuleInstallationInactive) {
			return nil, wrapStatus(codes.PermissionDenied, ErrModuleInstallationInactive,
				"installation %s", installationID)
		}
		if errors.Is(err, ErrModuleAuthorityUnreadable) {
			return nil, wrapStatus(codes.InvalidArgument, ErrModuleAuthorityUnreadable,
				"principal %s", caller.PrincipalID)
		}
		return nil, status.Errorf(codes.Unavailable, "cannot re-read live module authority: %v", err)
	}
	return live, nil
}

// composesModule reports whether a solution's pinned modules include this one.
//
// An exact match on the module name. Not a prefix or substring test: `crm` must
// not be admitted by a solution composing `crm-reporting`, and a substring check
// is the kind of thing that reads as lenient and is actually a privilege
// escalation between two real modules.
func composesModule(composed []string, prefix string) bool {
	if prefix == "" {
		// A module with no declared prefix cannot be matched against anything,
		// and admitting it would mean admitting every solution.
		return false
	}
	for _, module := range composed {
		if module == prefix {
			return true
		}
	}
	return false
}

// SealFor renders what core's mint needs from an admission.
//
// Returned as the pointer-valued SealedModuleAuthority the verify side already
// compares, so the mint and the check cannot disagree about which terms are
// present: the same type carries them in both directions.
func (a *ModuleInstallationAdmission) SealFor() *SealedModuleAuthority {
	installation, revision, epoch := a.InstallationID, a.InstallationRevision, a.ProducerEpoch
	return &SealedModuleAuthority{
		InstallationID:       &installation,
		InstallationRevision: &revision,
		ProducerEpoch:        &epoch,
	}
}

// SetModuleInstallations wires the installation resolution.
//
// Unset means AdmitModuleUnderInstallation refuses, which is fail-closed: a host
// that cannot establish whose consent a module acts under must not mint a
// capability claiming it did.
func (s *Service) SetModuleInstallations(store ModuleInstallationResolver) {
	s.moduleInstallations = store
}
