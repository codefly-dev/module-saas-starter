//go:build !pure

package infra_test

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/infra"
	"accounts/pkg/infra/storetx"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type canonicalFixtureKeys map[string]ed25519.PublicKey

func (k canonicalFixtureKeys) PublicKeys(context.Context) (map[string]ed25519.PublicKey, error) {
	return k, nil
}

// Real owner Install + Accounts persistence + durable queue. The barrier is
// after setup success and before core's final transaction. No HTTP receiver is
// simulated by this test: receiver/transport qualification remains separate.
func TestCanonicalSolutionActivationFencesAcknowledgedPrerequisites(t *testing.T) {
	for _, change := range []string{"unchanged", "module revoked", "module delivery changed", "solution revoked", "setup refused", "setup retrying", "setup canceled", "wrong attempt"} {
		t.Run(change, func(t *testing.T) {
			dependency := newDynamicInstallKind(t, "", "", "", "module")
			alias := uniqueAlias("example-canonical")
			binding := "acme.test." + alias
			target := declarePresence(t, binding, alias)
			store := testStore.AuthorityCoreWithConsent(nil)
			var dependencyName string
			require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
				row, err := testStore.GetSolutionTarget(ctx, dependency.target)
				if err == nil {
					dependencyName = row.SolutionID
				}
				return err
			}))
			m := domain.Module{Name: alias, Principal: "solution:" + alias, Kind: domain.KindSolution, Generation: 1,
				EdgeDependencies: []domain.EdgeDependency{{Module: dependencyName, Presence: "required"}},
				InstallationRole: "none", Roles: map[string][]*basev0.WorkScopeV1{"installer": {{ResourceKind: "roles", Actions: []string{"write"}}}}}
			require.NoError(t, store.Write(testCtx, func(tx authority.Tx) error {
				if err := tx.PutModule(domain.Module{Name: dependencyName, Principal: dependencyName, Kind: domain.KindModule, Generation: 1}); err != nil {
					return err
				}
				return tx.PutModule(m)
			}))
			public, private, err := ed25519.GenerateKey(nil)
			require.NoError(t, err)
			core, err := authority.New(store, authority.Config{Issuer: "https://authority.example.com", KeyID: "test", Signer: private,
				Keys: canonicalFixtureKeys{"test": public}, Replay: workcontext.NewMemoryReplayStore(), HostInstallsModules: true})
			require.NoError(t, err)
			caller := authority.Person{ID: dependency.owner, Organization: dependency.org}
			approval, err := core.Review(testCtx, caller, dependency.org, alias)
			require.NoError(t, err)
			approval.OperationID = business.NewIDString()
			plan := business.SolutionInstallationPlan{Organization: dependency.org, Target: alias, MachineRole: "none", Approval: approval,
				HostTargetID: target, BindingID: binding}
			witness := business.ModuleInstallationPrerequisite{InstallationID: dependency.installation.Id, Organization: dependency.org,
				Revision: dependency.installation.Revision, TargetID: dependency.target, BindingID: dependency.binding, PrincipalID: dependency.principal,
				EnvelopeRevision: dependency.envelope}
			scope, err := json.Marshal(business.FullInstallationScope(dependency.grant))
			require.NoError(t, err)
			witness.ScopeDigest = fmt.Sprintf("%x", sha256.Sum256(scope))
			require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
				q := storetx.Tx(ctx)
				if err := q.QueryRow(ctx, `SELECT applied_release,applied_digest FROM public.solution_host_bindings WHERE binding_id=$1`, binding).
					Scan(&plan.Release, &plan.DeliveryDigest); err != nil {
					return err
				}
				if err := q.QueryRow(ctx, `SELECT applied_generation,applied_digest FROM public.solution_host_bindings WHERE binding_id=$1`, dependency.binding).
					Scan(&witness.Generation, &witness.DeliveryDigest); err != nil {
					return err
				}
				role, _, _, err := testStore.InstallationRoleRevision(ctx, dependency.org, dependency.installation.Id)
				witness.RoleID = role
				return err
			}))
			plan.Dependencies = []business.ModuleInstallationPrerequisite{witness}
			ctx := business.WithSolutionInstallationPlan(testCtx, plan)
			openPool := infra.NewJobWorkerPool
			if raw := os.Getenv("ACCOUNTS_INSTALLER_TEST_DATABASE_URL"); raw != "" {
				openPool = func(ctx context.Context) (*pgxpool.Pool, error) { return infra.NewJobWorkerPoolFromURL(ctx, raw) }
			}
			pool, err := openPool(testCtx)
			require.NoError(t, err)
			defer pool.Close()
			worker := infra.NewPostgresJobStore(pool)
			inst, err := core.Install(ctx, caller, dependency.org, alias, "", "", &approval, func(ctx context.Context, pending domain.Installation) error {
				jobID, err := testStore.EnqueueCanonicalInstallationStep(ctx, pending, false)
				if err != nil {
					return err
				}
				again, err := testStore.EnqueueCanonicalInstallationStep(ctx, pending, false)
				require.NoError(t, err)
				require.Equal(t, jobID, again, "lost enqueue response must reuse the original job")
				readOutcome := func(state string) *gen.Installation {
					var projected *gen.Installation
					for _, id := range []string{pending.ID, ""} {
						rows, err := testStore.CanonicalSolutionSummaries(ctx, pending.Organization, id, "", gen.InstallationStatus_INSTALLATION_STATUS_UNSPECIFIED, 20)
						require.NoError(t, err)
						require.Len(t, rows, 1)
						projected = rows[0].Installation
						require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_PENDING, projected.Status)
						require.Equal(t, state, projected.SetupState)
					}
					return projected
				}
				readOutcome("pending")
				job := claimInstallationFor(t, worker, pending.ID)
				require.Equal(t, jobID, job.Id)
				readOutcome("processing")
				switch change {
				case "setup refused", "wrong attempt":
					reason := business.BoundedInstallationReason("Required setup is unavailable. " + strings.Repeat("x", 700))
					require.NoError(t, worker.DeadLetter(ctx, &jobsv1.DeadLetterJobRequest{Lease: installationLease(job), Failure: &jobsv1.JobFailure{Code: "installation.refused", Message: reason}}))
					if change == "wrong attempt" {
						err := testStore.WithControlPlane(ctx, func(ctx context.Context) error {
							_, err := storetx.Tx(ctx).Exec(ctx, `UPDATE public.authority_core_installations SET consent=jsonb_set(consent,'{host_plan,Receipt,Intent}','"another-attempt"') WHERE id=$1`, pending.ID)
							return err
						})
						require.NoError(t, err)
						require.Empty(t, readOutcome("unavailable").FailureReason)
					} else {
						require.Equal(t, reason, readOutcome("dead_letter").FailureReason)
					}
					return domain.NotNow("setup did not acknowledge")
				case "setup retrying":
					_, err := worker.Retry(ctx, &jobsv1.RetryJobRequest{Lease: installationLease(job), RetryAt: timestamppb.New(time.Now().Add(time.Minute)), Failure: &jobsv1.JobFailure{Code: "transport", Message: "private transport diagnostic"}})
					require.NoError(t, err)
					projected := readOutcome("retrying")
					require.Empty(t, projected.FailureReason)
					require.NotContains(t, projected.SetupMessage, "private")
					return domain.NotNow("setup has not acknowledged")
				case "setup canceled":
					_, err := pool.Exec(ctx, `UPDATE public.job_messages SET state='canceled',completed_at=clock_timestamp(),lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,heartbeat_at=NULL WHERE id=$1::uuid`, jobID)
					require.NoError(t, err)
					readOutcome("canceled")
					return domain.NotNow("setup was canceled")
				}
				require.NoError(t, worker.Complete(ctx, &jobsv1.CompleteJobRequest{Lease: installationLease(job)}))
				require.Contains(t, readOutcome("succeeded").SetupMessage, "Continue")
				require.NoError(t, store.Read(ctx, func(tx authority.Tx) error {
					current, err := tx.Installation(pending.ID)
					require.Equal(t, domain.Pending, current.Status, "queue success is not activation")
					return err
				}))
				if change == "unchanged" {
					assertCanonicalJobAssociations(t, ctx, pending)
				}
				switch change {
				case "module revoked":
					return testStore.WithControlPlane(ctx, func(ctx context.Context) error {
						_, _, err := testStore.UninstallSolution(ctx, dependency.org, dependency.installation.Id)
						return err
					})
				case "module delivery changed":
					return testStore.WithControlPlane(ctx, func(ctx context.Context) error {
						target, err := testStore.GetSolutionTarget(ctx, dependency.target)
						if err != nil {
							return err
						}
						return testStore.SaveSolutionHostBinding(ctx, appliedPresenceRecordKind(t, dependency.binding, target.SolutionID, 2, time.Now(), false, "module"))
					})
				case "solution revoked":
					return core.Uninstall(ctx, caller, dependency.org, pending.ID)
				}
				return nil
			})
			if change == "unchanged" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.NoError(t, store.Read(testCtx, func(tx authority.Tx) error {
				current, err := tx.Installation(inst.ID)
				if change == "unchanged" {
					require.Equal(t, domain.Active, current.Status)
				} else {
					require.NotEqual(t, domain.Active, current.Status)
				}
				return err
			}))
			if change == "unchanged" {
				require.NoError(t, core.Uninstall(testCtx, caller, dependency.org, inst.ID))
				require.NoError(t, testStore.WithOrgTx(testCtx, dependency.org, func(ctx context.Context) error {
					current, _, err := testStore.GetInstallation(ctx, dependency.org, dependency.installation.Id)
					require.Equal(t, dependency.installation.Status, current.Status, "solution uninstall retains shared module consent")
					return err
				}))
			}
		})
	}
}
