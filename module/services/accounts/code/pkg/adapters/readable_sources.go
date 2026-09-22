package adapters

import (
	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"context"

	"connectrpc.com/connect"
	codefly "github.com/codefly-dev/sdk-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// ListReadableSourceCollections verifies the exact forwarded viewer capability;
// this operation does not use the installation's module capability identity.
// The audience no longer separates those two — a module prefix and the
// module-capability audience are both legal audience strings — so what excludes a
// module's own identity token is the scope requirement below: that mint seals no
// authority scopes, and an empty scope set grants nothing.
func (s *ModuleCapabilitiesServer) ListReadableSourceCollections(ctx context.Context, req *gen.ListReadableSourceCollectionsRequest) (*gen.ListReadableSourceCollectionsResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := requireInternalCredential(ctx); err != nil {
		return nil, err
	}
	authority := WorkContextSingleton()
	if authority == nil || authority.verifier == nil || authority.configureErr != nil {
		return nil, status.Error(codes.Unavailable, "Work Context authority is not configured")
	}
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get(codefly.WorkContextHeaderName)
	if len(values) != 1 {
		return nil, status.Error(codes.Unauthenticated, "one viewer Work Context is required")
	}
	token, err := codefly.ParseWorkContextToken(values[0])
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid viewer Work Context")
	}
	// The audience is not compared against a literal: it names the composed
	// module the capability was minted for, and that module's own declaration
	// says which permission resource types its content is governed by. A host
	// that spelled one here would answer "no access" for every other consumer.
	claims, err := authority.verifier.Verify(token, codefly.WorkContextExpectations{Issuer: authority.issuer})
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid viewer Work Context")
	}
	// Both refusals answer identically. Telling "that audience declares no content"
	// apart from "your capability does not grant read on it" would report which
	// modules a composition declared, which the registration surface takes
	// constant-time care never to reveal.
	declared, err := service.ModuleContentResources(claims.GetAudience())
	if err != nil {
		return nil, status.Error(codes.PermissionDenied, "module content read scope required")
	}
	var resources []string
	for _, resource := range declared {
		if codefly.RequireWorkContextScope(claims, codefly.WorkContextScopeRequirement{ResourceKind: resource, Action: "read"}) == nil {
			resources = append(resources, resource)
		}
	}
	if len(resources) == 0 {
		return nil, status.Error(codes.PermissionDenied, "module content read scope required")
	}
	if claims.GetTenantId() == "" {
		return &gen.ListReadableSourceCollectionsResponse{}, nil
	}
	ctx = auth.WithVerifiedDatabaseIdentity(ctx, claims.GetOwnerPrincipalId(), claims.GetTenantId())
	subjects := []string{claims.GetOwnerPrincipalId()}
	for _, actor := range claims.GetActorChain() {
		subjects = append(subjects, actor.GetPrincipalId())
	}
	// The capability's own sealed scopes decide which collection details this
	// viewer may inspect: nothing is disclosed that the mint did not already
	// verify against live RBAC, and requireCurrentAuthority below re-resolves
	// every one of them, so a revoked permission fails the whole call rather
	// than quietly widening what the next page shows.
	seals := func(resource, action string) bool {
		return codefly.RequireWorkContextScope(claims, codefly.WorkContextScopeRequirement{ResourceKind: resource, Action: action}) == nil
	}
	disclosure := business.CollectionMetadataDisclosure{
		Grants:        seals("roles", "read"),
		SyncRequester: seals("audit", "read"),
	}
	return service.ReadableSourceCollections(ctx, claims.GetTenantId(), subjects, resources, disclosure, req, func(ctx context.Context) error {
		_, err := authority.requireCurrentAuthority(ctx, claims.GetTenantId(), claims)
		return err
	})
}
func (h *moduleCapabilitiesConnectHandler) ListReadableSourceCollections(ctx context.Context, req *connect.Request[gen.ListReadableSourceCollectionsRequest]) (*connect.Response[gen.ListReadableSourceCollectionsResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	md, _ := metadata.FromIncomingContext(ctx)
	md = md.Copy()
	md.Set(codefly.WorkContextHeaderName, req.Header().Values(codefly.WorkContextHeaderName)...)
	response, err := h.inner.ListReadableSourceCollections(metadata.NewIncomingContext(ctx, md), req.Msg)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(response), nil
}
