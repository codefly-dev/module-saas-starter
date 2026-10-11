package adapters

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"
)

// This private REST endpoint is intentionally absent from the gateway's public
// route catalog and from the module authority listener. It serves one queue.
const InstallationStepPath = "/internal/installation-steps/"

type installationStepService interface {
	ClaimInstallationStep(context.Context, string) (*jobsv1.JobEnvelope, error)
	WithInstallationStep(context.Context, *jobsv1.JobLeaseReference, func(context.Context, *business.InstallationStepAuthority, []*gen.WorkContextScope) error) error
	ValidateInstallationStepLease(context.Context, *jobsv1.JobLeaseReference) error
	FinishInstallationStep(context.Context, *jobsv1.JobLeaseReference, uint32, int, string) error
}

type installationWireScope struct {
	ResourceKind string   `json:"resourceKind"`
	Actions      []string `json:"actions"`
	ResourceIDs  []string `json:"resourceIds"`
}

func NewInstallationStepHandler(svc *business.Service) http.Handler {
	return installationStepHandler(svc, func(ctx context.Context, a *business.InstallationStepAuthority, scopes []*gen.WorkContextScope, lease *jobsv1.JobLeaseReference) (string, error) {
		s := WorkContextSingleton()
		if s == nil || s.configureErr != nil || s.signer == nil {
			return "", business.ErrModuleInstallationInactive
		}
		audience := "solution:" + a.BindingID
		// The leased read has already established this exact live binding.
		if a.Facts == nil || a.Facts.Actor == nil || (len(a.Facts.Actor.AllowedAudiences) > 0 && !slices.Contains(a.Facts.Actor.AllowedAudiences, audience)) {
			return "", business.ErrModuleInstallationInactive
		}
		_, authority, err := workContextScopes(scopes, noContentReads)
		if err != nil {
			return "", err
		}
		issued, err := s.issueInstallationTask(ctx, &gen.StartInstallationTaskRequest{
			OrgId: a.OrganizationID, InstallationId: a.InstallationID, TaskId: a.InstallationID,
			SessionId: lease.GetLeaseToken(), Audience: audience, TtlSeconds: int32(business.InstallationStepLease.Seconds()),
		}, a.Facts, authority)
		if err != nil {
			return "", err
		}
		return issued.GetToken(), nil
	})
}

func installationStepHandler(svc installationStepService, mint func(context.Context, *business.InstallationStepAuthority, []*gen.WorkContextScope, *jobsv1.JobLeaseReference) (string, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			installerError(w, 405, "POST required")
			return
		}
		if values := r.Header.Values("X-Codefly-Internal-Token"); len(values) != 1 || !validInternalToken(values[0]) {
			installerError(w, 401, "internal credential required")
			return
		}
		switch strings.TrimPrefix(r.URL.Path, InstallationStepPath) {
		case "claim":
			var req struct {
				Worker string `json:"worker"`
			}
			if !installerDecode(w, r, &req) {
				return
			}
			if req.Worker == "" || len(req.Worker) > 128 {
				installerError(w, 400, "worker required")
				return
			}
			job, err := svc.ClaimInstallationStep(r.Context(), req.Worker)
			if err != nil {
				installerError(w, 503, "installation queue unavailable")
				return
			}
			if job == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			lease := &jobsv1.JobLeaseReference{JobId: job.GetId(), WorkerId: req.Worker, LeaseToken: job.GetLease().GetToken()}
			var result map[string]any
			err = svc.WithInstallationStep(r.Context(), lease, func(ctx context.Context, a *business.InstallationStepAuthority, scopes []*gen.WorkContextScope) error {
				token, err := mint(ctx, a, scopes, lease)
				if err != nil {
					return err
				}
				wire := make([]installationWireScope, 0, len(scopes))
				for _, s := range scopes {
					wire = append(wire, installationWireScope{s.GetResourceKind(), s.GetActions(), s.GetResourceIds()})
				}
				result = map[string]any{"job_id": lease.JobId, "worker": lease.WorkerId, "lease_token": lease.LeaseToken, "attempt": job.GetAttemptCount(),
					"route_alias": a.RouteAlias, "target_id": a.TargetID, "binding_id": a.BindingID, "work_context": token,
					"installation": map[string]any{"installation_id": a.InstallationID, "organization_id": a.OrganizationID, "revision": a.Revision, "scope": wire}}
				return nil
			})
			if err != nil {
				// An unavailable mint consumes this attempt just like an unavailable
				// solution. If finalization itself fails, lease expiry recovers it.
				_ = svc.FinishInstallationStep(r.Context(), lease, job.GetAttemptCount(), 503, "")
				installerError(w, 503, "installation authority unavailable")
				return
			}
			installerJSON(w, 200, result)
		case "finish":
			var req struct {
				JobID   string `json:"job_id"`
				Worker  string `json:"worker"`
				Token   string `json:"lease_token"`
				Attempt uint32 `json:"attempt"`
				Status  int    `json:"status"`
				Reason  string `json:"reason"`
			}
			if !installerDecode(w, r, &req) {
				return
			}
			lease := &jobsv1.JobLeaseReference{JobId: req.JobID, WorkerId: req.Worker, LeaseToken: req.Token}
			if err := jobs.ValidateCommand(&jobsv1.CompleteJobRequest{Lease: lease}); err != nil {
				installerError(w, 400, "invalid lease")
				return
			}
			if err := svc.ValidateInstallationStepLease(r.Context(), lease); err != nil {
				installerError(w, 409, "installation lease expired")
				return
			}
			if err := svc.FinishInstallationStep(r.Context(), lease, req.Attempt, req.Status, req.Reason); err != nil {
				installerError(w, 503, "installation outcome unavailable")
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			installerError(w, 404, "unknown installation operation")
		}
	})
}
