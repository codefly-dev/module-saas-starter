// Package compositiontest stands in for the live module authority of a
// composition that installs every composed module at every organisation: what
// the authority's mini gateway holds implicitly, and what the host-interface
// conformance harness drives accounts against. It is a fake of that SOURCE for
// a harness, never a capability path: production reads its installations from
// the seal store, and this package is imported by tests alone.
package compositiontest

import (
	"context"
	"errors"
	"fmt"

	"accounts/pkg/business"
)

// ComposedModuleInstallations answers the live module authority for the
// interface's reading of composition: a composed module is installed at every
// organisation of the cell by composition, so a declared module principal has an
// active installation at any organisation, named deterministically, with no
// envelope and no narrowing (the contract decided on 2026-10-22,
// codefly-dev/authority, "Rules of change"). It reads the declaration through
// the service's own accessors, as a stand-in for it.
type ComposedModuleInstallations struct {
	service *business.Service
	seals   business.WorkContextSealStore
}

// NewComposedModuleInstallations reads the declared principals from the service
// and answers every other seal question from the wrapped store.
func NewComposedModuleInstallations(service *business.Service, seals business.WorkContextSealStore) *ComposedModuleInstallations {
	return &ComposedModuleInstallations{service: service, seals: seals}
}

// composedModuleOf answers which declared module an installation id names, if any.
func (c *ComposedModuleInstallations) composedModuleOf(orgID, installationID string) (string, bool) {
	for principalID := range c.service.ModulePrincipals() {
		if ComposedModuleInstallationID(orgID, principalID) == installationID {
			return principalID, true
		}
	}
	return "", false
}

// WorkContextHeldInstallation: whoever stands in the organisation (a member, or
// the live agent of one of its active installations) holds every composed
// module's installation, as they hold the organisation's own installations.
func (c *ComposedModuleInstallations) WorkContextHeldInstallation(ctx context.Context, orgID, principalID, installationID string) (uint64, error) {
	if _, composed := c.composedModuleOf(orgID, installationID); !composed {
		return c.seals.WorkContextHeldInstallation(ctx, orgID, principalID, installationID)
	}
	standing, ok := c.seals.(SealStandingReader)
	if !ok {
		return 0, errors.New("the seal store answers no standing, so no composed installation can be held")
	}
	stands, err := standing.WorkContextOrgStanding(ctx, orgID, principalID)
	if err != nil {
		return 0, err
	}
	if !stands {
		return 0, business.ErrWorkContextSealUnheld
	}
	return 1, nil
}

// SealStandingReader answers standing in the organisation on the seal reads'
// own snapshot, so a composed installation can be sealed with no tenant
// identity on the call.
type SealStandingReader interface {
	WorkContextOrgStanding(ctx context.Context, orgID, principalID string) (bool, error)
}

// WorkContextPrincipalEpoch is the wrapped store's answer.
func (c *ComposedModuleInstallations) WorkContextPrincipalEpoch(ctx context.Context, orgID, principalID string) (uint64, error) {
	return c.seals.WorkContextPrincipalEpoch(ctx, orgID, principalID)
}

// WorkContextOwnerRevision is the wrapped store's answer.
func (c *ComposedModuleInstallations) WorkContextOwnerRevision(ctx context.Context, orgID, ownerID string) (uint64, error) {
	return c.seals.WorkContextOwnerRevision(ctx, orgID, ownerID)
}

// ComposedModuleInstallationID is the deterministic id of a composed module's
// installation at one organisation, as the mini gateway spells it.
func ComposedModuleInstallationID(orgID, principalID string) string {
	return orgID + "/" + principalID
}

// BeginModuleAuthorityUse holds nothing: the composition does not move during a mint.
func (c *ComposedModuleInstallations) BeginModuleAuthorityUse(ctx context.Context) (context.Context, func(), error) {
	return ctx, func() {}, nil
}

// LiveModuleAuthority is active for every declared principal at every organisation.
func (c *ComposedModuleInstallations) LiveModuleAuthority(_ context.Context, principalID, orgID, installationID string) (*business.LiveModuleAuthority, error) {
	if !c.service.IsDeclaredModulePrincipal(principalID) {
		return nil, fmt.Errorf("%w: %s is not a composed module", business.ErrModuleInstallationInactive, principalID)
	}
	id := ComposedModuleInstallationID(orgID, principalID)
	if installationID != "" && installationID != id {
		return nil, fmt.Errorf("%w: %s holds %s, not %s", business.ErrModuleInstallationInactive, principalID, id, installationID)
	}
	// The manifest is the ceiling: a composed installation's scope is the
	// declared grant itself, narrowed by nothing.
	grant := c.service.ModulePrincipals()[principalID]
	return &business.LiveModuleAuthority{
		RegistryBindingID: grant.PresenceBinding, InstallationID: id, InstallationRevision: 1, ProducerEpoch: 1,
		Scope: business.InstallationScope{
			Capabilities: true, Follows: true, Queues: grant.Queues, Namespaces: grant.Namespaces,
			ExternalNamespaces: grant.ExternalNamespaces, Resources: grant.Resources,
			ReadAudiences: grant.ReadAudiences, OperationAudiences: grant.OperationAudiences, ArtifactPolicies: grant.ArtifactPolicies,
		},
	}, nil
}

// WorkContextSolutionInstallation is the wrapped store's answer: a solution is
// installed explicitly, never by composition.
func (c *ComposedModuleInstallations) WorkContextSolutionInstallation(ctx context.Context, orgID, bindingID string) (string, error) {
	return c.seals.WorkContextSolutionInstallation(ctx, orgID, bindingID)
}
