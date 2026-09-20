package adapters

import (
	"context"

	"accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"connectrpc.com/connect"
)

// ClientRegistryServer serves the declared first-party clients (issue #853) on
// the accounts internal listener. The auth-gateway is its only client: it
// resolves a client id to the origins that client calls from. A browser never
// reaches this — the public half of the flow is on AuthService.
type ClientRegistryServer struct {
	gen.UnsafeClientRegistryServiceServer
}

var clientRegistrySingleton = &ClientRegistryServer{}

// ClientRegistrySingleton returns the shared server instance.
func ClientRegistrySingleton() *ClientRegistryServer { return clientRegistrySingleton }

func (s *ClientRegistryServer) ListRegisteredClients(
	ctx context.Context, req *gen.ListRegisteredClientsRequest,
) (*gen.ListRegisteredClientsResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	declared := service.RegisteredClients(ctx)
	out := &gen.ListRegisteredClientsResponse{
		Clients: make([]*gen.RegisteredClient, 0, len(declared)),
	}
	for _, client := range declared {
		out.Clients = append(out.Clients, registeredClientProto(client))
	}
	return out, nil
}

func registeredClientProto(client auth.RegisteredClient) *gen.RegisteredClient {
	return &gen.RegisteredClient{
		ClientId:     client.ClientID,
		Name:         client.Name,
		Kind:         gen.ClientKind_CLIENT_KIND_PUBLIC,
		RedirectUris: client.RedirectURIs,
		Origins:      client.Origins,
	}
}

// clientRegistryConnectHandler serves the same server over Connect, so both
// listeners answer from one implementation.
type clientRegistryConnectHandler struct {
	inner *ClientRegistryServer
}

func (h *clientRegistryConnectHandler) ListRegisteredClients(ctx context.Context, req *connect.Request[gen.ListRegisteredClientsRequest]) (*connect.Response[gen.ListRegisteredClientsResponse], error) {
	return unary(ctx, req, h.inner.ListRegisteredClients)
}
