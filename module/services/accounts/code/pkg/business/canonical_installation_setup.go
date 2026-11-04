package business

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
)

// CanonicalInstallationSetupStore keeps a job link with the canonical consent,
// never a public.installations solution row or a second lifecycle state.
type CanonicalInstallationSetupStore interface {
	SolutionInstallationPlan(context.Context, string, string) (SolutionInstallationPlan, error)
	EnqueueCanonicalInstallationStep(context.Context, domain.Installation, bool) (string, error)
}

type retryCanonicalSetupKey struct{}

// RetryCanonicalSolution preserves the approved operation and its exact owner
// input. Core reauthorizes the caller and checks stale consent BEFORE the step
// may replay a dead-lettered job. Another administrator must review afresh; this
// method never impersonates the original approving person.
func (s *Service) RetryCanonicalSolution(ctx context.Context, caller authority.Person, org, id string) (domain.Installation, error) {
	core, err := s.AuthorityCore()
	if err != nil {
		return domain.Installation{}, err
	}
	all, err := core.Installations(ctx, caller, org)
	if err != nil {
		return domain.Installation{}, err
	}
	for _, inst := range all {
		if inst.ID != id {
			continue
		}
		if inst.Status != domain.Pending && inst.Status != domain.Active {
			return inst, domain.NotNow("INSTALLATION_REVIEW_REQUIRED: this installation is no longer pending")
		}
		store, ok := s.store.(CanonicalInstallationSetupStore)
		if !ok {
			return inst, domain.NotNow("canonical installation setup store unavailable")
		}
		plan, err := store.SolutionInstallationPlan(ctx, org, id)
		if err != nil {
			return inst, err
		}
		return s.continueCanonicalSolution(ctx, core, caller, inst, plan)
	}
	return domain.Installation{}, domain.Missing("installation %q is not present in this organization", id)
}

func (s *Service) continueCanonicalSolution(ctx context.Context, core *authority.Service, caller authority.Person, inst domain.Installation, plan SolutionInstallationPlan) (domain.Installation, error) {
	if inst.Status == domain.Pending {
		ctx = context.WithValue(ctx, retryCanonicalSetupKey{}, true)
	}
	return core.Install(ctx, caller, inst.Organization, plan.Target, plan.RequestedRole, plan.RequestedOwner, &plan.Approval, s.AuthorityInstallStep())
}

func (s *Service) canonicalInstallationStep(ctx context.Context, inst domain.Installation) error {
	store, ok := s.store.(CanonicalInstallationSetupStore)
	if !ok || s.jobOperations == nil {
		return domain.NotNow("INSTALLATION_SETUP_UNAVAILABLE: the host setup queue is not configured")
	}
	if bundle, ok := s.store.(solutionBundleStore); ok {
		if err := s.convergeReviewedModules(ctx, inst, bundle); err != nil {
			return err
		}
	}
	retry, _ := ctx.Value(retryCanonicalSetupKey{}).(bool)
	jobID, err := store.EnqueueCanonicalInstallationStep(ctx, inst, retry)
	if err != nil {
		return err
	}
	// This is a bounded wait on the existing durable worker, not a second
	// delivery loop. Cancellation leaves the same canonical attempt pending.
	wait, cancel := context.WithTimeout(ctx, InstallationStepTimeout)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		result, err := s.jobOperations.GetJob(wait, &jobsv1.GetJobRequest{JobId: jobID})
		if err != nil {
			return fmt.Errorf("installation setup result unavailable: %w", err)
		}
		job := result.GetJob()
		if job.GetId() != jobID || job.GetScope().GetOrganizationId() != inst.Organization ||
			job.GetQueue() != InstallationStepQueue || job.GetSource() != "saas.installation" || job.GetTopic() != "installation.step" {
			return domain.NotNow("INSTALLATION_SETUP_CHANGED: the result does not belong to this setup attempt")
		}
		switch result.GetJob().GetState() {
		case jobsv1.JobState_JOB_STATE_SUCCEEDED:
			return nil // Only core.Install's final transaction may activate.
		case jobsv1.JobState_JOB_STATE_DEAD_LETTER:
			return domain.NotNow("INSTALLATION_SETUP_FAILED: %s", BoundedInstallationReason(result.GetJob().GetLastFailure().GetMessage()))
		case jobsv1.JobState_JOB_STATE_CANCELED:
			return domain.NotNow("INSTALLATION_SETUP_CHANGED: the setup attempt was canceled; review again")
		}
		select {
		case <-wait.Done():
			return domain.NotNow("INSTALLATION_SETUP_PENDING: setup has not acknowledged yet; retry the same approved operation")
		case <-ticker.C:
		}
	}
}

// CanonicalInstallationDelivery contains only host-resolved routing and the
// strict setup body. Token is fresh for this lease and is never stored.
type CanonicalInstallationDelivery struct {
	Plan  SolutionInstallationPlan
	Token string
}

func (s *Service) WithCanonicalInstallationStep(ctx context.Context, job *jobsv1.JobEnvelope, lease *jobsv1.JobLeaseReference, use func(CanonicalInstallationDelivery) error) error {
	if job.GetAttributes()["canonical.installation"] != "true" || job.GetId() != lease.GetJobId() {
		return domain.Refuse("not a canonical installation setup job")
	}
	if err := s.ValidateInstallationStepLease(ctx, lease); err != nil {
		return err
	}
	var step InstallationStep
	if err := json.Unmarshal(job.GetPayload(), &step); err != nil {
		return err
	}
	store, ok := s.store.(CanonicalInstallationSetupStore)
	if !ok {
		return domain.NotNow("canonical installation setup store unavailable")
	}
	plan, err := store.SolutionInstallationPlan(ctx, step.OrganizationID, step.InstallationID)
	if err != nil {
		return err
	}
	if plan.SetupJobID != lease.GetJobId() || plan.Revision != step.Revision || plan.Receipt.OperationID != job.GetAttributes()["canonical.operation"] || plan.Receipt.Intent != job.GetAttributes()["canonical.intent"] {
		return domain.NotNow("canonical installation setup attempt changed")
	}
	core, err := s.AuthorityCore()
	if err != nil {
		return err
	}
	remaining := time.Until(job.GetLease().GetExpiresAt().AsTime())
	issued, err := core.IssueInstallationStep(ctx, authority.InstallationStepAttempt{
		InstallationID: plan.InstallationID, Organization: plan.Organization, Target: plan.Target,
		TargetID: plan.Approval.TargetID, Revision: plan.Revision, Generation: plan.Approval.Generation,
		Consent: plan.Receipt, Digest: plan.Approval.Digest,
	}, min(remaining, authority.MaxInstallationStepTTL))
	if err != nil {
		return err
	}
	if err := s.ValidateInstallationStepLease(ctx, lease); err != nil {
		return err
	}
	return use(CanonicalInstallationDelivery{Plan: plan, Token: issued.Token})
}
