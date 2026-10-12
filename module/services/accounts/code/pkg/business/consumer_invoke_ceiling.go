package business

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"slices"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// THE CEILING OF A CREDENTIAL DELIVERED TO A THIRD PARTY (issue #1083).
//
// A resource-confined credential is held by a client a person authorized for ONE
// installed consumer, and a capability minted with it may act for no more than
// that consumer DECLARES it consumes, as this organization consented to it.
//
// The record read is the consumer's registry grant: the principal its delivered
// authority document is issued to, derived from the signed document, the applied
// presence it is granted over and the envelope. Its operation audiences name
// each module the consumer calls — the audience a capability for that module is
// addressed to — and the scopes of every operation it redeems there. The ceiling
// is their INVOKE half, because invoke is the operation that carries a person's
// own authority, attenuated, which is what a confined mint is.
//
// WHAT REACHES THAT RECORD TODAY. The renderer derives the authority document
// from a module's published contract (module.contract.codefly.yaml) and from
// nothing else. A solution that declares its consumption only in
// solution.codefly.yaml (`api.consumes`) has no authority document, so no
// registry grant, so this ceiling holds nothing for it and every confined mint
// it receives is refused. Rendering a solution's declared consumption into its
// authority document is the delivery half of #1083 and belongs to the renderer;
// this file is the host half and reads only what delivery produced.
//
// What is read is the grant AS THIS ORGANIZATION CONSENTED TO IT: the registry
// entry for the installation's delivered principal, intersected with the
// installation's scope, through the same decision every capability of that
// consumer goes through. Consent is fixed when the organization installs: a
// module the consumer starts declaring afterwards is outside the consent until
// the organization installs again, and the refusal says exactly that rather than
// calling a declared module undeclared.
//
// The installation's `allowed_audiences` / `allowed_scopes` are NOT read here.
// They are an administrator's lists and may only narrow what this returns; the
// caller applies them after.

// ConsumerCeiling is one consumer's ceiling as decided for one mint.
type ConsumerCeiling struct {
	// Consented is what the installation consents to: the declared invoke
	// bindings intersected with the installation's scope. It is the ceiling.
	Consented map[string]ModuleOperationAudience
	// Declared is what the consumer's delivered authority declares now, before
	// the installation's consent. Read only to say WHY a mint outside Consented
	// is refused; it never admits anything.
	Declared map[string]ModuleOperationAudience
	// Own are the audiences that name the consumer itself.
	Own []string

	principalID    string
	binding        string
	orgID          string
	installationID string
}

// ConsumerCeilingVerdict is the answer for one (audience, scopes).
type ConsumerCeilingVerdict int

const (
	// ConsumerCeilingAdmitted: inside one consented binding.
	ConsumerCeilingAdmitted ConsumerCeilingVerdict = iota
	// ConsumerCeilingOwnAudience: the audience is the consumer itself.
	ConsumerCeilingOwnAudience
	// ConsumerCeilingUndeclared: the consumer declares no binding at the audience.
	ConsumerCeilingUndeclared
	// ConsumerCeilingOutsideDeclared: declared at the audience, but the scopes
	// fit no binding the consumer declares there.
	ConsumerCeilingOutsideDeclared
	// ConsumerCeilingNotConsented: inside what the consumer declares now, and
	// outside what this installation consented to when it was made.
	ConsumerCeilingNotConsented
)

// Admit decides one capability against the ceiling.
//
// A CONFINED MINT IS NEVER ADDRESSED TO THE CONSUMER ITSELF. The credential is
// already confined to that consumer and reaches its own routes directly, so a
// capability for its own audience would add nothing it does not hold. It is
// refused by name, and BEFORE any binding is consulted: a binding may name its
// own module when it is self-admitting (headless or delegation work, see
// selfAdmittingBinding), and such a binding carries invoke scopes. Its purpose
// is unattended work with nobody present, never a person's credential held by a
// third party, so it must not admit a confined mint — and checking the
// audience first is what keeps it from doing so.
func (c ConsumerCeiling) Admit(audience string, scopes []ModuleOperationScope) ConsumerCeilingVerdict {
	if slices.Contains(c.Own, audience) {
		return ConsumerCeilingOwnAudience
	}
	if _, inside := ConsumerCeilingAdmits(c.Consented, audience, scopes); inside {
		return ConsumerCeilingAdmitted
	}
	declared, inside := ConsumerCeilingAdmits(c.Declared, audience, scopes)
	switch {
	case inside:
		return ConsumerCeilingNotConsented
	case declared:
		return ConsumerCeilingOutsideDeclared
	}
	return ConsumerCeilingUndeclared
}

// ConsumerInvokeCeiling resolves the ceiling of the consumer installed as
// installationID in orgID.
//
// It fails closed. An installation that names no delivered principal — one made
// before installations recorded the authority they consent to — declares
// nothing, and is refused by name rather than read as declaring everything. So
// is one whose consent withdrew the consumer's capabilities.
func (s *Service) ConsumerInvokeCeiling(ctx context.Context, orgID, installationID string) (*ConsumerCeiling, error) {
	if s == nil || s.store == nil {
		return nil, status.Error(codes.FailedPrecondition,
			"this host cannot read what an installed consumer declares, so there is no ceiling to hold its credential to")
	}
	store, ok := s.store.(InstallationScopeStore)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition,
			"this host cannot read an installation's delivered authority, so there is no ceiling to hold its credential to")
	}
	// The decision is only as current as the locks behind it. A caller that
	// goes on to mint opens the use span first (see StartTask), so this one is
	// the same span and its locks are held through the signature; opened here
	// alone, it covers the read.
	ctx, releaseAuthority, fenceErr := s.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return nil, fenceErr
	}
	defer releaseAuthority()

	var state *InstallationAuthorityState
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		state, err = store.InstallationAuthorityState(ctx, orgID, installationID)
		return err
	}); err != nil {
		var storeErr *StoreError
		if errors.As(err, &storeErr) && storeErr.StoreErrorType == ErrTypeNotFound {
			// The perimeter admitted under this installation a moment ago, so
			// gone now means withdrawn in between: nothing is consented to.
			return nil, status.Error(codes.PermissionDenied,
				"the installation this credential was admitted under is no longer active")
		}
		return nil, status.Error(codes.Unavailable, "cannot read the installed consumer's delivered authority")
	}
	if state.PrincipalID == "" {
		return nil, status.Error(codes.FailedPrecondition,
			"the installation this credential was admitted under records no delivered authority, so its consumer declares nothing a credential may act for; "+
				"install the consumer again, which records the authority its delivered contract declares")
	}
	if !state.Scope.Capabilities {
		// Named, because the live read below reports it as an inactive
		// installation, and this installation is active: its consent withdrew
		// the consumer's capabilities, which consent can only narrow.
		return nil, status.Error(codes.PermissionDenied,
			"the installation this credential was admitted under withdrew its consumer's capabilities, so a credential delivered to it may act for nothing; "+
				"an administrator restores them by installing the consumer again")
	}
	authority, snapshot, err := s.authorizeModuleCapability(ctx, ModuleCaller{
		PrincipalID: state.PrincipalID,
		BoundOrg:    orgID,
		// The installation the perimeter named, and no other: the consumer's
		// principal could in principle be installed again under a new id, and
		// a ceiling read from that one would be consent nobody gave this
		// credential.
		Sealed: &SealedModuleAuthority{InstallationID: &installationID},
	})
	if err != nil {
		return nil, err
	}
	// The declaration the decision was made against, from the SAME registry
	// snapshot: never a second read that could disagree with it.
	declared := snapshot.registry[state.PrincipalID]
	ceiling := &ConsumerCeiling{
		Consented:      authority.Grant.OperationAudiences,
		Declared:       declared.OperationAudiences,
		principalID:    state.PrincipalID,
		binding:        declared.PresenceBinding,
		orgID:          orgID,
		installationID: installationID,
	}
	if declared.Prefix != "" {
		ceiling.Own = append(ceiling.Own, declared.Prefix)
	}
	if declared.PresenceBinding != "" {
		ceiling.Own = append(ceiling.Own, SolutionAudience(declared.PresenceBinding))
	}
	return ceiling, nil
}

// ConsumerCeilingStillCurrent refuses a capability whose consumer's ceiling
// changed after it was decided.
//
// The use span holds the installation, its target and the envelope revision; it
// does not hold the registry, which the reconciler replaces in memory when a new
// authority generation is applied. So a binding withdrawn between the decision
// and the signature would otherwise still be signed. A mint calls this AFTER
// signing and BEFORE the capability leaves the process, and it DECIDES AGAIN —
// the same live authority read, sealed to the same installation, in the same
// span — then requires the consumer's declaration and this installation's
// consent to be what they were. A withdrawal published before this refuses the
// capability; one published after it lands after issuance, which is the
// ordinary bound every capability has: its lifetime.
//
// It compares the declaration and the consent, not the registry's identity:
// the reconciler republishes on every pass, and a pass that changed nothing
// about this consumer must not refuse its mint.
func (s *Service) ConsumerCeilingStillCurrent(ctx context.Context, c *ConsumerCeiling) error {
	if c == nil {
		return nil
	}
	ctx, releaseAuthority, fenceErr := s.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return fenceErr
	}
	defer releaseAuthority()

	changed := status.Error(codes.Aborted,
		"the consumer this credential was delivered to changed what it declares while this capability was being minted, so it was not issued; ask again")
	authority, snapshot, err := s.authorizeModuleCapability(ctx, ModuleCaller{
		PrincipalID: c.principalID,
		BoundOrg:    c.orgID,
		Sealed:      &SealedModuleAuthority{InstallationID: &c.installationID},
	})
	if err != nil {
		if code := status.Code(err); code == codes.PermissionDenied || code == codes.Unauthenticated {
			return changed
		}
		return err
	}
	current := snapshot.registry[c.principalID]
	if current.PresenceBinding != c.binding ||
		!reflect.DeepEqual(current.OperationAudiences, c.Declared) ||
		!reflect.DeepEqual(authority.Grant.OperationAudiences, c.Consented) {
		return changed
	}
	return nil
}

// ConsumerCeilingAdmits reports whether a capability addressed to audience and
// carrying scopes lies inside a set of bindings: declared says the set names
// audience at all, and inside says the scopes fit ONE binding there.
//
// One binding, not their union: a capability is minted under a declaration, and
// two bindings for one module that each grant half of a request do not together
// declare the whole of it.
func ConsumerCeilingAdmits(bindings map[string]ModuleOperationAudience, audience string, scopes []ModuleOperationScope) (declared, inside bool) {
	for _, id := range slices.Sorted(maps.Keys(bindings)) {
		binding := bindings[id]
		if audience == "" || binding.Audience != audience || len(binding.InvokeScopes) == 0 {
			continue
		}
		declared = true
		if operationScopesSubset(scopes, binding.InvokeScopes) {
			return true, true
		}
	}
	return declared, false
}
