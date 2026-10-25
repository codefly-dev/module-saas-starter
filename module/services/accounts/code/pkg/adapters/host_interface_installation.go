package adapters

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// forInstallation is an installation acting for itself (the interface's
// InstallationRef). An installation id is not a credential: the actor must be a
// workload of the installed solution, and the installation it names must be
// that solution's own active installation in the organisation the work runs
// in. Every refusal comes before the mint and names its reason; the authority
// is then the installation's own, through the host's headless mint.
func (s *HostAuthorityServer) forInstallation(ctx context.Context, installationID string, req *authorityv1.IssueRequest, scopes []*gen.WorkContextScope) (*gen.IssuedWorkContext, error) {
	if installationID == "" {
		return nil, status.Error(codes.InvalidArgument, "an installation's issue names the installation acting")
	}
	credential := req.GetActor().GetCredential()
	if credential == "" {
		return nil, status.Errorf(codes.Unauthenticated, "installation %s acts for itself through its solution's workload, and the issue carries no actor credential", installationID)
	}
	prefix, err := service.ModulePrefixOfWorkload(ctx, credential)
	if err != nil {
		if mapped := moduleIdentityError(err, codes.PermissionDenied, "actor credential refused"); mapped != nil {
			return nil, mapped
		}
		return nil, status.Error(codes.PermissionDenied, "actor credential refused")
	}
	org, err := installationOrganization(prefix, req.GetOrganizationId())
	if err != nil {
		return nil, err
	}
	own, err := s.workContexts.seals.WorkContextAudienceInstallation(ctx, org, prefix)
	switch {
	case errors.Is(err, business.ErrWorkContextNoAudienceInstallation):
		return nil, status.Errorf(codes.PermissionDenied, "%s has no active installation in organization %s, so installation %s does not act for it", prefix, org, installationID)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return nil, status.FromContextError(err).Err()
	case err != nil:
		return nil, status.Errorf(codes.Unavailable, "cannot resolve %s's installation in organization %s", prefix, org)
	}
	if own != installationID {
		return nil, status.Errorf(codes.PermissionDenied, "installation %s is not %s's active installation in organization %s; an installation acts only for itself", installationID, prefix, org)
	}
	mint := &gen.StartInstallationTaskRequest{
		OrgId: org, InstallationId: installationID, TaskId: uuid.NewString(),
		Audience: req.GetAudience(), AuthorityScopes: scopes, TtlSeconds: int32(req.GetTtlSeconds()),
	}
	if len(scopes) == 0 {
		// An empty ask is everything the installation holds at the audience.
		if mint.AuthorityScopes, err = s.workContexts.installationHoldings(ctx, org, installationID, req.GetAudience()); err != nil {
			return nil, err
		}
	}
	if err := Validate(mint); err != nil {
		return nil, err
	}
	return s.workContexts.startInstallationTask(ctx, mint)
}

// installationOrganization is the organisation an installation's work runs in:
// the one the request names, held to the solution's own tenant when the
// composition pins one, or that tenant when the request names none.
func installationOrganization(prefix, named string) (string, error) {
	grant := service.ModulePrincipals()[business.ModulePrincipalID(prefix)]
	switch {
	case named == "" && grant.Tenant == "":
		return "", status.Errorf(codes.InvalidArgument, "%s is not pinned to one organization, so its installation's issue names the organization", prefix)
	case named == "":
		return grant.Tenant, nil
	case grant.Tenant != "" && named != grant.Tenant && !grant.CrossTenant:
		return "", status.Errorf(codes.PermissionDenied, "%s acts in organization %s, not %s", prefix, grant.Tenant, named)
	}
	return named, nil
}

// installationHoldings is what an installation's empty ask means: every
// (kind, action) the composition names at the audience that the installation's
// own authority admits, by the check its mint makes. Holding nothing is refused.
func (s *WorkContextAuthorityServer) installationHoldings(ctx context.Context, orgID, installationID, audience string) ([]*gen.WorkContextScope, error) {
	kinds, err := service.ModuleContentResources(audience)
	if err != nil {
		return nil, status.Errorf(codes.PermissionDenied, "installation %s holds nothing at %s: %v", installationID, audience, err)
	}
	var held []*gen.WorkContextScope
	for _, kind := range kinds {
		var actions []string
		for _, action := range candidateActions(kind, audience, nil) {
			permissions, _, err := workContextScopes([]*gen.WorkContextScope{{ResourceKind: kind, Actions: []string{action}}}, mintContentReads(audience))
			if err != nil {
				return nil, status.Error(codes.Internal, err.Error())
			}
			_, err = s.resolveInstallationAuthority(ctx, orgID, installationID, permissions)
			switch status.Code(err) {
			case codes.OK:
				actions = append(actions, action)
			case codes.PermissionDenied:
			default:
				return nil, err
			}
		}
		if len(actions) > 0 {
			held = append(held, &gen.WorkContextScope{ResourceKind: kind, Actions: actions})
		}
	}
	if len(held) == 0 {
		return nil, status.Errorf(codes.PermissionDenied, "installation %s holds nothing at %s", installationID, audience)
	}
	return held, nil
}
