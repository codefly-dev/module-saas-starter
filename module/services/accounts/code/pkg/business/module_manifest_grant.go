package business

import (
	"context"
	"fmt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"sort"
	"strings"

	modulecontract "github.com/codefly-dev/core/contracts/module"
	"github.com/codefly-dev/core/solutionhost"
)

// manifestCeiling grants only what this signed manifest declares. Tenancy is
// resolved by installation consent; the manifest cannot grant cross-tenant use,
// resource-instance rights, artifact policies or installed delegation modes.
func manifestCeiling(request *solutionhost.Authority) ModuleEnvelopePrincipal {
	grant := ModulePrincipalGrant{}
	if request == nil {
		return ModuleEnvelopePrincipal{Grant: grant}
	}
	grant.Queues = append([]string(nil), request.Queues...)
	grant.Namespaces = append([]string(nil), request.Namespaces...)
	grant.Resources = append([]string(nil), request.ResourceKinds...)
	for _, binding := range request.Bindings {
		audience := ModuleOperationAudience{
			Audience:       binding.Audience,
			InvokeScopes:   scopesOf(binding.Operations[modulecontract.OperationInvoke]),
			LookupScopes:   scopesOf(binding.Operations[modulecontract.OperationLookup]),
			HeadlessScopes: nilIfEmpty(scopesOf(binding.Operations[modulecontract.OperationHeadless])),
		}
		// A source connection can select a single declared binding. Several
		// bindings do not identify which one accepts that delegation.
		if len(request.Bindings) == 1 {
			audience.SourceDelegationScopes = nilIfEmpty(audience.InvokeScopes)
		}
		if grant.OperationAudiences == nil {
			grant.OperationAudiences = map[string]ModuleOperationAudience{}
		}
		grant.OperationAudiences[binding.ID] = audience
	}
	return ModuleEnvelopePrincipal{Grant: grant}
}

// scopesOf groups "kind:action" unit scopes by kind, actions sorted, no
// resource ids: a manifest names kinds, never instances.
func scopesOf(units []string) []ModuleOperationScope {
	byKind := map[string]map[string]bool{}
	for _, unit := range units {
		kind, action, ok := strings.Cut(unit, ":")
		if !ok || kind == "" || action == "" {
			continue
		}
		if byKind[kind] == nil {
			byKind[kind] = map[string]bool{}
		}
		byKind[kind][action] = true
	}
	kinds := make([]string, 0, len(byKind))
	for kind := range byKind {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	scopes := make([]ModuleOperationScope, 0, len(kinds))
	for _, kind := range kinds {
		actions := make([]string, 0, len(byKind[kind]))
		for action := range byKind[kind] {
			actions = append(actions, action)
		}
		sort.Strings(actions)
		scopes = append(scopes, ModuleOperationScope{ResourceKind: kind, Actions: actions})
	}
	return scopes
}

// A different replica may apply a narrowing before this replica reconciles.
// Judge the captured registry against committed generations on every decision,
// including the final mint fence. An older grant never borrows a newer revision.
func (s *Service) fenceManifestGenerations(ctx context.Context, snapshot *installedModuleRegistry) error {
	err := s.store.WithControlPlaneReadSnapshot(ctx, func(ctx context.Context) error {
		records, err := s.store.ListSolutionHostBindings(ctx)
		if err != nil {
			return err
		}
		live := map[string]uint64{}
		for _, record := range records {
			if record.Applied != nil && !record.Applied.Removed {
				live[record.BindingID] = record.Applied.Generation
			}
		}
		for _, grant := range snapshot.registry {
			if live[grant.PresenceBinding] != grant.PresenceGeneration || grant.PresenceGeneration == 0 {
				return fmt.Errorf("binding %s: registry generation %d differs from applied generation %d", grant.PresenceBinding, grant.PresenceGeneration, live[grant.PresenceBinding])
			}
		}
		return nil
	})
	if err != nil {
		s.requestRegistryPass()
		return status.Errorf(codes.Unavailable, "cannot use the module registry: %v", err)
	}
	return nil
}
