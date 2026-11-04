package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// The gateway half of the module-authority contract host
// (qualification/module-authority). That qualification drives the published
// client library against the REAL host: this helper serves this gateway's real
// HTTP handler — the router built from the generated REST and Connect
// catalogs, and the module broker routes on it — whose accounts connection is
// the REAL accounts internal tier the accounts half serves
// (TestModuleAuthorityContractHost in accounts/pkg/adapters). The gateway and
// accounts cannot share a process: this module may not import accounts
// (TestAccountsDependencyUsesGeneratedClientOnly), and neither can link the
// client library, whose generated saas.accounts.v1 types the protobuf
// registry refuses to register a second time.
//
// It is a helper process, not a test: it runs only when the qualification
// starts it with MODULE_AUTHORITY_CONTRACT_GATEWAY set, serves until its stdin
// closes, and otherwise skips.
//
// Given accounts' Connect tier, its accounts_connect upstream is that tier. No
// module is read through this gateway: a module call goes through the hop
// (gateway_hop.go), never to the module from here.

const (
	moduleAuthorityContractGatewayEnv   = "MODULE_AUTHORITY_CONTRACT_GATEWAY"
	moduleAuthorityContractGatewayReady = "MODULE_AUTHORITY_CONTRACT_GATEWAY_READY "
)

type moduleAuthorityContractGatewayInput struct {
	InternalToken string `json:"internal_token"`
	// Accounts is the host:port of accounts' internal tier, as the accounts
	// half announced it.
	Accounts string `json:"accounts"`
	// Connect is the host:port of accounts' Connect tier: the gateway's
	// accounts_connect upstream.
	Connect      string `json:"connect"`
	GatewayToken string `json:"gateway_token"`
	// PublicBase is the deployment's public origin; the accounts half is
	// given the same.
	PublicBase string `json:"public_base"`
}

func TestModuleAuthorityContractGateway(t *testing.T) {
	raw := os.Getenv(moduleAuthorityContractGatewayEnv)
	if raw == "" {
		t.Skip("a helper process for qualification/module-authority; it serves only when that qualification starts it")
	}
	var input moduleAuthorityContractGatewayInput
	require.NoError(t, json.Unmarshal([]byte(raw), &input))
	require.NotEmpty(t, input.InternalToken)
	require.NotEmpty(t, input.Accounts)

	// The accounts connection main builds: plaintext gRPC to the internal
	// listener.
	conn, err := grpc.NewClient(input.Accounts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	// The operator-trusted public origin a confined credential names its
	// resource under, read by NewExtAuthz.
	if input.Connect != "" {
		require.NotEmpty(t, input.PublicBase)
		t.Setenv("APP_BASE_URL", input.PublicBase)
	}
	// Access-token verification is not under test for the module broker; the
	// key only has to exist. Everything else runs as in production.
	public, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	authz := NewExtAuthz(conn, staticAccessKeys(public))
	authz.internalToken = input.InternalToken
	authz.gatewayToken = input.GatewayToken

	// The router main builds, from the generated catalogs and this service's
	// routing extensions.
	restEntries, err := LoadAllRESTRoutes(context.Background(), DefaultRoutingDir())
	require.NoError(t, err)
	connectEntries, err := LoadConnectRoutesFromCatalog()
	require.NoError(t, err)
	matcher := NewRouteMatcher(restEntries, connectEntries)
	accounts := &url.URL{Scheme: "http", Host: input.Accounts}
	upstreams := map[string]*url.URL{"accounts": accounts, "accounts_connect": accounts}
	if input.Connect != "" {
		upstreams["accounts_connect"] = &url.URL{Scheme: "http", Host: input.Connect}
	}

	// The solution and client registries are consulted only by solution
	// routes and cross-origin requests, which the module broker never is; the
	// in-memory ones keep this helper from reconciling against a registry the
	// accounts half does not seed.
	gateway := NewGateway(authz, matcher, upstreams, nil, newFakeSolutionRegistry(), newFakeClientRegistry())

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	handler := newGatewayHTTPHandler(gateway, nil)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	// The workload endpoint, as main serves it: the same handler on a port of
	// its own, marked so the module credential exchanges know they are on it.
	// The module-authority client mints there — the composition points the
	// gateway value a workload reads at this endpoint — and the viewer's
	// reads stay on the public one.
	workloadPort, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	workloadServer := &http.Server{Handler: workloadListener(handler), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = workloadServer.Serve(workloadPort) }()
	t.Cleanup(func() { _ = workloadServer.Close() })

	announced := map[string]any{
		"gateway":  "http://" + listener.Addr().String(),
		"workload": "http://" + workloadPort.Addr().String(),
	}
	ready, err := json.Marshal(announced)
	require.NoError(t, err)
	_, err = fmt.Fprintln(os.Stdout, moduleAuthorityContractGatewayReady+string(ready))
	require.NoError(t, err)

	stdin := bufio.NewReader(os.Stdin)
	for {
		if _, err := stdin.ReadByte(); err != nil {
			return
		}
	}
}
