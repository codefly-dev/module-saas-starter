//go:build !pure

package business_test

import (
	"accounts/pkg/business"
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestInstallationScopeInstallNarrowAuditAndPolicyLog(t *testing.T) {
	clearData(t)
	owner, org := mustUserAndOrg(t, testCtx, "scope@example.com", "scope-admin", "Acme Scope")
	role := seedInstallableRole(t, testCtx, org)
	target, alias := declarePresence(t, "scope")
	registry := testService.ModulePrincipals()
	principal := business.ModulePrincipalID(alias)
	grant := registry[principal]
	grant.Queues = []string{"example.ingest", "example.export"}
	registry[principal] = grant
	testService.SetModulePrincipals(registry)
	scope := business.FullInstallationScope(grant)
	scope.Queues = []string{"foreign.queue"}
	params := &business.InstallSolutionParams{OrgID: org, TargetID: target, AgentIdentifier: "acme.example/scope:1.0.0", RoleID: role, Scope: &scope}
	_, err := testService.InstallSolution(testCtx, owner, params)
	require.ErrorContains(t, err, "scope exceeds")
	scope.Queues = []string{"example.ingest"}
	installed, err := testService.InstallSolution(testCtx, owner, params)
	require.NoError(t, err)
	state := func() *business.InstallationAuthorityState {
		t.Helper()
		var out *business.InstallationAuthorityState
		require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
			var err error
			out, err = testStore.InstallationAuthorityState(ctx, org, installed.Id)
			return err
		}))
		return out
	}
	require.Equal(t, scope, state().Scope)
	mark := testPolicyLog.mark()
	wider := business.FullInstallationScope(grant)
	require.Error(t, testService.NarrowInstallation(testCtx, owner, org, installed.Id, wider))
	require.Empty(t, testPolicyLog.appended(mark))
	require.Equal(t, uint64(1), state().Revision)
	scope.Queues = nil
	testPolicyLog.mu.Lock()
	testPolicyLog.failWith = errPolicyLogDown
	testPolicyLog.mu.Unlock()
	err = testService.NarrowInstallation(testCtx, owner, org, installed.Id, scope)
	testPolicyLog.mu.Lock()
	testPolicyLog.failWith = nil
	testPolicyLog.mu.Unlock()
	require.ErrorIs(t, err, business.ErrPolicyLogUnreachable)
	require.Equal(t, uint64(1), state().Revision)
	require.NoError(t, testService.NarrowInstallation(testCtx, owner, org, installed.Id, scope))
	require.Equal(t, uint64(2), state().Revision)
	require.Equal(t, scope, state().Scope)
	entries := testPolicyLog.appended(mark)
	require.Len(t, entries, 1)
	require.Equal(t, installed.Id, entries[0].SubjectID)
	require.Equal(t, business.PolicyLogNarrowed, entries[0].Decision)
	require.Equal(t, "active", entries[0].Policy["status"])
	require.Contains(t, entries[0].Policy, "scope")
	require.Empty(t, uncommittedOperations(t, testCtx))
	audit, _, _, err := testService.QueryAuditLog(testCtx, business.AuditQuery{OrgID: org, EventType: string(business.EventInstallationNarrowed), PageSize: 10})
	require.NoError(t, err)
	require.Len(t, audit, 1)
	require.Equal(t, owner, audit[0].ActorID)
	require.Equal(t, installed.Id, audit[0].ResourceID)
	require.Equal(t, "2", audit[0].Payload["revision"])
	require.NoError(t, testService.NarrowInstallation(testCtx, owner, org, installed.Id, scope))
	require.Equal(t, uint64(2), state().Revision, "a no-op does not cancel work again")
	require.Len(t, testPolicyLog.appended(mark), 1)
	_, foreignOrg := mustUserAndOrg(t, testCtx, "foreign-scope@example.com", "foreign-scope", "ExampleCorp")
	require.Error(t, testService.NarrowInstallation(testCtx, owner, foreignOrg, installed.Id, scope))
	require.Equal(t, uint64(2), state().Revision)
}
