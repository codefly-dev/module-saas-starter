package business

import (
	"context"
	"errors"
	"fmt"
)

// ComposedModuleInstallations answers the live module authority for the
// interface's reading of composition: a composed module is installed at every
// organisation of the cell by composition, so a declared module principal has an
// active installation at any organisation, named deterministically, with no
// envelope and no narrowing. It is what the mini gateway holds implicitly and
// what the contract decided on 2026-10-22 (codefly-dev/authority, "Rules of
// change"). Additive: production keeps its reads until the registry derives
// these rows itself; the rung 1 harness wires this one.
type ComposedModuleInstallations struct {
	service *Service
	seals   WorkContextSealStore
}

// NewComposedModuleInstallations reads the declared principals from the service
// and answers every other seal question from the wrapped store.
func NewComposedModuleInstallations(service *Service, seals WorkContextSealStore) *ComposedModuleInstallations {
	return &ComposedModuleInstallations{service: service, seals: seals}
}

// composedModuleOf answers which declared module an installation id names, if any.
func (c *ComposedModuleInstallations) composedModuleOf(orgID, installationID string) (string, bool) {
	for principalID := range c.service.declaredModules() {
		if ComposedModuleInstallationID(orgID, principalID) == installationID {
			return principalID, true
		}
	}
	return "", false
}

// WorkContextHeldInstallation: a member of the organisation holds every composed
// module's installation, as they hold the organisation's own installations.
func (c *ComposedModuleInstallations) WorkContextHeldInstallation(ctx context.Context, orgID, principalID, installationID string) (uint64, error) {
	if _, composed := c.composedModuleOf(orgID, installationID); !composed {
		return c.seals.WorkContextHeldInstallation(ctx, orgID, principalID, installationID)
	}
	members, ok := c.seals.(SealMembershipReader)
	if !ok {
		return 0, errors.New("the seal store answers no membership, so no composed installation can be held")
	}
	member, err := members.WorkContextOrgMember(ctx, orgID, principalID)
	if err != nil {
		return 0, err
	}
	if !member {
		return 0, ErrWorkContextSealUnheld
	}
	return 1, nil
}

// SealMembershipReader answers membership on the seal reads' own snapshot, so a
// composed installation can be sealed with no tenant identity on the call.
type SealMembershipReader interface {
	WorkContextOrgMember(ctx context.Context, orgID, principalID string) (bool, error)
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
func (c *ComposedModuleInstallations) LiveModuleAuthority(_ context.Context, principalID, orgID, installationID string) (*LiveModuleAuthority, error) {
	if !c.service.isDeclaredModulePrincipal(principalID) {
		return nil, fmt.Errorf("%w: %s is not a composed module", ErrModuleInstallationInactive, principalID)
	}
	id := ComposedModuleInstallationID(orgID, principalID)
	if installationID != "" && installationID != id {
		return nil, fmt.Errorf("%w: %s holds %s, not %s", ErrModuleInstallationInactive, principalID, id, installationID)
	}
	// The manifest is the ceiling: a composed installation's scope is the
	// declared grant itself, narrowed by nothing.
	grant := c.service.declaredModules()[principalID]
	return &LiveModuleAuthority{
		RegistryBindingID: grant.PresenceBinding, InstallationID: id, InstallationRevision: 1, ProducerEpoch: 1,
		Scope: InstallationScope{
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
