package business

import (
	"strings"
	"testing"
)

func TestModuleAuthorityProceduresAreInternalTier(t *testing.T) {
	if err := ValidateModuleAuthorityProcedures(); err != nil {
		t.Fatal(err)
	}
}

// The endpoint is least-privilege by construction: the exchanges the gateway
// brokers, the gateway's own registries and validators, and the generic
// authority oracles stay on the private listener.
func TestModuleAuthorityProceduresExcludeHostOnlyMethods(t *testing.T) {
	hostOnly := []string{
		"/saas.accounts.v1.ModuleCapabilitiesService/MintModuleOperationContext",
		"/saas.accounts.v1.ModuleCapabilitiesService/MintModuleRegistration",
		"/saas.accounts.v1.ModuleCapabilitiesService/MintModuleWorkContext",
		"/saas.accounts.v1.ModuleCapabilitiesService/MintSolutionRegistration",
		"/saas.accounts.v1.APIKeyService/ValidateAPIKey",
		"/saas.accounts.v1.ClientRegistryService/ListRegisteredClients",
		"/saas.accounts.v1.IdentityService/ResolveIdentity",
		"/saas.accounts.v1.UsageService/ConsumeUsage",
		"/saas.accounts.v1.WorkContextService/StartInstallationTask",
	}
	for _, procedure := range hostOnly {
		if IsModuleAuthorityProcedure(procedure) {
			t.Errorf("%s must not be reachable on the %q endpoint", procedure, ModuleAuthorityEndpoint)
		}
	}
	for _, procedure := range ModuleAuthorityProcedures() {
		for _, prefix := range []string{
			"/saas.accounts.v1.SolutionRegistryService/",
			"/saas.accounts.v1.PermissionService/",
			"/saas.accounts.v1.PrincipalService/",
		} {
			if strings.HasPrefix(procedure, prefix) {
				t.Errorf("%s must not be reachable on the %q endpoint", procedure, ModuleAuthorityEndpoint)
			}
		}
	}
}

func TestIsModuleAuthorityProcedureAcceptsBothSpellings(t *testing.T) {
	if !IsModuleAuthorityProcedure("/saas.accounts.v1.WorkContextService/CheckAuthorizationRevision") ||
		!IsModuleAuthorityProcedure("saas.accounts.v1.WorkContextService/CheckAuthorizationRevision") {
		t.Fatal("CheckAuthorizationRevision must be served on the module authority endpoint")
	}
	if IsModuleAuthorityProcedure("/saas.accounts.v1.UserService/GetSelf") {
		t.Fatal("a tenant method is never served on the module authority endpoint")
	}
}
