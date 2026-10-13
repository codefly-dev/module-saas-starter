//go:build !pure

package infra_test

import (
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/infra/storetx"
	"accounts/pkg/jobs"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

type dynamicInstall struct {
	org, owner, target, binding, principal string
	installation                           *gen.Installation
	grant                                  business.ModulePrincipalGrant
	envelope                               uint64
}

func newDynamicInstall(t *testing.T, target, binding, principal string, pending ...bool) dynamicInstall {
	t.Helper()
	owner := seedUser(t)
	seedHumanPrincipal(t, owner, "Jane Doe")
	org := seedOrg(t, owner)
	require.NoError(t, testStore.As(business.Identity{OrgID: org}).AddOrgMember(testCtx, owner, "admin"))
	role := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		return testStore.CreateRole(ctx, &gen.Role{Id: role, Name: "Example role " + role, OrgId: org, Permissions: []*gen.Permission{{Resource: "solution", Action: "use"}}})
	}))
	if target == "" {
		alias := uniqueAlias("example-installed")
		binding = "acme.test." + alias
		target = declarePresence(t, binding, alias)
		principal = business.ModulePrincipalID(alias)
	}
	grant := business.ModulePrincipalGrant{Prefix: "example", PresenceBinding: binding, Tenant: org, CrossTenant: true, Queues: []string{"example.ingest"}, Resources: []string{"records"}}
	scope := business.FullInstallationScope(grant)
	var revision uint64
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var err error
		revision, err = testStore.LatestModuleEnvelopeRevision(ctx)
		return err
	}))
	var installation *gen.Installation
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		var err error
		installation, err = testStore.InstallSolution(ctx, &business.InstallSolutionParams{OrgID: org, AgentIdentifier: "acme.example/installed:1.0.0", TargetID: target,
			OwnerPrincipalID: owner, GrantedBy: owner, RoleID: role, Kind: "solution", RegistryBindingID: binding, RegistryPrincipalID: principal, Scope: &scope, EnvelopeRevision: revision})
		return err
	}))
	if len(pending) == 0 || !pending[0] {
		acknowledgeInstallation(t, installation)
	}
	return dynamicInstall{org, owner, target, binding, principal, installation, grant, revision}
}

func TestDynamicInstallationDBIdentityScopeRevisionAndRLS(t *testing.T) {
	a := newDynamicInstall(t, "", "", "")
	b := newDynamicInstall(t, a.target, a.binding, a.principal)
	live, err := testStore.LiveModuleAuthority(testCtx, a.principal, a.org, "")
	require.NoError(t, err)
	require.Equal(t, a.installation.Id, live.InstallationID)
	require.NotEqual(t, a.installation.AgentPrincipalId, a.principal)
	require.Equal(t, a.binding, live.RegistryBindingID)
	require.Equal(t, uint64(1), live.InstallationRevision)
	require.Equal(t, business.FullInstallationScope(a.grant), live.Scope)
	_, err = testStore.LiveModuleAuthority(testCtx, a.principal, b.org, a.installation.Id)
	require.ErrorIs(t, err, business.ErrModuleInstallationInactive)
	_, err = testStore.LiveModuleAuthority(testCtx, business.ModulePrincipalID("foreign"), a.org, a.installation.Id)
	require.ErrorIs(t, err, business.ErrModuleInstallationInactive)
	// Tenant A cannot read B even when it knows the exact id.
	require.NoError(t, testStore.WithOrgTx(testCtx, a.org, func(ctx context.Context) error {
		var n int
		err := storetx.Tx(ctx).QueryRow(ctx, `SELECT count(*) FROM installations WHERE id=$1`, b.installation.Id).Scan(&n)
		require.Zero(t, n)
		if err != nil {
			return err
		}
		foreignStamp, err := json.Marshal(jobs.ModuleAuthority{PrincipalID: b.principal, OrganizationID: b.org, InstallationID: b.installation.Id, InstallationRevision: 1, BindingID: b.binding}.Attributes())
		if err != nil {
			return err
		}
		var allowed bool
		err = storetx.Tx(ctx).QueryRow(ctx, `SELECT public.module_job_authority_current($1::jsonb)`, foreignStamp).Scan(&allowed)
		require.False(t, allowed, "the definer predicate cannot expose another organization's consent to a tenant connection")
		return err
	}))
	wider := business.FullInstallationScope(a.grant)
	wider.Queues = append(wider.Queues, "foreign.queue")
	err = testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return testStore.NarrowInstallationScope(ctx, a.org, a.installation.Id, 1, wider, business.ModulePrincipalRegistry{a.principal: a.grant}, a.envelope)
	})
	require.ErrorContains(t, err, "must narrow")
	narrow := business.FullInstallationScope(a.grant)
	narrow.Queues = nil
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return testStore.NarrowInstallationScope(ctx, a.org, a.installation.Id, 1, narrow, business.ModulePrincipalRegistry{a.principal: a.grant}, a.envelope)
	}))
	live, err = testStore.LiveModuleAuthority(testCtx, a.principal, a.org, "")
	require.NoError(t, err)
	require.Equal(t, uint64(2), live.InstallationRevision)
	require.Empty(t, live.Scope.Queues)
	other, err := testStore.LiveModuleAuthority(testCtx, b.principal, b.org, "")
	require.NoError(t, err)
	require.Equal(t, uint64(1), other.InstallationRevision)
	require.NotEmpty(t, other.Scope.Queues)
	// Repeating identical scope does not advance revision; a stale writer loses.
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return testStore.NarrowInstallationScope(ctx, a.org, a.installation.Id, 2, narrow, business.ModulePrincipalRegistry{a.principal: a.grant}, a.envelope)
	}))
	err = testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return testStore.NarrowInstallationScope(ctx, a.org, a.installation.Id, 1, narrow, business.ModulePrincipalRegistry{a.principal: a.grant}, a.envelope)
	})
	require.ErrorContains(t, err, "revision changed")
}

func TestDynamicInstallationDBCancelsOnlyItsOrgAndFencesLeases(t *testing.T) {
	for _, narrow := range []bool{false, true} {
		t.Run(map[bool]string{false: "uninstall", true: "narrow"}[narrow], func(t *testing.T) {
			a := newDynamicInstall(t, "", "", "")
			b := newDynamicInstall(t, a.target, a.binding, a.principal)
			pool, worker := newJobExecutionHarness(t)
			queue := executionQueue("installation-revoke")
			stamp := func(i dynamicInstall) jobs.ModuleAuthority {
				return jobs.ModuleAuthority{PrincipalID: i.principal, OrganizationID: i.org, InstallationID: i.installation.Id, InstallationRevision: 1, BindingID: i.binding}
			}
			enqueue := func(i dynamicInstall) string {
				req := jobs.StampModuleJob(&jobsv1.EnqueueJobRequest{Job: &jobsv1.NewJob{Direction: jobsv1.JobDirection_JOB_DIRECTION_OUTBOX,
					Scope: &jobsv1.JobScope{Value: &jobsv1.JobScope_OrganizationId{OrganizationId: i.org}}, Queue: queue, Topic: "example.work", Source: "example", IdempotencyKey: business.NewIDString(), SchemaVersion: 1, ContentType: "application/json", MaxAttempts: 3}}, stamp(i))
				out, err := worker.EnqueueJob(testCtx, req)
				require.NoError(t, err)
				return out.JobId
			}
			orderedQueue := queue + ".ordered"
			ordered := func(stamped bool) string {
				req := &jobsv1.EnqueueJobRequest{Job: &jobsv1.NewJob{Direction: jobsv1.JobDirection_JOB_DIRECTION_OUTBOX,
					Scope: &jobsv1.JobScope{Value: &jobsv1.JobScope_OrganizationId{OrganizationId: a.org}}, Queue: orderedQueue, Topic: "example.work", Source: "example",
					Ordering: &jobsv1.JobOrderingKey{Namespace: "example", Components: []string{"one"}}, IdempotencyKey: business.NewIDString(), SchemaVersion: 1, ContentType: "application/json", MaxAttempts: 3}}
				if stamped {
					req = jobs.StampModuleJob(req, stamp(a))
				}
				out, err := worker.EnqueueJob(testCtx, req)
				require.NoError(t, err)
				return out.JobId
			}
			ordered(true)
			successor := ordered(false)
			leasedID := enqueue(a)
			claimed, err := worker.Claim(jobs.WithModuleClaim(testCtx, stamp(a)), claimExecutionRequest(queue, "example-worker", 1))
			require.NoError(t, err)
			require.Len(t, claimed.Jobs, 1)
			require.Equal(t, leasedID, claimed.Jobs[0].Id)
			pendingID := enqueue(a)
			foreignID := enqueue(b)
			// A's installation does not lease B's payload, even on a shared queue.
			onlyA, err := worker.Claim(jobs.WithModuleClaim(testCtx, stamp(a)), claimExecutionRequest(queue, "example-worker-2", 10))
			require.NoError(t, err)
			require.Len(t, onlyA.Jobs, 1)
			require.Equal(t, pendingID, onlyA.Jobs[0].Id)
			pendingID = enqueue(a)
			foreignLease, err := worker.Claim(jobs.WithModuleClaim(testCtx, stamp(b)), claimExecutionRequest(queue, "other-worker", 1))
			require.NoError(t, err)
			require.Len(t, foreignLease.Jobs, 1)
			require.Equal(t, foreignID, foreignLease.Jobs[0].Id)
			foreignPendingID := enqueue(b)
			svc, err := business.NewService(testStore)
			require.NoError(t, err)
			svc.InstallDerivedModulePrincipals(business.ModulePrincipalRegistry{a.principal: a.grant}, a.envelope, nil)
			emitter, err := business.NewDurableAuditEmitter(testStore, testStore)
			require.NoError(t, err)
			svc.SetAuditEmitter(emitter)
			wireNarrowingPolicyLog(svc)
			if narrow {
				scope := business.FullInstallationScope(a.grant)
				scope.Queues = nil
				require.NoError(t, svc.NarrowInstallation(testCtx, a.owner, a.org, a.installation.Id, scope))
			} else {
				require.NoError(t, svc.UninstallSolution(testCtx, a.owner, a.org, a.installation.Id))
			}
			for _, id := range []string{leasedID, pendingID} {
				var state, reason string
				require.NoError(t, pool.QueryRow(testCtx, `SELECT state,last_error_code FROM job_messages WHERE id=$1`, id).Scan(&state, &reason))
				require.Equal(t, "canceled", state)
				require.Equal(t, "revoked_authority", reason)
			}
			var foreignState string
			require.NoError(t, pool.QueryRow(testCtx, `SELECT state FROM job_messages WHERE id=$1`, foreignPendingID).Scan(&foreignState))
			require.Equal(t, "pending", foreignState)
			require.NoError(t, worker.Complete(testCtx, &jobsv1.CompleteJobRequest{Lease: executionLease(foreignLease.Jobs[0])}), "another organization's live lease survives")
			require.ErrorIs(t, worker.Complete(testCtx, &jobsv1.CompleteJobRequest{Lease: executionLease(claimed.Jobs[0])}), jobs.ErrLeaseLost)
			_, err = worker.ReplayJob(testCtx, &jobsv1.ReplayJobRequest{SourceJobId: leasedID, IdempotencyKey: business.NewIDString()})
			require.ErrorIs(t, err, jobs.ErrReplayNotAllowed)
			other, err := testStore.LiveModuleAuthority(testCtx, b.principal, b.org, "")
			require.NoError(t, err)
			require.Equal(t, uint64(1), other.InstallationRevision)
			unblocked, err := worker.Claim(testCtx, claimExecutionRequest(orderedQueue, "host-worker", 2))
			require.NoError(t, err)
			require.Len(t, unblocked.Jobs, 1)
			require.Equal(t, successor, unblocked.Jobs[0].Id, "cancellation releases the FIFO key for unaffected work")
		})
	}
}

// This test observes rows immediately after the envelope commit, before any
// reconcile pass. Revocation must be in that transaction, not on the next tick.
func TestDynamicInstallationDBTenancyChangeRevokesOnlyForbiddenOrganizations(t *testing.T) {
	s := newRegistryScenario(t, "install-tenancy")
	s.established(t)
	before, err := testStore.LiveModuleAuthority(testCtx, business.ModulePrincipalID(s.prefix), s.tenant, "")
	require.NoError(t, err)
	otherOrg := seedOrg(t, s.admin)
	require.NoError(t, testStore.As(business.Identity{OrgID: otherOrg}).AddOrgMember(testCtx, s.admin, "admin"))
	revision := s.putEnvelope(t, envelopeOptions{resources: true, crossTenant: true})
	s.deliverPresence(t, s.primary, 2, revision)
	s.pass(t)
	s.deliverAuthority(t, s.authority(t, s.primary, 2, revision, false, s.grantsTo(s.primary)))
	s.pass(t)
	other := s.install(t, otherOrg)
	s.putEnvelope(t, envelopeOptions{resources: true})
	_, err = testStore.LiveModuleAuthority(testCtx, business.ModulePrincipalID(s.prefix), otherOrg, other.Id)
	require.ErrorIs(t, err, business.ErrModuleInstallationInactive)
	kept, err := testStore.LiveModuleAuthority(testCtx, business.ModulePrincipalID(s.prefix), s.tenant, before.InstallationID)
	require.NoError(t, err)
	require.Equal(t, before.InstallationRevision, kept.InstallationRevision)
	require.Equal(t, before.Scope, kept.Scope)
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var state string
		var rev uint64
		err := storetx.Tx(ctx).QueryRow(ctx, `SELECT status,revision FROM installations WHERE id=$1 AND org_id=$2`, other.Id, otherOrg).Scan(&state, &rev)
		require.Equal(t, "revoked", state)
		require.Equal(t, uint64(2), rev)
		return err
	}))
}
