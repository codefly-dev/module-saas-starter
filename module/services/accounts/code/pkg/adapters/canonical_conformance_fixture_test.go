package adapters

import (
	"context"
	"fmt"
	"testing"

	"accounts/pkg/business"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/infra"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/durationpb"
)

// The conformance composition has no executable solution backend. Its explicit
// setup handler acknowledges through the real durable queue, so the unchanged
// host activation fence still applies. Receiver execution is qualified separately.
func canonicalConformanceStep(t *testing.T, ctx context.Context, svc *business.Service, store *infra.PostgresStore) authority.InstallStep {
	t.Helper()
	pool, err := infra.NewJobWorkerPool(ctx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	worker := infra.NewPostgresJobStore(pool)
	svc.SetJobOperations(worker)
	return func(ctx context.Context, inst domain.Installation) error {
		plan, err := store.SolutionInstallationPlan(ctx, inst.Organization, inst.ID)
		if err != nil {
			return err
		}
		var envelope uint64
		err = store.WithControlPlane(ctx, func(ctx context.Context) error {
			var err error
			envelope, err = store.LatestModuleEnvelopeRevision(ctx)
			return err
		})
		if err != nil {
			return err
		}
		var witnesses []business.ModuleInstallationPrerequisite
		for _, m := range plan.Review.Modules {
			if !m.Required {
				continue
			}
			if m.ReuseInstallationId == "" {
				return fmt.Errorf("conformance module %s has no separately approved consent", m.Principal)
			}
			w, err := store.ModuleInstallationPrerequisite(ctx, inst.Organization, m.ReuseInstallationId, envelope)
			if err != nil {
				return err
			}
			witnesses = append(witnesses, w)
		}
		if err := store.RecordSolutionPrerequisites(ctx, inst, witnesses); err != nil {
			return err
		}
		jobID, err := store.EnqueueCanonicalInstallationStep(ctx, inst, false)
		if err != nil {
			return err
		}
		current, err := worker.GetJob(ctx, &jobsv1.GetJobRequest{JobId: jobID})
		if err != nil {
			return err
		}
		if current.GetJob().GetState() == jobsv1.JobState_JOB_STATE_SUCCEEDED {
			return nil
		}
		jobs, err := worker.Claim(ctx, &jobsv1.ClaimJobsRequest{Queue: business.InstallationStepQueue, WorkerId: "conformance-setup", Limit: 1, LeaseDuration: durationpb.New(business.InstallationStepLease)})
		if err != nil {
			return err
		}
		if len(jobs.Jobs) != 1 || jobs.Jobs[0].Id != jobID {
			return fmt.Errorf("conformance setup claimed a different attempt")
		}
		return worker.Complete(ctx, &jobsv1.CompleteJobRequest{Lease: &jobsv1.JobLeaseReference{JobId: jobID, WorkerId: "conformance-setup", LeaseToken: jobs.Jobs[0].Lease.Token}})
	}
}

// The fixture administrator reviews; the separately named owner may be a
// member. This never signs in as, or borrows authority from, that owner.
func installConformanceSolution(ctx context.Context, svc *business.Service, core *authority.Service, target, role, owner string) (domain.Installation, error) {
	caller := authority.Person{ID: conformanceAlex, Organization: fixtureOrganization}
	approval, err := core.Review(ctx, caller, fixtureOrganization, target)
	if err != nil {
		return domain.Installation{}, err
	}
	approval.OperationID = business.NewIDString()
	return svc.InstallAuthoritySolution(ctx, caller, fixtureOrganization, target, role, owner, &approval)
}
