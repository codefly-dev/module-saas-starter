package adapters

// Connect handlers for the host interfaces, one line per method, the same
// shape every other service here uses: cmd/connect-registration mounts them
// from connect_bindings.yaml.

import (
	artifactv1 "accounts/pkg/gen/host/artifact/v1"
	"context"

	"connectrpc.com/connect"

	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	compositionv1 "accounts/pkg/gen/host/composition/v1"
	installationv1 "accounts/pkg/gen/host/installation/v1"
	sessionv1 "accounts/pkg/gen/host/session/v1"
)

var hostAuthoritySingleton = &HostAuthorityServer{workContexts: workContextSingleton}

// HostAuthoritySingleton serves codefly.dev/authority over the Work Context singleton.
func HostAuthoritySingleton() *HostAuthorityServer { return hostAuthoritySingleton }

var hostCompositionSingleton = &HostCompositionServer{}

// HostCompositionSingleton serves codefly.dev/composition.
func HostCompositionSingleton() *HostCompositionServer { return hostCompositionSingleton }

type hostAuthorityConnectHandler struct{ inner *HostAuthorityServer }

func (h *hostAuthorityConnectHandler) Issue(ctx context.Context, req *connect.Request[authorityv1.IssueRequest]) (*connect.Response[authorityv1.IssueResponse], error) {
	return unary(ctx, req, h.inner.Issue)
}
func (h *hostAuthorityConnectHandler) Check(ctx context.Context, req *connect.Request[authorityv1.CheckRequest]) (*connect.Response[authorityv1.CheckResponse], error) {
	return unary(ctx, req, h.inner.Check)
}
func (h *hostAuthorityConnectHandler) ListResources(ctx context.Context, req *connect.Request[authorityv1.ListResourcesRequest]) (*connect.Response[authorityv1.ListResourcesResponse], error) {
	return unary(ctx, req, h.inner.ListResources)
}
func (h *hostAuthorityConnectHandler) Delegate(ctx context.Context, req *connect.Request[authorityv1.DelegateRequest]) (*connect.Response[authorityv1.Delegation], error) {
	return unary(ctx, req, h.inner.Delegate)
}
func (h *hostAuthorityConnectHandler) RevokeDelegation(ctx context.Context, req *connect.Request[authorityv1.RevokeDelegationRequest]) (*connect.Response[authorityv1.RevokeDelegationResponse], error) {
	return unary(ctx, req, h.inner.RevokeDelegation)
}
func (h *hostAuthorityConnectHandler) GetDelegation(ctx context.Context, req *connect.Request[authorityv1.GetDelegationRequest]) (*connect.Response[authorityv1.Delegation], error) {
	return unary(ctx, req, h.inner.GetDelegation)
}
func (h *hostAuthorityConnectHandler) RegisterAdmissionKey(ctx context.Context, req *connect.Request[authorityv1.RegisterAdmissionKeyRequest]) (*connect.Response[authorityv1.RegisterAdmissionKeyResponse], error) {
	return unary(ctx, req, h.inner.RegisterAdmissionKey)
}
func (h *hostAuthorityConnectHandler) AttachResource(ctx context.Context, req *connect.Request[authorityv1.AttachResourceRequest]) (*connect.Response[authorityv1.AttachResourceResponse], error) {
	return unary(ctx, req, h.inner.AttachResource)
}
func (h *hostAuthorityConnectHandler) DetachResource(ctx context.Context, req *connect.Request[authorityv1.DetachResourceRequest]) (*connect.Response[authorityv1.DetachResourceResponse], error) {
	return unary(ctx, req, h.inner.DetachResource)
}
func (h *hostAuthorityConnectHandler) AuthorizeTransfer(ctx context.Context, req *connect.Request[authorityv1.AuthorizeTransferRequest]) (*connect.Response[authorityv1.AuthorizeTransferResponse], error) {
	return unary(ctx, req, h.inner.AuthorizeTransfer)
}
func (h *hostAuthorityConnectHandler) Keys(ctx context.Context, req *connect.Request[authorityv1.KeysRequest]) (*connect.Response[authorityv1.KeysResponse], error) {
	return unary(ctx, req, h.inner.Keys)
}

type hostCompositionConnectHandler struct{ inner *HostCompositionServer }

func (h *hostCompositionConnectHandler) ApplyManifest(ctx context.Context, req *connect.Request[compositionv1.ApplyManifestRequest]) (*connect.Response[compositionv1.ApplyManifestResponse], error) {
	return unary(ctx, req, h.inner.ApplyManifest)
}

type hostInstallationConnectHandler struct{ svc *business.Service }

func (h *hostInstallationConnectHandler) server() *HostInstallationServer {
	return &HostInstallationServer{}
}
func (h *hostInstallationConnectHandler) Install(ctx context.Context, req *connect.Request[installationv1.InstallRequest]) (*connect.Response[installationv1.Installation], error) {
	return unary(ctx, req, h.server().Install)
}
func (h *hostInstallationConnectHandler) Current(ctx context.Context, req *connect.Request[installationv1.CurrentInstallationRequest]) (*connect.Response[installationv1.CurrentInstallation], error) {
	return unary(ctx, req, h.server().Current)
}
func (h *hostInstallationConnectHandler) Review(ctx context.Context, req *connect.Request[installationv1.ReviewRequest]) (*connect.Response[installationv1.ConsentApproval], error) {
	return unary(ctx, req, h.server().Review)
}
func (h *hostInstallationConnectHandler) GrantRole(ctx context.Context, req *connect.Request[installationv1.GrantRoleRequest]) (*connect.Response[installationv1.Installation], error) {
	return unary(ctx, req, h.server().GrantRole)
}
func (h *hostInstallationConnectHandler) RevokeRole(ctx context.Context, req *connect.Request[installationv1.RevokeRoleRequest]) (*connect.Response[installationv1.Installation], error) {
	return unary(ctx, req, h.server().RevokeRole)
}
func (h *hostInstallationConnectHandler) ListRoleGrants(ctx context.Context, req *connect.Request[installationv1.ListRoleGrantsRequest]) (*connect.Response[installationv1.ListRoleGrantsResponse], error) {
	return unary(ctx, req, h.server().ListRoleGrants)
}
func (h *hostInstallationConnectHandler) List(ctx context.Context, req *connect.Request[installationv1.ListRequest]) (*connect.Response[installationv1.ListResponse], error) {
	return unary(ctx, req, h.server().List)
}

type hostOfferConnectHandler struct{ svc *business.Service }

func (h *hostOfferConnectHandler) ListOffers(ctx context.Context, req *connect.Request[installationv1.ListOffersRequest]) (*connect.Response[installationv1.ListOffersResponse], error) {
	return unary(ctx, req, HostOfferSingleton().ListOffers)
}
func (h *hostOfferConnectHandler) ReviewOffer(ctx context.Context, req *connect.Request[installationv1.ReviewRequest]) (*connect.Response[installationv1.ReviewedOffer], error) {
	return unary(ctx, req, HostOfferSingleton().ReviewOffer)
}

type hostSessionConnectHandler struct{ svc *business.Service }

func (h *hostSessionConnectHandler) SignIn(ctx context.Context, req *connect.Request[sessionv1.SignInRequest]) (*connect.Response[sessionv1.Session], error) {
	return unary(ctx, req, NewHostSessionServer(h.svc).SignIn)
}
func (h *hostSessionConnectHandler) Current(ctx context.Context, req *connect.Request[sessionv1.CurrentRequest]) (*connect.Response[sessionv1.Session], error) {
	return unary(ctx, req, NewHostSessionServer(h.svc).Current)
}
func (h *hostSessionConnectHandler) ListFixturePeople(ctx context.Context, req *connect.Request[sessionv1.ListFixturePeopleRequest]) (*connect.Response[sessionv1.ListFixturePeopleResponse], error) {
	return unary(ctx, req, NewHostSessionServer(h.svc).ListFixturePeople)
}

type hostArtifactConnectHandler struct{ svc *business.Service }

func (h *hostArtifactConnectHandler) server() *HostArtifactServer { return &HostArtifactServer{} }
func (h *hostArtifactConnectHandler) Approve(ctx context.Context, req *connect.Request[artifactv1.ArtifactRequest]) (*connect.Response[artifactv1.Approval], error) {
	return unary(ctx, req, h.server().Approve)
}
func (h *hostArtifactConnectHandler) Authorize(ctx context.Context, req *connect.Request[artifactv1.ArtifactRequest]) (*connect.Response[artifactv1.Approval], error) {
	return unary(ctx, req, h.server().Authorize)
}
func (h *hostArtifactConnectHandler) Revoke(ctx context.Context, req *connect.Request[artifactv1.RevokeRequest]) (*connect.Response[artifactv1.Approval], error) {
	return unary(ctx, req, h.server().Revoke)
}
