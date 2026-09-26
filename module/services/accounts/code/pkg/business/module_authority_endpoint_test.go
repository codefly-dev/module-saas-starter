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
		// Authorizes on the shared perimeter credential alone and WRITES a
		// replay claim keyed on the org_id and context_id the request carries,
		// so serving it here would let any composed module burn any tenant's
		// single-use capability and fill the replay table for tenants it has no
		// relationship with. Same class as ConsumeUsage below.
		"/saas.accounts.v1.WorkContextService/ConsumeSingleUse",
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

// TestModuleAuthorityValidationRefusesACallerUnboundProcedure exercises the gate
// itself, not the current list: the endpoint's whole security argument is that
// every procedure on it either authenticates the calling module from a signed
// Work Context or is a declared read-only oracle. A one-line edit adding an
// internal method that authorizes on the shared perimeter credential alone —
// which is how ConsumeSingleUse reached the list — must fail generation.
func TestModuleAuthorityValidationRefusesACallerUnboundProcedure(t *testing.T) {
	restore := moduleAuthorityProcedures
	t.Cleanup(func() { moduleAuthorityProcedures = restore })

	for name, procedure := range map[string]string{
		"a mutating perimeter-credential-only method": "/saas.accounts.v1.WorkContextService/ConsumeSingleUse",
		"the headless installation mint":              "/saas.accounts.v1.WorkContextService/StartInstallationTask",
		"a usage consumption":                         "/saas.accounts.v1.UsageService/ConsumeUsage",
	} {
		moduleAuthorityProcedures = append(append([]string(nil), restore...), procedure)
		err := ValidateModuleAuthorityProcedures()
		if err == nil {
			t.Errorf("%s (%s) must be refused on the %q endpoint", name, procedure, ModuleAuthorityEndpoint)
			continue
		}
		if !strings.Contains(err.Error(), "read-only oracle") {
			t.Errorf("%s: refusal must name the rule it broke, got %v", name, err)
		}
	}

	// A Mint* method is on the capability surface, so the service-prefix rule
	// alone would admit it; it authenticates with the module's identity secret
	// rather than a Work Context and the gateway brokers it.
	moduleAuthorityProcedures = append(append([]string(nil), restore...),
		"/saas.accounts.v1.ModuleCapabilitiesService/MintModuleWorkContext")
	err := ValidateModuleAuthorityProcedures()
	if err == nil || !strings.Contains(err.Error(), "identity secret") {
		t.Errorf("a Mint* capability method must be refused by name, got %v", err)
	}
}

// A justification for a procedure nobody serves is dead documentation that the
// next reader will trust.
func TestModuleAuthorityReadOraclesAreAllServed(t *testing.T) {
	for procedure := range moduleAuthorityReadOracles {
		if !IsModuleAuthorityProcedure(procedure) {
			t.Errorf("read oracle %s is declared but not served on the %q endpoint", procedure, ModuleAuthorityEndpoint)
		}
	}
}
