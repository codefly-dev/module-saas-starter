//go:build !pure

package infra_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"accounts/pkg/business"
	"accounts/pkg/events"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/infra"
	"accounts/pkg/infra/storetx"
	"accounts/pkg/jobs"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type placementFenceStore struct {
	business.Store
	beforeEffect func()
}

func (s *placementFenceStore) WithOrgTx(ctx context.Context, org string, fn func(context.Context) error) error {
	s.beforeEffect()
	return s.Store.WithOrgTx(ctx, org, fn)
}

func TestInstallationDBFenceSerializesPlacementAndNarrowing(t *testing.T) {
	for _, uninstall := range []bool{false, true} {
		t.Run(map[bool]string{false: "narrow", true: "uninstall"}[uninstall], func(t *testing.T) {
			a := newDynamicInstall(t, "", "", "")
			registerNode(t, a.org, "review", "root", "", "")
			ctx, cancel := context.WithTimeout(testCtx, 15*time.Second)
			defer cancel()
			reached, resume := make(chan struct{}), make(chan struct{})
			store := &placementFenceStore{Store: testStore, beforeEffect: func() {
				close(reached)
				select {
				case <-resume:
				case <-ctx.Done():
				}
			}}
			svc, err := business.NewService(store)
			require.NoError(t, err)
			svc.InstallDerivedModulePrincipals(business.ModulePrincipalRegistry{a.principal: a.grant}, a.envelope, nil)
			svc.SetModuleAuthorityReads(testStore, nil)
			caller := business.ModuleCaller{PrincipalID: a.principal, BoundOrg: a.org}
			placed := make(chan error, 1)
			go func() {
				_, err := svc.ModulePlaceRecord(ctx, caller, a.org, "review.one", "record", "Example", "records", "one")
				placed <- err
			}()
			select {
			case <-reached:
			case err := <-placed:
				t.Fatalf("placement did not reach effect: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			pid := make(chan int, 1)
			changed := make(chan error, 1)
			go func() {
				changed <- testStore.WithControlPlane(ctx, func(ctx context.Context) error {
					var backend int
					if err := storetx.Tx(ctx).QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&backend); err != nil {
						return err
					}
					pid <- backend
					if uninstall {
						_, _, err := testStore.UninstallSolution(ctx, a.org, a.installation.Id)
						return err
					}
					scope := business.FullInstallationScope(a.grant)
					scope.Resources = nil
					return testStore.NarrowInstallationScope(ctx, a.org, a.installation.Id, 1, scope, business.ModulePrincipalRegistry{a.principal: a.grant}, a.envelope)
				})
			}()
			var backend int
			select {
			case backend = <-pid:
			case err := <-changed:
				t.Fatalf("mutation did not start: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			// Observe PostgreSQL's lock wait, not a guessed sleep. The writer has
			// reached the installation update while the real placement is paused.
			require.Eventually(t, func() bool {
				var blocked bool
				err := testStore.WithControlPlane(ctx, func(ctx context.Context) error {
					return storetx.Tx(ctx).QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1)) > 0`, backend).Scan(&blocked)
				})
				return err == nil && blocked
			}, 3*time.Second, 10*time.Millisecond)
			close(resume)
			require.NoError(t, <-placed)
			require.NoError(t, <-changed)
			var exists bool
			require.NoError(t, testStore.WithOrgTx(ctx, a.org, func(ctx context.Context) error {
				return storetx.Tx(ctx).QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM scope_nodes WHERE resource_type='records' AND resource_id='one')`).Scan(&exists)
			}))
			require.True(t, exists, "effect commits before the waiting revocation")
			_, err = svc.ModulePlaceRecord(ctx, caller, a.org, "review.two", "record", "Example", "records", "two")
			require.Error(t, err, "a subsequent effect must use the narrowed/revoked consent")
		})
	}
}

func installationJobStamp(a dynamicInstall) jobs.ModuleAuthority {
	return jobs.ModuleAuthority{PrincipalID: a.principal, OrganizationID: a.org, InstallationID: a.installation.Id, InstallationRevision: 1, BindingID: a.binding, AllowsGlobal: true}
}

func TestInstallationDBCancelsHostEventConsumerWithoutProducerStamp(t *testing.T) {
	for _, uninstall := range []bool{false, true} {
		t.Run(map[bool]string{false: "narrow", true: "uninstall"}[uninstall], func(t *testing.T) {
			a := newDynamicInstall(t, "", "", "")
			b := newDynamicInstall(t, a.target, a.binding, a.principal)
			pool, worker := newJobExecutionHarness(t)
			queue := executionQueue("host-event-consumer")
			eventType := strings.ReplaceAll(queue, "-", "_") + ".created"
			seedSubscriptions(t, []events.Subscription{{TypePattern: eventType, Queue: queue, Delivery: events.DeliveryOrdered}})
			transport := infra.NewPostgresEventTransport(worker, pool, "example-relay", time.Minute)
			for _, org := range []string{a.org, a.org, b.org} {
				require.NoError(t, transport.Publish(testCtx, nil, &events.EventEnvelope{Id: business.NewIDString(), Type: eventType, Source: "urn:example:host", Specversion: "1.0", TenantId: org, SchemaVersion: 1, Data: []byte(`{}`)}))
			}
			_, err := transport.RelayOnce(testCtx)
			require.NoError(t, err)
			claim := func(i dynamicInstall) *jobsv1.JobEnvelope {
				out, err := worker.Claim(jobs.WithModuleClaim(testCtx, installationJobStamp(i)), claimExecutionRequest(queue, "example-worker", 1))
				require.NoError(t, err)
				require.Len(t, out.Jobs, 1)
				require.NotContains(t, out.Jobs[0].Attributes, "codefly.installation_id", "real host delivery has no producer installation")
				var installation string
				require.NoError(t, pool.QueryRow(testCtx, `SELECT consumer_authority->>'codefly.installation_id' FROM job_messages WHERE id=$1`, out.Jobs[0].Id).Scan(&installation))
				require.Equal(t, i.installation.Id, installation)
				return out.Jobs[0]
			}
			first, pendingRetry, foreign := claim(a), claim(a), claim(b)
			_, err = worker.Retry(jobs.WithModuleClaim(testCtx, installationJobStamp(a)), &jobsv1.RetryJobRequest{Lease: executionLease(pendingRetry), RetryAt: timestamppb.New(time.Now().Add(time.Hour)), Failure: &jobsv1.JobFailure{Code: "example.retry"}})
			require.NoError(t, err)
			require.ErrorIs(t, worker.Complete(jobs.WithModuleClaim(testCtx, installationJobStamp(b)), &jobsv1.CompleteJobRequest{Lease: executionLease(first)}), jobs.ErrLeaseLost, "a lease cannot be exercised through another organization's installation")
			// Retried host work keeps the consumer stamp after its lease is gone.
			_, err = worker.Retry(jobs.WithModuleClaim(testCtx, installationJobStamp(a)), &jobsv1.RetryJobRequest{Lease: executionLease(first), RetryAt: first.AvailableAt, Failure: &jobsv1.JobFailure{Code: "example.retry"}})
			require.NoError(t, err)
			again, err := worker.Claim(jobs.WithModuleClaim(testCtx, installationJobStamp(a)), claimExecutionRequest(queue, "example-worker", 1))
			require.NoError(t, err)
			require.Len(t, again.Jobs, 1)
			if uninstall {
				require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
					_, _, err := testStore.UninstallSolution(ctx, a.org, a.installation.Id)
					return err
				}))
			} else {
				scope := business.FullInstallationScope(a.grant)
				scope.Queues = nil
				require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
					return testStore.NarrowInstallationScope(ctx, a.org, a.installation.Id, 1, scope, business.ModulePrincipalRegistry{a.principal: a.grant}, a.envelope)
				}))
			}
			var state, reason string
			require.NoError(t, pool.QueryRow(testCtx, `SELECT state,last_error_code FROM job_messages WHERE id=$1`, first.Id).Scan(&state, &reason))
			require.Equal(t, "canceled", state)
			require.Equal(t, "revoked_authority", reason)
			require.NoError(t, pool.QueryRow(testCtx, `SELECT state,last_error_code FROM job_messages WHERE id=$1`, pendingRetry.Id).Scan(&state, &reason))
			require.Equal(t, "canceled", state)
			require.Equal(t, "revoked_authority", reason, "consumer authority survives a retry with no active lease")
			_, err = worker.Heartbeat(testCtx, &jobsv1.HeartbeatJobRequest{Lease: executionLease(again.Jobs[0]), Extension: durationpb.New(time.Minute)})
			require.ErrorIs(t, err, jobs.ErrLeaseLost)
			require.ErrorIs(t, worker.Complete(testCtx, &jobsv1.CompleteJobRequest{Lease: executionLease(again.Jobs[0])}), jobs.ErrLeaseLost)
			_, err = worker.ReplayJob(testCtx, &jobsv1.ReplayJobRequest{SourceJobId: first.Id, IdempotencyKey: business.NewIDString()})
			require.ErrorIs(t, err, jobs.ErrReplayNotAllowed)
			require.NoError(t, worker.Complete(jobs.WithModuleClaim(testCtx, installationJobStamp(b)), &jobsv1.CompleteJobRequest{Lease: executionLease(foreign)}))
		})
	}
}

func TestInstallationDBGlobalDatasourceClaimKeepsTenantIsolation(t *testing.T) {
	a := newDynamicInstall(t, "", "", "")
	b := newDynamicInstall(t, a.target, a.binding, a.principal)
	_, worker := newJobExecutionHarness(t)
	queue := executionQueue("datasource-global")
	enqueue := func(attrs map[string]string, org string) string {
		scope := &jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}}
		if org != "" {
			scope = &jobsv1.JobScope{Value: &jobsv1.JobScope_OrganizationId{OrganizationId: org}}
		}
		out, err := worker.EnqueueJob(testCtx, &jobsv1.EnqueueJobRequest{Job: &jobsv1.NewJob{Direction: jobsv1.JobDirection_JOB_DIRECTION_INBOX, Scope: scope, Queue: queue, Topic: "datasource.api.sync", Source: "example", IdempotencyKey: business.NewIDString(), SchemaVersion: 1, ContentType: "application/json", Payload: []byte(`{}`), MaxAttempts: 3, Attributes: attrs}})
		require.NoError(t, err)
		return out.JobId
	}
	// Existing producer shape, without invented codefly.* provenance.
	own := enqueue(map[string]string{"datasource.org_id": a.org, "datasource.source_id": business.NewIDString()}, "")
	foreign := enqueue(map[string]string{"datasource.org_id": b.org}, "")
	global := enqueue(nil, "")
	tenant := enqueue(nil, a.org)
	otherTenant := enqueue(nil, b.org)
	conflicting := enqueue(map[string]string{"datasource.org_id": b.org}, a.org)
	stamp := installationJobStamp(a)
	stamp.AllowsGlobal = false
	got, err := worker.Claim(jobs.WithModuleClaim(testCtx, stamp), claimExecutionRequest(queue, "pinned-worker", 10))
	require.NoError(t, err)
	ids := []string{}
	for _, job := range got.Jobs {
		ids = append(ids, job.Id)
	}
	require.ElementsMatch(t, []string{own, tenant}, ids)
	stamp.AllowsGlobal = true
	got, err = worker.Claim(jobs.WithModuleClaim(testCtx, stamp), claimExecutionRequest(queue, "shared-worker", 10))
	require.NoError(t, err)
	require.Len(t, got.Jobs, 1)
	require.Equal(t, global, got.Jobs[0].Id)
	got, err = worker.Claim(jobs.WithModuleClaim(testCtx, installationJobStamp(b)), claimExecutionRequest(queue, "other-worker", 10))
	require.NoError(t, err)
	ids = nil
	for _, job := range got.Jobs {
		ids = append(ids, job.Id)
	}
	require.ElementsMatch(t, []string{foreign, otherTenant}, ids)
	require.NotContains(t, ids, conflicting)
}
