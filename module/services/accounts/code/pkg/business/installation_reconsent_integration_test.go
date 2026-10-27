//go:build !pure

package business_test

import (
	"context"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Both public install paths must repair a consent-required module without
// replacing its identity, and neither may treat a member's request as consent.
func TestModuleReinstallRecordsExplicitAdministratorConsent(t *testing.T) {
	for _, byName := range []bool{false, true} {
		name := "InstallSolution"
		if byName {
			name = "InstallTargetByName"
		}
		t.Run(name, func(t *testing.T) {
			clearData(t)
			ctx := testCtx
			owner, org := mustUserAndOrg(t, ctx, "reconsent@example.com", "reconsent-owner", "Acme Consent")
			member, _ := mustUserAndOrg(t, ctx, "member-consent@example.com", "reconsent-member", "ExampleCorp")
			require.NoError(t, testStore.As(business.Identity{OrgID: org}).AddOrgMember(ctx, member, "member"))
			role := seedInstallableRole(t, ctx, org)
			alias := uniqueAlias("reconsent")
			binding := "acme.test." + alias
			target := declareCapabilityPresence(t, binding, alias, business.SolutionDeclaredKindModule)
			principal := business.ModulePrincipalID(alias)
			grant := business.ModulePrincipalGrant{Prefix: alias, PresenceBinding: binding, Queues: []string{"example.read"}}
			previous := testService.ModulePrincipals()
			t.Cleanup(func() { testService.SetModulePrincipals(previous) })
			testService.SetModulePrincipals(business.ModulePrincipalRegistry{principal: grant})
			// Reproduce the row created before a no-envelope host required snapshots.
			following := business.InstallationScope{Capabilities: true, Follows: true}
			var installed *gen.Installation
			require.NoError(t, testStore.WithOrgTx(ctx, org, func(ctx context.Context) error {
				var err error
				installed, err = testStore.InstallSolution(ctx, &business.InstallSolutionParams{
					OrgID: org, TargetID: target, Kind: "module", RegistryBindingID: binding, RegistryPrincipalID: principal,
					Scope: &following, OwnerPrincipalID: owner, GrantedBy: owner, RoleID: role,
					AgentIdentifier: "acme/" + alias + ":1.0.0", DisplayName: alias,
				})
				return err
			}))
			state := func() *business.InstallationAuthorityState {
				t.Helper()
				var out *business.InstallationAuthorityState
				require.NoError(t, testStore.WithOrgTx(ctx, org, func(ctx context.Context) error {
					var err error
					out, err = testStore.InstallationAuthorityState(ctx, org, installed.Id)
					return err
				}))
				return out
			}
			caller := business.ModuleCaller{PrincipalID: principal, BoundOrg: org}
			_, err := testService.AuthorizeModuleCapability(ctx, caller)
			require.Equal(t, codes.FailedPrecondition, status.Code(err))
			require.ErrorContains(t, err, "InstallationService/Install")
			reInstall := func(actor string) (*gen.Installation, error) {
				approval := &business.InstallationApproval{TargetID: target, Generation: 1, ExpectedRevision: state().Revision, OperationID: uuid.NewString()}
				if byName {
					row, created, err := testService.InstallTargetByName(ctx, actor, org, alias, "installable role "+role, "", approval)
					require.Equal(t, err == nil, created, "a successful module re-install records an explicit consent action")
					return row, err
				}
				return testService.InstallSolution(ctx, actor, &business.InstallSolutionParams{
					Approval: approval, OrgID: org, TargetID: target, AgentIdentifier: "acme/" + alias + ":1.0.0", RoleID: role,
				})
			}
			before := state()
			_, err = reInstall(member)
			require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
			require.Equal(t, before, state(), "a non-admin cannot grant consent even naming an admin owner")
			_, err = testService.AuthorizeModuleCapability(ctx, caller)
			require.Equal(t, codes.FailedPrecondition, status.Code(err))

			require.NoError(t, testStore.As(business.Identity{OrgID: org}).AddOrgMember(ctx, member, "admin"))
			row, err := reInstall(member)
			require.NoError(t, err)
			require.Equal(t, installed.Id, row.Id)
			require.Equal(t, owner, row.OwnerPrincipalId, "another admin re-consents without taking ownership")
			require.Equal(t, business.FullInstallationScope(grant), state().Scope)
			require.Greater(t, state().Revision, before.Revision)
			held, err := testService.AuthorizeModuleCapability(ctx, caller)
			require.NoError(t, err)
			require.Equal(t, []string{"example.read"}, held.Grant.Queues)

			// Delivery alone never approves a wider ceiling. The next explicit install does.
			grant.Queues = append(grant.Queues, "example.write")
			testService.SetModulePrincipals(business.ModulePrincipalRegistry{principal: grant})
			held, err = testService.AuthorizeModuleCapability(ctx, caller)
			require.NoError(t, err)
			require.Equal(t, []string{"example.read"}, held.Grant.Queues)
			_, err = reInstall(owner)
			require.NoError(t, err)
			held, err = testService.AuthorizeModuleCapability(ctx, caller)
			require.NoError(t, err)
			require.Equal(t, grant.Queues, held.Grant.Queues)
		})
	}
}
