package adapters

import (
	"context"
	"errors"
	"slices"

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
// in. The caller is proven first, so one proving nothing learns nothing of
// what it asked, not even whether the ask is well formed; only then is the
// installation held to the solution's own, and the audience to the
// composition. Every refusal comes before the mint and names its reason; the
// authority is then the installation's own, through the host's headless mint.
func (s *HostAuthorityServer) forInstallation(ctx context.Context, installationID string, req *authorityv1.IssueRequest, scopes []*gen.WorkContextScope, ttl int32) (*gen.IssuedWorkContext, error) {
	credential := req.GetActor().GetCredential()
	if credential == "" {
		return nil, status.Error(codes.Unauthenticated, "an installation acts for itself through its solution's workload, and the issue carries no actor credential")
	}
	prefix, err := service.ModulePrefixOfWorkload(ctx, credential)
	if err != nil {
		return nil, credentialRefusal(err, "actor")
	}
	if installationID == "" {
		return nil, status.Error(codes.InvalidArgument, "an installation's issue names the installation acting")
	}
	// The installation is read for whose it is, its organization included:
	// the request names the installation, never the organization it runs in.
	installation, err := s.workContexts.authority.WorkContextInstallationIdentity(ctx, installationID)
	var missing *business.StoreError
	switch {
	case errors.As(err, &missing) && missing.StoreErrorType == business.ErrTypeNotFound:
		return nil, status.Error(codes.NotFound, err.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return nil, status.FromContextError(err).Err()
	case err != nil:
		return nil, status.Errorf(codes.Unavailable, "cannot read installation %s", installationID)
	}
	if installation.PrincipalID != business.ModulePrincipalID(prefix) {
		return nil, status.Errorf(codes.PermissionDenied, "installation %s acts only through its installed solution's workload, and %s is not the installed solution", installationID, prefix)
	}
	org := installation.OrgID
	if named := req.GetOrganizationId(); named != "" && named != org {
		return nil, status.Errorf(codes.PermissionDenied, "installation %s belongs to organization %s, not %s", installationID, org, named)
	}
	// A solution the composition pins to one tenant acts there alone.
	if _, err := installationOrganization(prefix, org); err != nil {
		return nil, err
	}
	if installation.Status != "active" {
		return nil, status.Errorf(codes.PermissionDenied, "installation %s is %s; only an active installation acts for itself", installationID, installation.Status)
	}
	if req.GetAudience() == "" {
		return nil, status.Error(codes.InvalidArgument, "an issue names its audience")
	}
	if err := requireComposed(req.GetAudience()); err != nil {
		return nil, err
	}
	mint := &gen.StartInstallationTaskRequest{
		OrgId: org, InstallationId: installationID, TaskId: uuid.NewString(),
		Audience: req.GetAudience(), AuthorityScopes: scopes, TtlSeconds: ttl,
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
	// A solution's roles name actions on its own kinds that no organisation
	// role does; the installation's standing role is one of them, so each is a
	// candidate, decided below like every other.
	authority, err := service.DeliveredSolutionAuthority(ctx, audience)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "cannot read %s's delivered roles: %v", audience, err)
	}
	var held []*gen.WorkContextScope
	for _, kind := range kinds {
		var actions []string
		for _, action := range slices.Compact(slices.Sorted(slices.Values(append(candidateActions(kind, audience, nil), authority.RoleActions(kind)...)))) {
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
