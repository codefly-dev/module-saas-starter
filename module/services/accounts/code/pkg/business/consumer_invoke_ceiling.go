package business

import (
	"context"
	"errors"
	"maps"
	"slices"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// THE CEILING OF A CREDENTIAL DELIVERED TO A THIRD PARTY (issue #1083).
//
// A resource-confined credential is held by a client a person authorized for ONE
// installed consumer, and a capability minted with it may act for no more than
// that consumer DECLARES it consumes. The declaration is not a list an
// administrator types: it is the consumer's own published contract
// (module.contract.codefly.yaml), whose outward bindings name each module it
// calls — the audience a capability for that module is addressed to — and the
// scope ceiling of every operation it redeems there. The render derives the
// authority document from it, the delivery pipeline signs it, and this host
// derives the registry from the signed document, the applied presence it is
// granted over, and the envelope. So a consumer's registry grant already holds
// the consumption in exactly the terms a mint is checked in — an audience and
// the scopes under it — and holds it structurally, re-verified on every pass.
//
// The ceiling is the INVOKE half of that grant, because invoke is the operation
// that carries a person's own authority, attenuated, which is what a confined
// mint is. Lookup and headless are other operations and never widen it.
//
// What is read is the grant AS THIS ORGANIZATION CONSENTED TO IT: the registry
// entry for the installation's delivered principal, intersected with the
// installation's scope, through AuthorizeModuleCapability — the same decision
// every capability of that consumer goes through. So an envelope that shrank, a
// narrowed installation, a withdrawn binding or an unreadable authority each
// narrows or refuses this ceiling exactly as it does the consumer's own work.
//
// The installation's `allowed_audiences` / `allowed_scopes` are NOT read here.
// They are an administrator's lists and may only narrow what this returns; the
// caller applies them after.

// ConsumerInvokeCeiling is what the consumer installed as installationID in
// orgID declares it may act for on a person's behalf: its invoke bindings, each
// naming one consumed module's audience and the scopes it asks for there.
//
// It fails closed. An installation that names no delivered principal — one made
// before installations recorded the authority they consent to — declares
// nothing, and is refused by name rather than read as declaring everything.
func (s *Service) ConsumerInvokeCeiling(ctx context.Context, orgID, installationID string) (map[string]ModuleOperationAudience, error) {
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
	authority, err := s.AuthorizeModuleCapability(ctx, ModuleCaller{
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
	return authority.Grant.OperationAudiences, nil
}

// ConsumerCeilingAdmits reports whether a capability addressed to audience and
// carrying scopes lies inside what the consumer declares: declared says the
// consumer names audience at all, and inside says the scopes fit ONE binding it
// declares for that audience.
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
