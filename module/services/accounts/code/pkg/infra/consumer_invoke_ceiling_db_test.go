//go:build !pure

package infra_test

// ConsumerInvokeCeiling against real Postgres rows: the installation's delivered
// principal and consent read through the real store, and the live authority read
// through the real fence (issue #1083).

import (
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func consumerCeilingBindings() map[string]business.ModuleOperationAudience {
	return map[string]business.ModuleOperationAudience{"documents": {Audience: "documents",
		InvokeScopes: []business.ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"read"}}},
		LookupScopes: []business.ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"read"}}}}}
}

func consumerCeilingInstall(t *testing.T, installed business.ModulePrincipalGrant) dynamicInstall {
	t.Helper()
	owner := seedUser(t)
	seedHumanPrincipal(t, owner, "Jane Doe")
	org := seedOrg(t, owner)
	require.NoError(t, testStore.As(business.Identity{OrgID: org}).AddOrgMember(testCtx, owner, "admin"))
	role := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		return testStore.CreateRole(ctx, &gen.Role{Id: role, Name: "Example role " + role, OrgId: org, Permissions: []*gen.Permission{{Resource: "solution", Action: "use"}}})
	}))
	alias := uniqueAlias("example-consumer")
	binding := "acme.test." + alias
	target := declarePresence(t, binding, alias)
	principal := business.ModulePrincipalID(alias)
	installed.PresenceBinding, installed.Tenant, installed.CrossTenant = binding, org, true
	scope := business.FullInstallationScope(installed)
	var revision uint64
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var err error
		revision, err = testStore.LatestModuleEnvelopeRevision(ctx)
		return err
	}))
	var installation *gen.Installation
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		var err error
		installation, err = testStore.InstallSolution(ctx, &business.InstallSolutionParams{OrgID: org, AgentIdentifier: "acme.example/consumer:1.0.0", TargetID: target,
			OwnerPrincipalID: owner, GrantedBy: owner, RoleID: role, Kind: "solution", RegistryBindingID: binding, RegistryPrincipalID: principal, Scope: &scope, EnvelopeRevision: revision})
		return err
	}))
	return dynamicInstall{org, owner, target, binding, principal, installation, installed, revision}
}

func consumerCeilingService(t *testing.T, a dynamicInstall, current business.ModulePrincipalGrant) *business.Service {
	t.Helper()
	current.PresenceBinding, current.Tenant, current.CrossTenant = a.binding, a.org, true
	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	svc.InstallDerivedModulePrincipals(business.ModulePrincipalRegistry{a.principal: current}, a.envelope)
	svc.SetModuleAuthorityReads(testStore, nil)
	return svc
}

// Control: the declared invoke binding, consented at install, is the ceiling.
func TestConsumerInvokeCeilingDBDeclaredBindingIsTheCeiling(t *testing.T) {
	grant := business.ModulePrincipalGrant{Prefix: "example", Resources: []string{"records"}, OperationAudiences: consumerCeilingBindings()}
	a := consumerCeilingInstall(t, grant)
	svc := consumerCeilingService(t, a, grant)
	ceiling, err := svc.ConsumerInvokeCeiling(testCtx, a.org, a.installation.Id)
	require.NoError(t, err)
	require.Equal(t, business.ConsumerCeilingAdmitted,
		ceiling.Admit("documents", []business.ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"read"}}}))
}

// The consumer declares a consumed module AFTER the organization installed it
// (a new authority generation). Consent is fixed at install, so the module is
// outside the ceiling — and the verdict says NOT CONSENTED, which is what an
// operator has to change, not undeclared.
func TestConsumerInvokeCeilingDBModuleDeclaredAfterInstallIsNotConsented(t *testing.T) {
	before := business.ModulePrincipalGrant{Prefix: "example", Resources: []string{"records"}}
	a := consumerCeilingInstall(t, before)
	after := before
	after.OperationAudiences = consumerCeilingBindings()
	svc := consumerCeilingService(t, a, after)

	ceiling, err := svc.ConsumerInvokeCeiling(testCtx, a.org, a.installation.Id)

	require.NoError(t, err)
	evidence := []business.ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"read"}}}
	declared, _ := business.ConsumerCeilingAdmits(ceiling.Consented, "documents", evidence)
	require.False(t, declared, "the install-time consent does not reach the module declared since")
	require.Equal(t, business.ConsumerCeilingNotConsented, ceiling.Admit("documents", evidence))
}

// An installation row with no registry principal (the state migration 32's
// backfill leaves every pre-existing row in; the store writes the same NULL for
// an empty RegistryPrincipalID).
func TestConsumerInvokeCeilingDBRowWithoutDeliveredAuthorityIsRefusedByName(t *testing.T) {
	grant := business.ModulePrincipalGrant{Prefix: "example", Resources: []string{"records"}, OperationAudiences: consumerCeilingBindings()}
	owner := seedUser(t)
	seedHumanPrincipal(t, owner, "Jane Doe")
	org := seedOrg(t, owner)
	require.NoError(t, testStore.As(business.Identity{OrgID: org}).AddOrgMember(testCtx, owner, "admin"))
	role := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		return testStore.CreateRole(ctx, &gen.Role{Id: role, Name: "Example role " + role, OrgId: org, Permissions: []*gen.Permission{{Resource: "solution", Action: "use"}}})
	}))
	alias := uniqueAlias("example-legacy")
	binding := "acme.test." + alias
	target := declarePresence(t, binding, alias)
	var installation *gen.Installation
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		var err error
		installation, err = testStore.InstallSolution(ctx, &business.InstallSolutionParams{OrgID: org, AgentIdentifier: "acme.example/legacy:1.0.0", TargetID: target,
			OwnerPrincipalID: owner, GrantedBy: owner, RoleID: role, Kind: "solution", RegistryBindingID: binding})
		return err
	}))
	a := dynamicInstall{org: org, binding: binding, principal: business.ModulePrincipalID(alias), installation: installation}
	svc := consumerCeilingService(t, a, grant)
	_, err := svc.ConsumerInvokeCeiling(testCtx, a.org, a.installation.Id)
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "%v", err)
	require.Contains(t, err.Error(), "records no delivered authority")
}
