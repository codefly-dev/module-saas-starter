package adapters

import (
	"accounts/pkg/business"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
)

// The queue response retains immutable host routing separately from Authority's
// principal. Its receiver body has exactly three fields, and no scope grant.
func canonicalInstallationClaim(delivery business.CanonicalInstallationDelivery, job *jobsv1.JobEnvelope, lease *jobsv1.JobLeaseReference) map[string]any {
	p := delivery.Plan
	return map[string]any{"job_id": lease.JobId, "worker": lease.WorkerId, "lease_token": lease.LeaseToken, "attempt": job.GetAttemptCount(),
		"route_alias": p.Target, "target_id": p.HostTargetID, "binding_id": p.BindingID, "work_context": delivery.Token,
		"installation": business.InstallationStep{InstallationID: p.InstallationID, OrganizationID: p.Organization, Revision: p.Revision}}
}
