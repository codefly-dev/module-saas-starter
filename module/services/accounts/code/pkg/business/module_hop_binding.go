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
//
// The hop names its module as the capability does: by name for a hop the host
// interface issued, by registry id for the hops it does not. Either resolves
// to the one declared module, and every comparison below is on its registry id.
func (s *Service) ConfirmModuleHopBinding(ctx context.Context, orgID, principal, bindingID string, scopes []ModuleOperationScope) error {
	ctx, releaseAuthority, fenceErr := s.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return fenceErr
	}
	defer releaseAuthority()

	snapshot := s.moduleRegistrySnapshot()
	if snapshot == nil {
		return ErrModuleHopStale
	}
	principalID, declared := snapshot.registry.RegistryIDOf(principal)
	if !declared {
		return ErrModuleHopStale
	}

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

// IsDeclaredModulePrincipal reports whether a principal names a declared
// module, by its name or its registry id, so one caller can route a subject to
// the resolver that knows it.
func (s *Service) IsDeclaredModulePrincipal(principalID string) bool {
	return s.isDeclaredModulePrincipal(principalID)
}

// NamesASourceDelegation reports whether a sealed binding id names a source
// delegation, so a caller can tell the two resolvers apart by the binding
// rather than by the shape of the context.
func NamesASourceDelegation(bindingID string) bool {
	return namesASourceDelegation(bindingID)
}

// ModuleChainHop is one hop of a context's actor chain as the token carries
// it: the principal acting and the scopes it was granted.
type ModuleChainHop struct {
	Principal string
	Scopes    []ModuleOperationScope
	// Through is the binding the hop went through and the scope set of it the
	// hop was issued under, as the actor-chain journal recorded it; zero when
	// the caller holds no record of it.
	Through ActorChainHopBinding
}

// ConfirmModuleChain re-reads EVERY module hop of a chain against the binding
// it hopped through, not only the last one the context seals.
//
// A context seals one binding, the last hop's, and core re-reads only that
// one; without this a child kept verifying while a binding upstream of it was
// withdrawn or narrowed, for as long as the child lived. A module hops only on
// a token addressed to it, so hop i went through one of its module's bindings
// addressed to the next hop's module (and the last hop through one addressed
// to audience). That binding must still be installed here, and every scope the
// hop carries of a kind its original journaled grant governed must still be within it: a kind
// the chain carried down from that hop is held to the binding as it is now,
// so a narrowing upstream takes effect below it (rule A: never recovered).
// Kinds this hop did not grant are the owner's and other hops', checked
// where they are held.
//
// audience is the context's own audience; empty leaves the last hop to the
// caller, which confirms it by the binding the context seals. A hop that is
// not a declared module (a person, a registered agent) is answered by the
// authority store, not here.
func (s *Service) ConfirmModuleChain(ctx context.Context, orgID, audience string, hops []ModuleChainHop) error {
	ctx, releaseAuthority, fenceErr := s.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return fenceErr
	}
	defer releaseAuthority()
	snapshot := s.moduleRegistrySnapshot()
	if snapshot == nil {
		return ErrModuleHopStale
	}
	for i, hop := range hops {
		id, isModule := snapshot.registry.RegistryIDOf(hop.Principal)
		if !isModule {
			continue
		}
		target := audience
		if i+1 < len(hops) {
			target = hops[i+1].Principal
			if next, ok := snapshot.registry.RegistryIDOf(target); ok {
				target = snapshot.registry[next].Prefix
			}
		} else if target == "" {
			continue
		}
		installed, _, err := s.authorizeModuleCapabilityFromSnapshot(ctx, snapshot, ModuleCaller{PrincipalID: id, BoundOrg: orgID})
		if err != nil {
			return err
		}
		if !moduleAvailableToOrganization(installed.Grant, orgID) {
			return ErrModuleHopStale
		}
		if !hopWithinItsSet(snapshot.registry, installed.Grant, orgID, id, target, hop) {
			return ErrModuleHopStale
		}
	}
	return nil
}

// hopWithinItsSet holds a hop whose binding is on record to exactly the scope
// set of that binding it was issued under: the binding must still be its
// module's own, installed here and addressed to target, and every scope the
// hop carries of a kind originally granted by this hop must lie within
// that one set. A set narrowed, or a kind it dropped, ends the hop even while
// another set of the same binding still grants it: a hop issued under invoke
// never stands on lookup. A denial row (a kind and no action) widens nothing
// and is not held.
func hopWithinItsSet(registry ModulePrincipalRegistry, grant ModulePrincipalGrant, orgID, moduleID, target string, hop ModuleChainHop) bool {
	org, prefix, key, ok := parseModuleOperationBindingID(hop.Through.BindingID)
	if !ok || org != orgID {
		return false
	}
	if owner, declared := registry.RegistryIDOf(prefix); !declared || owner != moduleID {
		return false
	}
	binding, declared := grant.OperationAudiences[key]
	if !declared || (target != "" && binding.Audience != target) {
		return false
	}
	var set []ModuleOperationScope
	switch hop.Through.Operation {
	case "invoke":
		set = binding.InvokeScopes
	case "lookup":
		set = binding.LookupScopes
	case "headless":
		set = binding.HeadlessScopes
	default:
		return false
	}
	// Descendant tokens carry other hops' kinds too. The immutable journal,
	// not today's declaration, identifies which kinds this hop granted.
	if len(hop.Through.GrantedScopes) == 0 {
		return false
	}
	governed := map[string]bool{}
	for _, scope := range hop.Through.GrantedScopes {
		governed[scope.ResourceKind] = true
	}
	for _, scope := range hop.Scopes {
		if !governed[scope.ResourceKind] || len(scope.Actions) == 0 {
			continue
		}
		if !operationScopesSubset([]ModuleOperationScope{scope}, set) {
			return false
		}
	}
	return true
}
