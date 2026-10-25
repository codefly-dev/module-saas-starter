package business

import (
	"context"
	"strings"
)

// moduleOperationBindingPrefix spells a module's installed operation binding as
// one operation-binding id: module-operation/<module principal>/<binding>. It is
// this host's identifier, looked up exactly and never searched for; the
// mini gateway spells its own differently, and the contract allows both.
const moduleOperationBindingPrefix = "module-operation/"

// ModuleOperationBindingID names one module's installed operation binding as the
// operation binding a context is sealed to.
func ModuleOperationBindingID(principalID, bindingID string) string {
	return moduleOperationBindingPrefix + principalID + "/" + bindingID
}

// IsModuleOperationBindingOf reports whether id names one of principalID's own
// installed operation bindings: the only operation binding a module operation
// context (StartModuleOperationTask) is sealed to.
func IsModuleOperationBindingOf(id, principalID string) bool {
	owner, _, ok := parseModuleOperationBindingID(id)
	return ok && owner == principalID
}

func parseModuleOperationBindingID(id string) (principalID, bindingID string, ok bool) {
	rest, isModule := strings.CutPrefix(id, moduleOperationBindingPrefix)
	if !isModule {
		return "", "", false
	}
	principalID, bindingID, found := strings.Cut(rest, "/")
	if !found || principalID == "" || bindingID == "" || strings.Contains(bindingID, "/") {
		return "", "", false
	}
	return principalID, bindingID, true
}

// moduleOperationBinding answers the live state of a module's installed operation
// binding: held by the module principal within its installation at this
// organisation, at the revision of its terms. Not declared, or not installed, is
// no binding.
func (s *Service) moduleOperationBinding(ctx context.Context, orgID, id string) (WorkContextOperationBinding, error) {
	principalID, bindingID, _ := parseModuleOperationBindingID(id)
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
	return WorkContextOperationBinding{
		ID: id, PrincipalID: principalID, InstallationID: installed.InstallationID,
		Revision: ModuleOperationContextRevision(installed.Grant), Incarnation: 1,
	}, nil
}
