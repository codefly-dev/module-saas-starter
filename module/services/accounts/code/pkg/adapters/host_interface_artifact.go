package adapters

// The artifact interface (host.artifact.v1, what the local authority serves):
// a person's consent to an exact authored artifact a module runs, for one of
// its installations, under one of its installed artifact policies. It is
// accounts' one artifact decision (business.DecideExecutableArtifact, the
// decision saas.accounts.v1 ModuleCapabilitiesService answers too), reached
// with the host interfaces' own proof: the module's workload credential and a
// parent capability the authority core issued, addressed to that module.

import (
	"context"

	"github.com/codefly-dev/core/workcontext"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	artifactv1 "accounts/pkg/gen/host/artifact/v1"
)

// HostArtifactServer serves host.artifact.v1.
type HostArtifactServer struct {
	artifactv1.UnimplementedArtifactServiceServer
}

// Approve records the person's consent to the artifact, for the installation
// under the policy: the person present, holding the policy's activate
// permission on that installation.
func (s *HostArtifactServer) Approve(ctx context.Context, req *artifactv1.ArtifactRequest) (*artifactv1.Approval, error) {
	return s.decide(ctx, req, "approve")
}

// Authorize answers whether a standing approval covers the artifact, for a run:
// the policy's run permission, which a delegated context may carry.
func (s *HostArtifactServer) Authorize(ctx context.Context, req *artifactv1.ArtifactRequest) (*artifactv1.Approval, error) {
	return s.decide(ctx, req, "authorize")
}

// Revoke withdraws an approval, for the person present.
func (s *HostArtifactServer) Revoke(ctx context.Context, req *artifactv1.RevokeRequest) (*artifactv1.Approval, error) {
	a, err := artifactCaller(ctx, req.GetParentToken(), req.GetActorCredential(), true)
	if err != nil {
		return nil, err
	}
	out, revision, err := service.RevokeApprovedExecutableArtifact(a.ctx, a.caller, a.tenant, a.owner, a.audience, req.GetInstallationId(), req.GetApprovalId(),
		func(p business.ArtifactPermission) error { return a.holds(p, req.GetInstallationId()) })
	if err != nil {
		return nil, err
	}
	return &artifactv1.Approval{QualifiedName: "host/approved-artifacts/" + out.ID, Digest: out.Digest, ExpectedRevision: revision}, nil
}

func (s *HostArtifactServer) decide(ctx context.Context, req *artifactv1.ArtifactRequest, action string) (*artifactv1.Approval, error) {
	identity := req.GetIdentity()
	if identity == nil || identity.GetSchema() == "" || identity.GetSource() == "" || len(identity.GetSubject()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "an artifact names its schema, source and subject")
	}
	a, err := artifactCaller(ctx, req.GetParentToken(), req.GetActorCredential(), action == "approve")
	if err != nil {
		return nil, err
	}
	policy, err := service.ExecutableArtifactPolicyFor(a.ctx, a.caller, a.tenant, a.audience, req.GetPolicy())
	if err != nil {
		return nil, err
	}
	permission := policy.Run
	if action == "approve" {
		permission = policy.Activate
	}
	if err := a.holds(permission, req.GetInstallationId()); err != nil {
		return nil, err
	}
	contracts := make([]business.ArtifactContract, 0, len(identity.GetContracts()))
	for _, c := range identity.GetContracts() {
		contracts = append(contracts, business.ArtifactContract{Kind: c.GetKind(), Name: c.GetName(), Digest: c.GetDigest()})
	}
	out, err := service.DecideExecutableArtifact(a.ctx, a.caller, a.tenant, a.owner, a.audience, action, business.ExecutableArtifactRequest{
		Installation: req.GetInstallationId(), Policy: req.GetPolicy(),
		Identity: business.ExecutableArtifactIdentity{Schema: identity.GetSchema(), Source: identity.GetSource(), Subject: identity.GetSubject(),
			Contracts: contracts, ExpectedRevision: identity.GetExpectedRevision()},
	})
	if err != nil {
		return nil, err
	}
	return &artifactv1.Approval{QualifiedName: "host/approved-artifacts/" + out.ID, Digest: out.Digest, ExpectedRevision: identity.GetExpectedRevision()}, nil
}

// artifactAuthentication is who asks: the module its workload credential
// proves, and the parent capability it presents, verified by the core.
type artifactAuthentication struct {
	ctx                     context.Context
	caller                  business.ModuleCaller
	verified                *workcontext.Verified
	tenant, owner, audience string
}

// holds refuses, by name, a parent that does not carry permission on the
// installation.
func (a artifactAuthentication) holds(permission business.ArtifactPermission, installation string) error {
	if !workContextGrants(a.verified.Context(), permission.Resource, permission.Action, installation) {
		return status.Errorf(codes.PermissionDenied, "%s does not hold %s:%s on installation %s", a.owner, permission.Resource, permission.Action, installation)
	}
	return nil
}

// artifactCaller authenticates an artifact request: the module by its workload
// credential, the parent by the core, addressed to that module. Consent
// (approve, revoke) is the person's own: a person owns the parent and no module
// acts between them.
func artifactCaller(ctx context.Context, parent, credential string, consent bool) (artifactAuthentication, error) {
	core, err := authorityCore()
	if err != nil {
		return artifactAuthentication{}, err
	}
	actor, err := workload(ctx, credential, "the actor")
	if err != nil {
		return artifactAuthentication{}, status.Error(codes.PermissionDenied, status.Convert(err).Message())
	}
	module, err := core.Composed(ctx, actor.Module)
	if err != nil {
		return artifactAuthentication{}, coreStatus(err)
	}
	verified, _, err := core.Check(ctx, parent, module.Principal)
	if err != nil {
		return artifactAuthentication{}, coreStatus(err)
	}
	claims := verified.Context()
	if actor.Organization != "" && claims.GetTenantId() != actor.Organization {
		return artifactAuthentication{}, status.Errorf(codes.PermissionDenied, "workload %s is bound to organization %s", actor.Module, actor.Organization)
	}
	if consent && (claims.GetOwnerPrincipalKind() != "human" || verified.Actor() != nil) {
		return artifactAuthentication{}, status.Error(codes.PermissionDenied, "consent to an artifact is given by a person present, not through a module acting for them")
	}
	return artifactAuthentication{
		ctx:      auth.WithVerifiedDatabaseIdentity(ctx, claims.GetOwnerPrincipalId(), claims.GetTenantId()),
		caller:   business.ModuleCaller{PrincipalID: business.ModulePrincipalID(actor.Module), BoundOrg: claims.GetTenantId()},
		verified: verified, tenant: claims.GetTenantId(), owner: claims.GetOwnerPrincipalId(), audience: claims.GetAudience(),
	}, nil
}
