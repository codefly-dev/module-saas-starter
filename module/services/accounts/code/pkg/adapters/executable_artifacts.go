package adapters

import (
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"context"

	"connectrpc.com/connect"
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
	a, err := authenticateModuleParent(ctx, req.ParentWorkContextToken)
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
	authn, err := authenticateModuleParent(ctx, req.ParentWorkContextToken)
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
