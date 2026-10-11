package jobs

import (
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"context"
	"google.golang.org/protobuf/proto"
	"strconv"
	"strings"
)

// ModuleAuthority is host-stamped provenance. It is not part of the module's
// enqueue request and a supplied attribute with this reserved prefix is removed.
type ModuleAuthority struct {
	PrincipalID, OrganizationID, InstallationID, BindingID string
	InstallationRevision                                   uint64
	// AllowsGlobal comes only from the live envelope, never from job attributes.
	AllowsGlobal bool
}

func (a ModuleAuthority) Attributes() map[string]string {
	return map[string]string{
		"codefly.principal_id": a.PrincipalID, "codefly.organization_id": a.OrganizationID,
		"codefly.installation_id": a.InstallationID, "codefly.installation_revision": strconv.FormatUint(a.InstallationRevision, 10),
		"codefly.binding_id": a.BindingID,
	}
}
func StampModuleJob(req *jobsv1.EnqueueJobRequest, authority ModuleAuthority) *jobsv1.EnqueueJobRequest {
	out := proto.Clone(req).(*jobsv1.EnqueueJobRequest)
	attrs := out.Job.Attributes
	if attrs == nil {
		attrs = map[string]string{}
	}
	for key := range attrs {
		if strings.HasPrefix(key, "codefly.") {
			delete(attrs, key)
		}
	}
	for key, value := range authority.Attributes() {
		attrs[key] = value
	}
	out.Job.Attributes = attrs
	return out
}

type moduleClaimKey struct{}

func WithModuleClaim(ctx context.Context, authority ModuleAuthority) context.Context {
	return context.WithValue(ctx, moduleClaimKey{}, authority)
}
func ModuleClaim(ctx context.Context) (ModuleAuthority, bool) {
	a, ok := ctx.Value(moduleClaimKey{}).(ModuleAuthority)
	return a, ok
}
