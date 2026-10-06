package adapters

import (
	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"connectrpc.com/connect"
	"context"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/sdk-go/workcontext"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func (s *ModuleCapabilitiesServer) ApproveExecutableArtifact(ctx context.Context, req *gen.ModuleExecutableArtifactRequest) (*gen.ModuleExecutableArtifactResponse, error) {
	return s.decideExecutableArtifact(ctx, req, "approve")
}
func (s *ModuleCapabilitiesServer) AuthorizeExecutableArtifact(ctx context.Context, req *gen.ModuleExecutableArtifactRequest) (*gen.ModuleExecutableArtifactResponse, error) {
	return s.decideExecutableArtifact(ctx, req, "authorize")
}
func (s *ModuleCapabilitiesServer) RevokeExecutableArtifact(ctx context.Context, req *gen.ModuleRevokeExecutableArtifactRequest) (*gen.ModuleExecutableArtifactResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	a, err := authenticateArtifactParent(ctx, req.ParentWorkContextToken)
	if err != nil {
		return nil, err
	}
	if a.actor != nil {
		return nil, status.Error(codes.PermissionDenied, "artifact consent requires the person present")
	}
	out, revision, err := service.RevokeApprovedExecutableArtifact(a.ctx, a.caller, a.parent.TenantId, a.parent.OwnerPrincipalId, a.parent.Audience, req.InstallationId, req.ApprovalId, func(p business.ArtifactPermission) error {
		if err := workcontext.RequireWorkContextScope(a.parent, workcontext.WorkContextScopeRequirement{ResourceKind: p.Resource, Action: p.Action, ResourceID: req.InstallationId}); err != nil {
			return status.Error(codes.PermissionDenied, "artifact parent scope denied")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &gen.ModuleExecutableArtifactResponse{SchemaVersion: "host.executable-artifact/v1", QualifiedName: "host/approved-artifacts/" + out.ID, Digest: out.Digest, ExpectedRevision: revision}, nil
}

func (s *ModuleCapabilitiesServer) decideExecutableArtifact(ctx context.Context, req *gen.ModuleExecutableArtifactRequest, action string) (*gen.ModuleExecutableArtifactResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	authn, err := authenticateArtifactParent(ctx, req.ParentWorkContextToken)
	if err != nil {
		return nil, err
	}
	ctx, caller, parent, actor := authn.ctx, authn.caller, authn.parent, authn.actor
	// Explicit administrator consent is a person-present operation. Delegated
	// execution can read approvals, but cannot turn itself into its owner's consent.
	if action != "authorize" && actor != nil {
		return nil, status.Error(codes.PermissionDenied, "artifact consent requires the person present")
	}
	policy, err := service.ExecutableArtifactPolicyFor(caller, parent.TenantId, parent.Audience, req.PolicyId)
	if err != nil {
		return nil, err
	}
	permission := policy.Run
	if action != "authorize" {
		permission = policy.Activate
	}
	if err = workcontext.RequireWorkContextScope(parent, workcontext.WorkContextScopeRequirement{ResourceKind: permission.Resource, Action: permission.Action, ResourceID: req.InstallationId}); err != nil {
		return nil, status.Error(codes.PermissionDenied, "artifact parent scope denied")
	}
	identity := req.Identity
	contracts := make([]business.ArtifactContract, 0, len(identity.Contracts))
	for _, c := range identity.Contracts {
		contracts = append(contracts, business.ArtifactContract{Kind: c.Kind, Name: c.Name, Digest: c.Digest})
	}
	out, err := service.DecideExecutableArtifact(ctx, caller, parent.TenantId, parent.OwnerPrincipalId, parent.Audience, action, business.ExecutableArtifactRequest{Installation: req.InstallationId, Policy: req.PolicyId, Identity: business.ExecutableArtifactIdentity{Schema: identity.Schema, Source: identity.Source, Subject: identity.Subject, Contracts: contracts, ExpectedRevision: identity.ExpectedRevision}})
	if err != nil {
		return nil, err
	}
	return &gen.ModuleExecutableArtifactResponse{SchemaVersion: "host.executable-artifact/v1", QualifiedName: "host/approved-artifacts/" + out.ID, Digest: out.Digest, ExpectedRevision: identity.ExpectedRevision}, nil
}

func (h *moduleCapabilitiesConnectHandler) ApproveExecutableArtifact(ctx context.Context, req *connect.Request[gen.ModuleExecutableArtifactRequest]) (*connect.Response[gen.ModuleExecutableArtifactResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	md, _ := metadata.FromIncomingContext(ctx)
	md = md.Copy()
	md.Set(workcontext.WorkContextHeaderName, req.Header().Values(workcontext.WorkContextHeaderName)...)
	out, err := h.inner.ApproveExecutableArtifact(metadata.NewIncomingContext(ctx, md), req.Msg)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(out), nil
}

func (h *moduleCapabilitiesConnectHandler) AuthorizeExecutableArtifact(ctx context.Context, req *connect.Request[gen.ModuleExecutableArtifactRequest]) (*connect.Response[gen.ModuleExecutableArtifactResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	md, _ := metadata.FromIncomingContext(ctx)
	md = md.Copy()
	md.Set(workcontext.WorkContextHeaderName, req.Header().Values(workcontext.WorkContextHeaderName)...)
	out, err := h.inner.AuthorizeExecutableArtifact(metadata.NewIncomingContext(ctx, md), req.Msg)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(out), nil
}

func (h *moduleCapabilitiesConnectHandler) RevokeExecutableArtifact(ctx context.Context, req *connect.Request[gen.ModuleRevokeExecutableArtifactRequest]) (*connect.Response[gen.ModuleExecutableArtifactResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	md, _ := metadata.FromIncomingContext(ctx)
	md = md.Copy()
	md.Set(workcontext.WorkContextHeaderName, req.Header().Values(workcontext.WorkContextHeaderName)...)
	out, err := h.inner.RevokeExecutableArtifact(metadata.NewIncomingContext(ctx, md), req.Msg)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(out), nil
}

type artifactAuthentication struct {
	ctx    context.Context
	caller business.ModuleCaller
	parent *basev0.WorkContextV1
	actor  *business.Principal
}

func authenticateArtifactParent(ctx context.Context, encoded string) (artifactAuthentication, error) {
	if err := requireInternalCredential(ctx); err != nil {
		return artifactAuthentication{}, err
	}
	md, _ := metadata.FromIncomingContext(ctx)
	if len(md.Get(workcontext.WorkContextHeaderName)) != 1 {
		return artifactAuthentication{}, status.Error(codes.Unauthenticated, "one module Work Context required")
	}
	authority := WorkContextSingleton()
	if service == nil || authority == nil || authority.verifier == nil || authority.configureErr != nil || authority.authority == nil {
		return artifactAuthentication{}, status.Error(codes.Unavailable, "artifact authority unavailable")
	}
	caller, err := moduleCaller(ctx)
	if err != nil {
		return artifactAuthentication{}, err
	}
	token, err := workcontext.ParseWorkContextToken(encoded)
	if err != nil {
		return artifactAuthentication{}, status.Error(codes.PermissionDenied, "invalid artifact parent")
	}
	verified, err := authority.verifier.Verify(token, workcontext.WorkContextExpectations{Issuer: authority.issuer})
	if err != nil || verified.TenantId == "" || verified.OwnerPrincipalId == "" {
		return artifactAuthentication{}, status.Error(codes.PermissionDenied, "invalid artifact parent")
	}
	ctx = auth.WithVerifiedDatabaseIdentity(ctx, verified.OwnerPrincipalId, verified.TenantId)
	if len(verified.ActorChain) > 0 && authority.journal == nil {
		return artifactAuthentication{}, status.Error(codes.Unavailable, "delegation journal unavailable")
	}
	_, parent, actor, err := authority.verifyParent(ctx, verified.TenantId, verified.OwnerPrincipalId, encoded)
	if err != nil {
		return artifactAuthentication{}, err
	}
	return artifactAuthentication{ctx, caller, parent, actor}, nil
}
