package adapters

// Connect handlers for the host interfaces, one line per method, the same
// shape every other service here uses: cmd/connect-registration mounts them
// from connect_bindings.yaml.

import (
	"context"

	"connectrpc.com/connect"

	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	compositionv1 "accounts/pkg/gen/host/composition/v1"
	installationv1 "accounts/pkg/gen/host/installation/v1"
	sessionv1 "accounts/pkg/gen/host/session/v1"
)

var hostAuthoritySingleton = &HostAuthorityServer{workContexts: workContextSingleton, modules: moduleCapabilitiesSingleton}

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
func (h *hostAuthorityConnectHandler) Keys(ctx context.Context, req *connect.Request[authorityv1.KeysRequest]) (*connect.Response[authorityv1.KeysResponse], error) {
	return unary(ctx, req, h.inner.Keys)
}

type hostCompositionConnectHandler struct{ inner *HostCompositionServer }

func (h *hostCompositionConnectHandler) ApplyManifest(ctx context.Context, req *connect.Request[compositionv1.ApplyManifestRequest]) (*connect.Response[compositionv1.ApplyManifestResponse], error) {
	return unary(ctx, req, h.inner.ApplyManifest)
}

type hostInstallationConnectHandler struct{ svc *business.Service }

func (h *hostInstallationConnectHandler) server() *HostInstallationServer {
	return NewHostInstallationServer(installationSingleton, h.svc)
}
func (h *hostInstallationConnectHandler) Install(ctx context.Context, req *connect.Request[installationv1.InstallRequest]) (*connect.Response[installationv1.Installation], error) {
	return unary(ctx, req, h.server().Install)
}
func (h *hostInstallationConnectHandler) GrantToTeam(ctx context.Context, req *connect.Request[installationv1.GrantToTeamRequest]) (*connect.Response[installationv1.Installation], error) {
	return unary(ctx, req, h.server().GrantToTeam)
}
func (h *hostInstallationConnectHandler) List(ctx context.Context, req *connect.Request[installationv1.ListRequest]) (*connect.Response[installationv1.ListResponse], error) {
	return unary(ctx, req, h.server().List)
}

type hostSessionConnectHandler struct{ svc *business.Service }

func (h *hostSessionConnectHandler) SignIn(ctx context.Context, req *connect.Request[sessionv1.SignInRequest]) (*connect.Response[sessionv1.Session], error) {
	return unary(ctx, req, NewHostSessionServer(h.svc).SignIn)
}
