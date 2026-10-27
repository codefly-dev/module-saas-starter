package business

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

// ModuleOperationBindingID names one module's installed operation binding as
// the operation binding a context is sealed to, as the host interface states
// it: <organisation>/<module>/<binding>. A binding's id is its module's own,
// so it is named within that module and the organisation it is installed in.
// It is looked up exactly and never searched for.
func ModuleOperationBindingID(orgID, prefix, bindingID string) string {
	return orgID + "/" + prefix + "/" + bindingID
}

// IsModuleOperationBindingOf reports whether id names one of principalID's own
// installed operation bindings in orgID: the only operation binding a module
// operation context (StartModuleOperationTask) is sealed to.
func IsModuleOperationBindingOf(id, orgID, principalID string) bool {
	org, prefix, _, ok := parseModuleOperationBindingID(id)
	return ok && org == orgID && ModulePrincipalID(prefix) == principalID
}

// NamesModuleOperationBinding reports whether id is a module's installed
// operation binding (<organisation>/<module>/<binding>), the binding every hop
// through a binding seals, as opposed to a source delegation's or an installed
// content read's.
func NamesModuleOperationBinding(id string) bool {
	_, _, _, ok := parseModuleOperationBindingID(id)
	return ok
}

// parseModuleOperationBindingID reads <organisation>/<module>/<binding>. The
// organisation is a canonical UUID, which is what tells this id apart from
// every other binding id this host issues (a source delegation's, an installed
// binding's), each of which starts with its own word.
func parseModuleOperationBindingID(id string) (orgID, prefix, bindingID string, ok bool) {
	parts := strings.Split(id, "/")
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	if parsed, err := uuid.Parse(parts[0]); err != nil || parsed.String() != parts[0] {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// moduleOperationBinding answers the live state of a module's installed operation
// binding: held by the module within its installation at this organisation, at
// the revision of its terms. Another organisation's id, not declared, or not
// installed, is no binding.
//
// The principal it is granted to is the module's NAME, the one the binding id
// already names it by and the one a hop exercising it carries as its actor.
// Core holds a context to this answer by equality (the exercising principal
// must be the binding's), so a module that presents another module's binding
// is refused by name. ModuleRegistryID is the same module as this host stores
// it, which is how the module's own headless context names its owner
// (hostSealSource answers that context in its owner's spelling).
func (s *Service) moduleOperationBinding(ctx context.Context, orgID, id string) (WorkContextOperationBinding, error) {
	org, prefix, bindingID, _ := parseModuleOperationBindingID(id)
	if org != orgID {
		return WorkContextOperationBinding{}, ErrNoOperationBinding
	}
	principalID := ModulePrincipalID(prefix)
	ctx, releaseAuthority, fenceErr := s.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return WorkContextOperationBinding{}, fenceErr
	}
	defer releaseAuthority()
	snapshot := s.moduleRegistrySnapshot()
	installed, _, err := s.authorizeModuleCapabilityFromSnapshot(ctx, snapshot, ModuleCaller{PrincipalID: principalID, BoundOrg: orgID})
	if err != nil {
		return WorkContextOperationBinding{}, err
	}
	if _, declared := installed.Grant.OperationAudiences[bindingID]; !declared {
		return WorkContextOperationBinding{}, ErrNoOperationBinding
	}
	binding := WorkContextOperationBinding{
		ID: id, PrincipalID: prefix, ModuleRegistryID: principalID, InstallationID: installed.InstallationID,
		Revision: ModuleOperationContextRevision(installed.Grant), Incarnation: 1,
	}
	return binding, nil
}
