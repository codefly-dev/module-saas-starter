package business

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"
	"unicode/utf8"

	gen "accounts/pkg/gen/saas/accounts/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type InstallationRetryStore interface {
	RetryInstallation(context.Context, string, string) (*gen.Installation, uint64, error)
}

// RetryInstallation preserves the consent revision. State, outbox and audit
// commit together; a repeated request while pending is a conflict.
func (s *Service) RetryInstallation(ctx context.Context, actorID, orgID, id string) (*gen.Installation, error) {
	store, ok := s.store.(InstallationRetryStore)
	if !ok {
		return nil, errors.New("installation retry store unavailable")
	}
	actorType := s.actorTypeForCreator(ctx, actorID)
	var installation *gen.Installation
	// The target lock must precede the installation lock, as on withdrawal.
	// The control-plane transaction can read the target; every write names orgID.
	err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		var revision uint64
		installation, revision, err = store.RetryInstallation(ctx, orgID, id)
		if err != nil {
			return err
		}
		return s.emitTx(ctx, actorID, actorType, EventInstallationRetried, "installation", id, orgID, map[string]any{"revision": fmt.Sprint(revision)})
	})
	return installation, err
}

// InstallationStepAuthority is resolved under locks for the current leased run.
// StandingPermissions is the agent's role at its own root, never the owner's RBAC.
type InstallationStepAuthority struct {
	InstallationStep
	TargetID, RouteAlias, BindingID, PrincipalID, RootID string
	Scope                                                InstallationScope
	Facts                                                *InstallationAuthorityFacts
	StandingPermissions                                  []WorkContextPermission
}

type InstallationStepStore interface {
	WithInstallationStepAuthority(context.Context, *jobsv1.JobLeaseReference, uint64, func(context.Context, *InstallationStepAuthority) error) error
	InstallationStepLeaseValid(context.Context, *jobsv1.JobLeaseReference) (bool, error)
}

// WithInstallationStep checks delivered authority as well as consent. The
// callback signs while the target and installation locks still fence changes.
func (s *Service) WithInstallationStep(ctx context.Context, lease *jobsv1.JobLeaseReference, mint func(context.Context, *InstallationStepAuthority, []*gen.WorkContextScope) error) error {
	store, ok := s.store.(InstallationStepStore)
	if !ok {
		return errors.New("installation step store unavailable")
	}
	snapshot := s.moduleRegistrySnapshot()
	if err := s.fenceModuleRegistrySnapshot(ctx, snapshot); err != nil {
		return err
	}
	return store.WithInstallationStepAuthority(ctx, lease, snapshot.envelopeRevision, func(ctx context.Context, a *InstallationStepAuthority) error {
		grant, ok := snapshot.registry[a.PrincipalID]
		if !ok || a.BindingID != grant.PresenceBinding || !moduleAvailableToOrganization(grant, a.OrganizationID) {
			return ErrModuleInstallationInactive
		}
		a.Scope = FullInstallationScope(intersectInstallationScope(grant, a.Scope))
		if err := mint(ctx, a, installationStepScopes(a)); err != nil {
			return err
		}
		if s.moduleRegistrySnapshot() != snapshot {
			return ErrModuleRegistryStale
		}
		return nil
	})
}

// A setup token is always confined to this installation's root. Only concrete
// actions in delivered operation/read bindings, consent, the agent ceiling and
// its standing role survive. Empty authority is valid for a no-op install.
func installationStepScopes(a *InstallationStepAuthority) []*gen.WorkContextScope {
	byKind := map[string][]string{}
	add := func(scope ModuleOperationScope) {
		if len(scope.ResourceIDs) > 0 && !slices.Contains(scope.ResourceIDs, a.RootID) {
			return
		}
		if len(a.Facts.Actor.AllowedScopes) > 0 && !slices.Contains(a.Facts.Actor.AllowedScopes, scope.ResourceKind) {
			return
		}
		for _, action := range scope.Actions {
			if action == "*" || scope.ResourceKind == "*" {
				continue
			}
			for _, p := range a.StandingPermissions {
				if (p.ResourceKind == "*" || p.ResourceKind == scope.ResourceKind) && (p.Action == "*" || p.Action == action) {
					byKind[scope.ResourceKind] = append(byKind[scope.ResourceKind], action)
					break
				}
			}
		}
	}
	for _, binding := range a.Scope.OperationAudiences {
		for _, scope := range binding.InvokeScopes {
			add(scope)
		}
	}
	for _, binding := range a.Scope.ReadAudiences {
		for _, scope := range readOperationScopes(binding.Scopes) {
			add(scope)
		}
	}
	var result []*gen.WorkContextScope
	for kind, actions := range byKind {
		slices.Sort(actions)
		result = append(result, &gen.WorkContextScope{ResourceKind: kind, Actions: slices.Compact(actions), ResourceIds: []string{a.RootID}})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ResourceKind < result[j].ResourceKind })
	return result
}

func (s *Service) ValidateInstallationStepLease(ctx context.Context, lease *jobsv1.JobLeaseReference) error {
	store, ok := s.store.(InstallationStepStore)
	if !ok {
		return errors.New("installation step store unavailable")
	}
	valid, err := store.InstallationStepLeaseValid(ctx, lease)
	if err != nil {
		return err
	}
	if !valid {
		return errors.New("installation step lease is no longer current")
	}
	return nil
}

const (
	InstallationStepQueue       = "installation-step"
	InstallationStepMaxAttempts = 5
	InstallationStepLease       = time.Minute
	InstallationStepTimeout     = 20 * time.Second
	InstallationFailureLimit    = 512
	InstallationStepFailure     = "The installation step could not be completed after repeated attempts."
)

// InstallationStep identifies consent, never a caller-selected upstream. Scope
// is resolved afresh for every attempt. Tokens are never stored in job payloads.
type InstallationStep struct {
	InstallationID string `json:"installation_id"`
	OrganizationID string `json:"organization_id"`
	Revision       uint64 `json:"revision"`
}

// NewInstallationStepJob gives each operator retry its own durable run while
// keeping the solution's idempotency identity (installation, revision) stable.
// Ordering serializes revisions and retries of one installation.
func NewInstallationStepJob(step InstallationStep, run string) (*jobsv1.EnqueueJobRequest, error) {
	payload, err := json.Marshal(step)
	if err != nil {
		return nil, err
	}
	req := &jobsv1.EnqueueJobRequest{Job: &jobsv1.NewJob{
		Direction: jobsv1.JobDirection_JOB_DIRECTION_OUTBOX,
		Scope:     &jobsv1.JobScope{Value: &jobsv1.JobScope_OrganizationId{OrganizationId: step.OrganizationID}},
		Queue:     InstallationStepQueue, Topic: "installation.step", Source: "saas.installation",
		IdempotencyKey: fmt.Sprintf("%s:%d:%s", step.InstallationID, step.Revision, run),
		Ordering:       &jobsv1.JobOrderingKey{Namespace: "installation", Components: []string{step.InstallationID}},
		SchemaVersion:  1, Payload: payload, ContentType: "application/json",
		MaxAttempts: InstallationStepMaxAttempts,
		Attributes: map[string]string{
			"installation_step.id":          step.InstallationID,
			"installation_step.revision":    fmt.Sprint(step.Revision),
			"codefly.installation_id":       step.InstallationID,
			"codefly.organization_id":       step.OrganizationID,
			"codefly.installation_revision": fmt.Sprint(step.Revision),
		},
	}}
	if err := jobs.ValidateCommand(req); err != nil {
		return nil, err
	}
	return req, nil
}

func InstallationStepRetryDelay(attempt uint32) time.Duration {
	if attempt == 0 {
		attempt = 1
	}
	if attempt > InstallationStepMaxAttempts {
		attempt = InstallationStepMaxAttempts
	}
	return 5 * time.Second * time.Duration(1<<(attempt-1))
}

// BoundedInstallationReason accepts only the solution's explicit refusal field.
// Invalid/empty diagnostics become a fixed message; arbitrary transport errors
// never enter installation records or job history.
func BoundedInstallationReason(reason string) string {
	if !utf8.ValidString(reason) || reason == "" {
		return "The solution refused the installation step."
	}
	for len(reason) > InstallationFailureLimit {
		_, size := utf8.DecodeLastRuneInString(reason)
		reason = reason[:len(reason)-size]
	}
	return reason
}

// ClaimInstallationStep uses the existing isolated job store. A gateway replica
// takes one lease at a time and bounds the workload call below that lease.
func (s *Service) ClaimInstallationStep(ctx context.Context, worker string) (*jobsv1.JobEnvelope, error) {
	if s.moduleJobStore == nil {
		return nil, errors.New("installation job store unavailable")
	}
	result, err := s.moduleJobStore.Claim(ctx, &jobsv1.ClaimJobsRequest{
		Queue: InstallationStepQueue, WorkerId: worker, Limit: 1,
		LeaseDuration: durationpb.New(InstallationStepLease),
	})
	if err != nil || len(result.GetJobs()) == 0 {
		return nil, err
	}
	return result.Jobs[0], nil
}

// FinishInstallationStep delegates transitions, lease fencing and exhaustion to
// the common job system. The migration projects each transition onto the current
// installation atomically with the job attempt, including expired final leases.
func (s *Service) FinishInstallationStep(ctx context.Context, lease *jobsv1.JobLeaseReference, attempt uint32, status int, reason string) error {
	if s.moduleJobStore == nil {
		return errors.New("installation job store unavailable")
	}
	switch status {
	case 204:
		return s.moduleJobStore.Complete(ctx, &jobsv1.CompleteJobRequest{Lease: lease})
	case 409:
		return s.moduleJobStore.DeadLetter(ctx, &jobsv1.DeadLetterJobRequest{Lease: lease,
			Failure: &jobsv1.JobFailure{Code: "installation.refused", Message: BoundedInstallationReason(reason)}})
	default:
		_, err := s.moduleJobStore.Retry(ctx, &jobsv1.RetryJobRequest{Lease: lease,
			Failure: &jobsv1.JobFailure{Code: "installation.unavailable", Message: InstallationStepFailure},
			RetryAt: timestamppb.New(time.Now().Add(InstallationStepRetryDelay(attempt))),
		})
		return err
	}
}
