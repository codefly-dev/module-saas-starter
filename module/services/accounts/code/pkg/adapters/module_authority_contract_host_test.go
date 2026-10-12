package adapters

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"accounts/pkg/auth"
	ed25519minter "accounts/pkg/auth/ed25519"
	"accounts/pkg/business"
	"accounts/pkg/business/executiontest"
	"accounts/pkg/business/moduleregistrytest"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// The accounts half of the module-authority contract host
// (qualification/module-authority). That qualification drives the published
// client library against the REAL host: this helper serves accounts' real
// internal tier (the listener the auth-gateway's module broker mints through)
// and its real `authority` endpoint, both built by NewGrpServer with their
// production interceptors, over an in-memory store seeded with one module
// fixture. The gateway half is TestModuleAuthorityContractGateway in the
// auth-gateway module.
//
// It is a helper process, not a test: it runs only when the qualification
// starts it with MODULE_AUTHORITY_CONTRACT_HOST set, serves until its stdin
// closes, and otherwise skips. The client library cannot be linked into this
// binary — it carries its own generated saas.accounts.v1 types, and the
// protobuf registry refuses the second registration at init — so the client
// runs in the qualification's own process and reaches this one over the wire.
//
// With a gateway token it also serves accounts' Connect tier — the listener the
// gateway forwards a person's calls to, behind the production
// connectAuthInterceptor — and the Work Context JWKS, so the qualification can
// put the gateway's real Gateway.ServeHTTP in front of the real StartTask. The
// fixture person is an administrator of contractOrg, which has installed the
// `documents` module.

const (
	// moduleAuthorityContractEnv carries the qualification's fixture inputs as
	// JSON (moduleAuthorityContractInput) and is what makes this helper serve.
	moduleAuthorityContractEnv = "MODULE_AUTHORITY_CONTRACT_HOST"
	// moduleAuthorityContractReady prefixes the one stdout line announcing
	// the listeners and the seeded fixture (moduleAuthorityContractReadyLine).
	moduleAuthorityContractReady = "MODULE_AUTHORITY_CONTRACT_READY "

	contractSourceModule  = "docstore" // mints through source delegations and headless
	contractRuntimeModule = "runtime"  // exchanges the delegation-bearing parent

	contractOrg        = "a1111111-1111-4111-8111-111111111111"
	contractModuleOrg  = "a2222222-2222-4222-8222-222222222222"
	contractPerson     = "a3333333-3333-4333-8333-333333333333"
	contractActive     = "a4444444-4444-4444-8444-444444444444" // active delegation, facts hold
	contractRevoked    = "a5555555-5555-4555-8555-555555555555" // active row, the person lost the role
	contractInvalid    = "a6666666-6666-4666-8666-666666666666" // active row, no authorization revision
	contractMissing    = "a7777777-7777-4777-8777-777777777777" // no delegation at all
	contractDelegation = "b"                                    // delegation ids are "b" + source id's tail

	// contractReadModule is the composed module the fixture person reads
	// through a solution: declared, and installed in contractOrg.
	contractReadModule = "documents"
)

// contractPrincipals declares the source module bound to another tenant with
// an explicit cross-tenant ceiling. The currentModuleAuthority fixture supplies
// the source organization's live installation consent separately from the
// person's delegation. The runtime module is bound to that source organization:
// its own credential and the delegated parent must name the same installation
// organization when it exchanges through its operation binding.
const contractPrincipals = `{` +
	`"` + contractSourceModule + `":{"workload":{"service_account":"module","namespace":"acme-prod","container":"app"},"tenant":"` + contractModuleOrg + `","cross_tenant":true,"operation_audiences":{` +
	`"source-sync":{"audience":"` + contractRuntimeModule + `",` +
	`"invoke_scopes":[{"resource_kind":"collections","actions":["read","write"]}],` +
	`"lookup_scopes":[{"resource_kind":"collections","actions":["read"]}],` +
	`"source_delegation_scopes":[{"resource_kind":"collections","actions":["read","write"]}]},` +
	`"reindex":{"audience":"indexservice",` +
	`"invoke_scopes":[{"resource_kind":"indexes","actions":["read","write"]}],` +
	`"lookup_scopes":[{"resource_kind":"indexes","actions":["read"]}],` +
	`"headless_scopes":[{"resource_kind":"indexes","actions":["write"]}]}}},` +
	`"` + contractRuntimeModule + `":{"workload":{"service_account":"runtime","namespace":"acme-prod","container":"app"},"tenant":"` + contractOrg + `","operation_audiences":{` +
	`"ingest":{"audience":"docstore-ingest",` +
	`"invoke_scopes":[{"resource_kind":"collections","actions":["read","write"]}],` +
	`"lookup_scopes":[{"resource_kind":"collections","actions":["read"]}]}}}}`

type moduleAuthorityContractInput struct {
	InternalToken string `json:"internal_token"`
	// GatewayToken is the credential the gateway forwards a person's identity
	// with; when set, the Connect tier is served too.
	GatewayToken string `json:"gateway_token"`
	// PublicBase is the deployment's public origin, which a confined
	// credential's resource is named under; the gateway half is given the same.
	PublicBase string `json:"public_base"`
}

type moduleAuthorityContractReadyLine struct {
	// Internal is the host:port of the internal tier: h2c gRPC multiplexed
	// the way the private REST listener serves it in production.
	Internal string `json:"internal"`
	// Authority is the host:port of the `authority` endpoint.
	Authority string `json:"authority"`
	// Connect is the host:port of the Connect tier and the Work Context JWKS,
	// when a gateway token was given.
	Connect  string            `json:"connect,omitempty"`
	Sources  map[string]string `json:"sources"`
	Modules  map[string]string `json:"modules"`
	Bindings map[string]string `json:"bindings"`
	Tenant   string            `json:"tenant"`
	Owner    string            `json:"owner"`
	// Tokens are the projected ServiceAccount tokens of one pod per module,
	// each running its approved build, keyed by module prefix — plus
	// "unapproved", a pod of the source module's workload running a build
	// nothing approved. A module proves which module it is with one (#1056).
	Tokens map[string]string `json:"tokens"`
}

// contractStore is the in-memory store the helper's business.Service runs
// on: the source-delegation reads and writes, with facts per source so each
// refusal is reachable by source id, the way a module names a source.
type contractStore struct {
	*sourceDelegationMemoryStore
	factsBySource map[string]business.SourceDelegationFacts
}

func (s *contractStore) OrganizationIDExists(context.Context, string) (bool, error) {
	return true, nil
}

// The reads a person's StartTask makes: the fixture person is an administrator
// of contractOrg and holds no platform role, and no solution binding is
// declared, so the audience vocabulary is the declared modules.
func (s *contractStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *contractStore) LiveDeclaredSolutionBindingIDs(context.Context) ([]string, error) {
	return nil, nil
}

func (s *contractStore) GetPlatformRole(context.Context, string) (string, error) { return "", nil }

func (s *contractStore) GetOrgMembership(_ context.Context, orgID, userID string) (*gen.OrgMembership, error) {
	if orgID == contractOrg && userID == contractPerson {
		return &gen.OrgMembership{UserId: userID, Role: gen.OrgRole_ORG_ROLE_ADMIN}, nil
	}
	return nil, nil
}

func (s *contractStore) SourceDelegationFacts(_ context.Context, _, _, sourceID string) (*business.SourceDelegationFacts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	facts := s.factsBySource[sourceID]
	return &facts, nil
}

func TestModuleAuthorityContractHost(t *testing.T) {
	raw := os.Getenv(moduleAuthorityContractEnv)
	if raw == "" {
		t.Skip("a helper process for qualification/module-authority; it serves only when that qualification starts it")
	}
	var input moduleAuthorityContractInput
	require.NoError(t, json.Unmarshal([]byte(raw), &input))

	// The production servers, with the production interceptors, built before
	// the service is wired as main does: NewGrpServer configures the Work
	// Context issuer from the deployment's key, which the fixture then
	// replaces with its own. Nothing is bound to a Codefly-allocated port: the
	// helper listens on loopback ephemeral ports and announces them.
	servers, err := NewGrpServer(&Configuration{})
	require.NoError(t, err)
	t.Cleanup(servers.internalGRPC.Stop)
	t.Cleanup(servers.authorityGRPC.Stop)
	tokens := installModuleAuthorityContract(t, input)

	internalListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	internal := &http.Server{
		Handler:           h2c.NewHandler(multiplexInternalGRPC(servers.internalGRPC, http.NotFoundHandler()), &http2.Server{}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = internal.Serve(internalListener) }()
	t.Cleanup(func() { _ = internal.Close() })

	authorityListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = servers.authorityGRPC.Serve(authorityListener) }()

	var connectAddress string
	if input.GatewayToken != "" {
		require.NotEmpty(t, input.PublicBase)
		previous, _ := auth.ConfiguredPublicOrigin()
		require.NoError(t, auth.SetConfiguredPublicOrigin(input.PublicBase))
		t.Cleanup(func() { _ = auth.SetConfiguredPublicOrigin(previous) })
		connectAddress = serveContractConnectTier(t, servers, input.GatewayToken)
	}

	ready, err := json.Marshal(moduleAuthorityContractReadyLine{
		Internal:  internalListener.Addr().String(),
		Authority: authorityListener.Addr().String(),
		Sources: map[string]string{
			"active": contractActive, "revoked": contractRevoked,
			"invalid": contractInvalid, "missing": contractMissing,
		},
		Modules:  map[string]string{"source": contractSourceModule, "runtime": contractRuntimeModule},
		Bindings: map[string]string{"headless": "reindex", "delegation": "source-sync", "exchange": "ingest"},
		Tenant:   contractOrg,
		Owner:    contractPerson,
		Tokens:   tokens,
		Connect:  connectAddress,
	})
	require.NoError(t, err)
	_, err = fmt.Fprintln(os.Stdout, moduleAuthorityContractReady+string(ready))
	require.NoError(t, err)

	// Serve until the qualification closes our stdin.
	stdin := bufio.NewReader(os.Stdin)
	for {
		if _, err := stdin.ReadByte(); err != nil {
			return
		}
	}
}

// installModuleAuthorityContract points the adapters at a business.Service over
// the in-memory fixture, a fake cluster whose pods the modules run in, a fresh
// Work Context key and the qualification's internal token, restoring what they
// held when t ends. It returns each module pod's projected token.
func installModuleAuthorityContract(t *testing.T, input moduleAuthorityContractInput) map[string]string {
	t.Helper()
	require.NotEmpty(t, input.InternalToken)

	registry, err := moduleregistrytest.Parse(contractPrincipals)
	require.NoError(t, err)
	registry[business.ModulePrincipalID(contractReadModule)] = business.ModulePrincipalGrant{Prefix: contractReadModule}
	binding := registry[business.ModulePrincipalID(contractSourceModule)].OperationAudiences["source-sync"]
	digest := business.SourceDelegationBindingDigest(binding)
	holds := business.SourceDelegationFacts{
		SourceExists: true, MemberRole: "admin", UserStatus: "active",
		OrganizationRevision: 40, PrincipalRevision: 42,
	}
	delegations := map[string]*business.SourceDelegation{}
	for _, source := range []string{contractActive, contractRevoked, contractInvalid} {
		id := contractDelegation + source[1:]
		delegations[id] = &business.SourceDelegation{
			ID: id, OrgID: contractOrg, SourceID: source, PrincipalID: contractPerson,
			ModulePrefix: contractSourceModule, BindingID: "source-sync",
			BindingDigest: digest, CreatedAt: time.Now(),
		}
	}
	lostRole := holds
	lostRole.MemberRole = "member"
	noRevision := holds
	noRevision.OrganizationRevision, noRevision.PrincipalRevision = 0, 0
	store := &contractStore{
		sourceDelegationMemoryStore: &sourceDelegationMemoryStore{delegations: delegations},
		factsBySource: map[string]business.SourceDelegationFacts{
			contractActive: holds, contractRevoked: lostRole, contractInvalid: noRevision,
		},
	}

	previousService, previousAuthority := service, *workContextSingleton
	t.Cleanup(func() {
		service, *workContextSingleton = previousService, previousAuthority
		SetInternalToken("")
	})
	svc, err := business.NewService(store)
	require.NoError(t, err)
	svc.SetModuleAuthorityReads(currentModuleAuthority{service: svc}, nil)
	svc.SetModuleCapabilities(nil, nil, registry)
	cluster := bindModuleExecution(t, svc, registry)
	tokens := map[string]string{}
	for _, prefix := range []string{contractSourceModule, contractRuntimeModule} {
		tokens[prefix] = moduleToken(t, prefix)
	}
	tokens["unapproved"] = cluster.Run(
		moduleWorkload(registry[business.ModulePrincipalID(contractSourceModule)]),
		executiontest.Image(contractSourceModule+"-unreviewed"))
	svc.SetAuditEmitter(&validatingAudit{})
	WithService(svc)
	// Keyed as work.go keys production: the cluster key signs access tokens and
	// Work Contexts alike, under the minter's key id and the "saas-starter"
	// issuer, and the JWKS publishes it.
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	minter := ed25519minter.New(ed25519minter.Config{}, key, nil)
	svc.SetJWTMinter(minter)
	// The person's revision is one fact: the seal a delegation-bearing capability
	// carries, the delegation re-check that confirms it, and the facts a
	// person's StartTask resolves all read the same value.
	seals := moduleSealAnswers(12)
	seals.revisionFrom = holds.EffectiveRevision
	workContextSingleton.Configure(WorkContextAuthorityConfiguration{
		Issuer: "saas-starter", KeyID: minter.KeyID(), PrivateKey: key,
		Authority: &workContextAuthorityFake{
			facts: &business.WorkContextAuthorityFacts{
				OrganizationRevision: holds.OrganizationRevision, PrincipalRevision: holds.PrincipalRevision,
			},
			// The installation of the solution a confined credential is delivered
			// to: an administrator consented to its minting for the read module,
			// on that module's resource kind, and nothing else.
			installationFcts: &business.InstallationAuthorityFacts{
				OwnerPrincipalID:       contractPerson,
				OrganizationRevision:   holds.OrganizationRevision,
				OwnerPrincipalRevision: holds.PrincipalRevision,
				Actor: &business.Principal{
					ID: "a8888888-8888-4888-8888-888888888888", Kind: business.PrincipalKindAgent,
					AgentIdentifier:  "acme/example:1.0.0",
					AllowedAudiences: []string{contractReadModule}, AllowedScopes: []string{contractReadModule},
				},
			},
		},
		Seals: seals,
	})
	workContextSingleton.replay = &memoryReplay{}
	require.NoError(t, workContextSingleton.configureErr)
	SetInternalToken(input.InternalToken)
	return tokens
}

// serveContractConnectTier serves the Connect services the way ConnectServer.Run
// does — the catalog registration behind the production connectAuthInterceptor,
// over h2c — with the Work Context JWKS beside them, and returns its host:port.
// A forwarded identity is believed only with gatewayToken, as in production.
func serveContractConnectTier(t *testing.T, servers *GrpcServer, gatewayToken string) string {
	t.Helper()
	SetGatewayToken(gatewayToken)
	t.Cleanup(func() { SetGatewayToken("") })
	tier, err := NewConnectServer(&Configuration{}, servers)
	require.NoError(t, err)
	mux := http.NewServeMux()
	registerCatalogConnectServices(mux, tier, connect.WithInterceptors(connectAuthInterceptor(func() auth.JWTMinter {
		if service == nil {
			return nil
		}
		return service.JWTMinter()
	})))
	mux.Handle(jwksPath, NewJWKSHTTPHandler(service))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := &http.Server{Handler: h2c.NewHandler(mux, &http2.Server{}), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return listener.Addr().String()
}
