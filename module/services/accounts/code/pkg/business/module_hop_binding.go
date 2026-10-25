package business

import (
	"context"
	"errors"
)

// ErrModuleHopStale is a module hop of a Work Context whose authority no longer
// stands: its module is not installed in the organization any more, the binding
// the hop names is not one that installation declares, or the scopes the hop
// carries are outside that binding.
var ErrModuleHopStale = errors.New("the module authority this Work Context asserts is no longer current")

// ConfirmModuleHopBinding answers, for ONE module hop of a context, whether the
// authority it carries still stands — from the binding the hop NAMES.
//
// This is the one rule every module hop follows, and the reason it can be one:
// a hop names the binding it exercises, so a verifier does not have to guess
// which of several checks owns a context from the shape of its subjects. A
// delegation's hop names its delegation and is confirmed against it; every
// other hop names its module's own installed binding and is confirmed against
// that. Nothing is resolved by elimination.
//
// A module principal is not in the authority store — it is not a registered
// agent — so asking the store about it returns "not found", which a module
// reports to its caller as a revocation that never happened. That is what this
// replaces.
//
// No revision is compared here. The revision sealed into a context is the
// OWNER's, and the owner's own resolver compares it; a module hop carries no
// revision of its own. What makes the hop current is that its installation
// still declares the binding and still grants the scopes.
func (s *Service) ConfirmModuleHopBinding(ctx context.Context, orgID, principalID, bindingID string, scopes []ModuleOperationScope) error {
	ctx, releaseAuthority, fenceErr := s.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return fenceErr
	}
	defer releaseAuthority()

	if NamesInstalledBinding(bindingID) {
		identity, err := s.resolveInstalledBinding(ctx, orgID, bindingID)
		if err != nil {
			return err
		}
		if identity.PrincipalID != principalID {
			return ErrModuleHopStale
		}
		mode, id, ok := decodeInstalledBinding(bindingID)
		if !ok {
			return ErrModuleHopStale
		}
		_, binding, err := s.InstalledBinding(ctx, ModuleCaller{PrincipalID: principalID, BoundOrg: orgID}, id.Binding, mode)
		if err != nil {
			return err
		}
		if len(scopes) == 0 || !operationScopesSubset(scopes, binding.InvokeScopes) {
			return ErrModuleHopStale
		}
		return nil
	}
	if bindingID == "" {
		return ErrModuleHopStale
	}
	// The binding id this host seals on a module's hop through a binding
	// (<organisation>/<module>/<binding>, ModuleOperationBindingID). The hop
	// exercising it is confirmed against that binding as below; another
	// organisation's id confirms nothing. A module hop replayed ahead of it
	// carries the owner's authority through the last hop's binding and nothing
	// of its own: it stands while its module is installed here.
	replayed := false
	if org, prefix, key, sealed := parseModuleOperationBindingID(bindingID); sealed {
		if org != orgID {
			return ErrModuleHopStale
		}
		bindingID, replayed = key, ModulePrincipalID(prefix) != principalID
	}
	snapshot := s.moduleRegistrySnapshot()
	if snapshot == nil {
		return ErrModuleHopStale
	}
	if _, declared := snapshot.registry[principalID]; !declared {
		return ErrModuleHopStale
	}
	installed, _, err := s.authorizeModuleCapabilityFromSnapshot(ctx, snapshot,
		ModuleCaller{PrincipalID: principalID, BoundOrg: orgID})
	if err != nil {
		return err
	}
	if !moduleAvailableToOrganization(installed.Grant, orgID) {
		return ErrModuleHopStale
	}
	if replayed {
		return nil
	}
	binding, declared := installed.Grant.OperationAudiences[bindingID]
	if !declared {
		return ErrModuleHopStale
	}
	if len(scopes) == 0 {
		return nil
	}
	// The hop's scopes came from one of this binding's own lists when it was
	// minted — invoke for a call, lookup for a receipt read, headless for a
	// module acting alone. Any of them is this binding's authority; nothing
	// outside it is.
	within := make([]ModuleOperationScope, 0,
		len(binding.InvokeScopes)+len(binding.LookupScopes)+len(binding.HeadlessScopes))
	within = append(within, binding.InvokeScopes...)
	within = append(within, binding.LookupScopes...)
	within = append(within, binding.HeadlessScopes...)
	if !operationScopesSubset(scopes, within) {
		return ErrModuleHopStale
	}
	return nil
}

// IsDeclaredModulePrincipal reports whether a principal id names a declared
// module, so one caller can route a subject to the resolver that knows it.
func (s *Service) IsDeclaredModulePrincipal(principalID string) bool {
	return s.isDeclaredModulePrincipal(principalID)
}

// NamesASourceDelegation reports whether a sealed binding id names a source
// delegation, so a caller can tell the two resolvers apart by the binding
// rather than by the shape of the context.
func NamesASourceDelegation(bindingID string) bool {
	return namesASourceDelegation(bindingID)
}
