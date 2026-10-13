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

	"github.com/codefly-dev/core/workcontext"
	carrier "github.com/codefly-dev/sdk-go/workcontext"
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
// Given accounts' Connect tier, it also serves the module read a confined
// credential is delivered for (TestTheModuleReadThroughTheRealGatewayIsMintedByTheRealStartTask
// in the qualification): its accounts_connect upstream and Work Context JWKS
// are that tier, it forwards the gateway credential accounts trusts, and it
// declares one solution and one composed module the fixture person is entitled
// to. The solution and client registries and the entitlements are this
// package's in-memory ones; the mint, the key set and the edge are real.

const (
	moduleAuthorityContractGatewayEnv   = "MODULE_AUTHORITY_CONTRACT_GATEWAY"
	moduleAuthorityContractGatewayReady = "MODULE_AUTHORITY_CONTRACT_GATEWAY_READY "
)

type moduleAuthorityContractGatewayInput struct {
	InternalToken string `json:"internal_token"`
	// Accounts is the host:port of accounts' internal tier, as the accounts
	// half announced it.
	Accounts string `json:"accounts"`
	// Connect is the host:port of accounts' Connect tier and Work Context
	// JWKS; with it the module read flow is served too.
	Connect      string `json:"connect"`
	GatewayToken string `json:"gateway_token"`
	// Tenant and Owner are the accounts fixture's organization and person:
	// the confined credential is theirs.
	Tenant string `json:"tenant"`
	Owner  string `json:"owner"`
	// PublicBase is the deployment's public origin; the accounts half is
	// given the same.
	PublicBase string `json:"public_base"`
	// ConsumerInstallation is the solution's installation in Tenant, as the
	// accounts half records it: the one its entitlement names.
	ConsumerInstallation string `json:"consumer_installation"`
}

// moduleReadFlowReady is what the flow needs from this half: the person's
// confined credential and the facts it carries, and the declared names.
type moduleReadFlowReady struct {
	Bearer   string `json:"bearer"`
	Subject  string `json:"subject"`
	Org      string `json:"org"`
	Session  string `json:"session"`
	Solution string `json:"solution"`
	Module   string `json:"module"`
	// Header is the request header a Work Context travels in.
	Header string `json:"header"`
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
	// key only has to exist. The module read flow signs its confined credential
	// with it. Everything else runs as in production.
	public, access, err := ed25519.GenerateKey(rand.Reader)
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
	server := &http.Server{Handler: newGatewayHTTPHandler(gateway, nil), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	announced := map[string]any{"gateway": "http://" + listener.Addr().String()}
	if input.Connect != "" {
		announced["flow"] = serveModuleReadFlow(t, gateway, access, input)
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

// serveModuleReadFlow points gateway's Work Context edge at accounts' key set,
// declares the "example" solution and the "documents" module, entitles the
// fixture organization to both, and signs the fixture person a credential
// confined to the solution.
//
// The module upstream answers with the facts of the capability the edge
// forwarded to it, decoded by core, so the qualification can hold them to what
// the credential named: the module's own full verification is pinned in this
// package (TestMCPModuleReadThroughTheRealGatewayOnCoreWorkContexts) and in
// accounts (TestStartTaskMintsWhatACoreVerifierReadsForAModule).
func serveModuleReadFlow(t *testing.T, gateway *Gateway, access ed25519.PrivateKey, input moduleAuthorityContractGatewayInput) moduleReadFlowReady {
	t.Helper()
	require.NotEmpty(t, input.Tenant)
	require.NotEmpty(t, input.Owner)
	gateway.workContext = newWorkContextVerifier("http://" + input.Connect)
	registerSolutionUpstream(t, gateway, "example")
	declareReadingModule(t, gateway, "documents", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decoded, err := workcontext.Decode(r.Header.Get(carrier.HeaderName))
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		claims := decoded.Context()
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer": claims.GetIssuer(), "audience": claims.GetAudience(),
			"owner": claims.GetOwnerPrincipalId(), "tenant": claims.GetTenantId(),
			"session": claims.GetSessionId(), "installation": claims.GetSeal().GetInstallationId(),
		})
	}))
	require.NotEmpty(t, input.ConsumerInstallation)
	gateway.solutionEntitlements = &entitledTo{
		ids:           []string{fakeSolutionTarget("example"), fakeSolutionTarget("documents")},
		installations: map[string]string{fakeSolutionTarget("example"): input.ConsumerInstallation},
	}
	caller := signConfinedCallerFor(t, access, solutionMCPResource(input.PublicBase, "example"), input.Owner, input.Tenant)
	return moduleReadFlowReady{
		Bearer: caller.bearer, Subject: caller.subject, Org: caller.org, Session: caller.session,
		Solution: "example", Module: "documents", Header: carrier.HeaderName,
	}
}
