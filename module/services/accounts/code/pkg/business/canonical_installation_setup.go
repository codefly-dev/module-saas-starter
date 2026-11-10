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
func (s *Service) RetryCanonicalSolution(ctx context.Context, caller authority.Person, org, id string) (InstalledSolution, error) {
	core, err := s.AuthorityCore()
	if err != nil {
		return InstalledSolution{}, err
	}
	all, err := core.Installations(ctx, caller, org)
	if err != nil {
		return InstalledSolution{}, err
	}
	for _, inst := range all {
		if inst.ID != id {
			continue
		}
		if inst.Status != domain.Pending && inst.Status != domain.Active {
			return InstalledSolution{Installation: inst}, domain.NotNow("INSTALLATION_REVIEW_REQUIRED: this installation is no longer pending")
		}
		store, ok := s.store.(CanonicalInstallationSetupStore)
		if !ok {
			return InstalledSolution{Installation: inst}, domain.NotNow("canonical installation setup store unavailable")
		}
		plan, err := store.SolutionInstallationPlan(ctx, org, id)
		if err != nil {
			return InstalledSolution{Installation: inst}, err
		}
		return s.continueCanonicalSolution(ctx, core, caller, inst, plan)
	}
	return InstalledSolution{}, domain.Missing("installation %q is not present in this organization", id)
}

func (s *Service) continueCanonicalSolution(ctx context.Context, core *authority.Service, caller authority.Person, inst domain.Installation, plan SolutionInstallationPlan) (InstalledSolution, error) {
	if inst.Status == domain.Pending {
		ctx = context.WithValue(ctx, retryCanonicalSetupKey{}, true)
	}
	step, err := s.installStepFor(ctx, core, plan.Target)
	if err != nil {
		return InstalledSolution{}, err
	}
	// The access the attempt recorded is applied once it is active, whoever
	// resumes it; a browser's choices since play no part.
	inst, err = core.Install(ctx, caller, inst.Organization, plan.Target, plan.RequestedRole, plan.RequestedOwner, &plan.Approval, step)
	if err != nil {
		return InstalledSolution{Installation: inst}, err
	}
	return InstalledSolution{Installation: inst, AccessProblems: s.applyInstallationAccess(ctx, caller, inst, plan.Access)}, nil
}

func (s *Service) canonicalInstallationStep(ctx context.Context, inst domain.Installation) error {
	store, ok := s.store.(CanonicalInstallationSetupStore)
	if !ok || s.jobOperations == nil {
		return domain.NotNow("INSTALLATION_SETUP_UNAVAILABLE: the host setup queue is not configured")
	}
	var upgrade bool
	if bundle, ok := s.store.(solutionBundleStore); ok {
		var err error
		if upgrade, err = s.convergeReviewedModules(ctx, inst, bundle); err != nil {
			return err
		}
	}
	if upgrade {
		// The closure of the upgraded review is converged; the solution's own
		// install step is not run again here. The durable setup job and its
		// receiver are fenced to the pending attempt the row records, and an
		// upgrade has none: the installation stays active and the core moves
		// its consent once this returns.
		return nil
	}
	retry, _ := ctx.Value(retryCanonicalSetupKey{}).(bool)
	jobID, err := store.EnqueueCanonicalInstallationStep(ctx, inst, retry)
	if err != nil {
		return err
	}
	if noWait, _ := ctx.Value(noSetupWaitKey{}).(bool); noWait {
		// One read, no wait: what the setup answered so far decides, and
		// anything still running is pending at once; the caller's next pass
		// resumes the same attempt.
		result, err := s.jobOperations.GetJob(ctx, &jobsv1.GetJobRequest{JobId: jobID})
		if err != nil {
			return fmt.Errorf("installation setup result unavailable: %w", err)
		}
		if done, err := setupAnswered(result, jobID, inst); done {
			return err
		}
		return domain.NotNow("INSTALLATION_SETUP_PENDING: setup has not acknowledged yet; the next pass resumes the same approved operation")
	}
	// This is a bounded wait on the existing durable worker, not a second
	// delivery loop. Cancellation leaves the same canonical attempt pending.
	wait, cancel := context.WithTimeout(ctx, InstallationStepTimeout)
	defer cancel()
	pending := domain.NotNow("INSTALLATION_SETUP_PENDING: setup has not acknowledged within %s; retry the same approved operation", InstallationStepTimeout)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		result, err := s.jobOperations.GetJob(wait, &jobsv1.GetJobRequest{JobId: jobID})
		if err != nil {
			if wait.Err() != nil && ctx.Err() == nil {
				// The bounded wait ended inside the read: setup is still
				// pending, not its result unavailable.
				return pending
			}
			return fmt.Errorf("installation setup result unavailable: %w", err)
		}
		if done, err := setupAnswered(result, jobID, inst); done {
			return err
		}
		select {
		case <-wait.Done():
			return pending
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

// setupAnswered reads one result of the setup job jobID of inst: done when the
// setup succeeded (nil: only core.Install's final transaction may activate),
// failed, was canceled, or answers for another attempt; not done while it
// still runs.
func setupAnswered(result *jobsv1.GetJobResponse, jobID string, inst domain.Installation) (bool, error) {
	job := result.GetJob()
	if job.GetId() != jobID || job.GetScope().GetOrganizationId() != inst.Organization ||
		job.GetQueue() != InstallationStepQueue || job.GetSource() != "saas.installation" || job.GetTopic() != "installation.step" {
		return true, domain.NotNow("INSTALLATION_SETUP_CHANGED: the result does not belong to this setup attempt")
	}
	switch job.GetState() {
	case jobsv1.JobState_JOB_STATE_SUCCEEDED:
		return true, nil
	case jobsv1.JobState_JOB_STATE_DEAD_LETTER:
		return true, domain.NotNow("INSTALLATION_SETUP_FAILED: %s", BoundedInstallationReason(job.GetLastFailure().GetMessage()))
	case jobsv1.JobState_JOB_STATE_CANCELED:
		return true, domain.NotNow("INSTALLATION_SETUP_CHANGED: the setup attempt was canceled; review again")
	}
	return false, nil
}

type noSetupWaitKey struct{}

// WithoutSetupWait marks ctx for work that runs inside a delivery pass (a
// fixture's declared installations): an install step there never waits on
// the solution's setup. It reads the setup's answer once and is pending at
// once otherwise; the next pass resumes the same attempt. An interactive
// install keeps the bounded wait.
func WithoutSetupWait(ctx context.Context) context.Context {
	return context.WithValue(ctx, noSetupWaitKey{}, true)
}
