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

	"accounts/pkg/business"

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
)

// contractPrincipals declares the source module bound to another tenant (the
// delegation alone admits the source's organization) with a source-delegation
// binding addressed to the runtime module and a separate headless binding, and
// the runtime module with one operation binding the parent is exchanged
// through.
const contractPrincipals = `{` +
	`"` + contractSourceModule + `":{"workload":{"service_account":"module","namespace":"acme-prod","container":"app"},"tenant":"` + contractModuleOrg + `","operation_audiences":{` +
	`"source-sync":{"audience":"` + contractRuntimeModule + `",` +
	`"invoke_scopes":[{"resource_kind":"collections","actions":["read","write"]}],` +
	`"lookup_scopes":[{"resource_kind":"collections","actions":["read"]}],` +
	`"source_delegation_scopes":[{"resource_kind":"collections","actions":["read","write"]}]},` +
	`"reindex":{"audience":"indexservice",` +
	`"invoke_scopes":[{"resource_kind":"indexes","actions":["read","write"]}],` +
	`"lookup_scopes":[{"resource_kind":"indexes","actions":["read"]}],` +
	`"headless_scopes":[{"resource_kind":"indexes","actions":["write"]}]}}},` +
	`"` + contractRuntimeModule + `":{"workload":{"service_account":"module","namespace":"acme-prod","container":"app"},"tenant":"` + contractModuleOrg + `","operation_audiences":{` +
	`"ingest":{"audience":"docstore-ingest",` +
	`"invoke_scopes":[{"resource_kind":"collections","actions":["read","write"]}],` +
	`"lookup_scopes":[{"resource_kind":"collections","actions":["read"]}]}}}}`

type moduleAuthorityContractInput struct {
	InternalToken string            `json:"internal_token"`
	Secrets       map[string]string `json:"secrets"`
}

type moduleAuthorityContractReadyLine struct {
	// Internal is the host:port of the internal tier: h2c gRPC multiplexed
	// the way the private REST listener serves it in production.
	Internal string `json:"internal"`
	// Authority is the host:port of the `authority` endpoint.
	Authority string            `json:"authority"`
	Sources   map[string]string `json:"sources"`
	Modules   map[string]string `json:"modules"`
	Bindings  map[string]string `json:"bindings"`
	Tenant    string            `json:"tenant"`
	Owner     string            `json:"owner"`
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
	installModuleAuthorityContract(t, input)

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
// the in-memory fixture, a fresh Work Context key and the qualification's
// internal token, restoring what they held when t ends.
func installModuleAuthorityContract(t *testing.T, input moduleAuthorityContractInput) {
	t.Helper()
	require.NotEmpty(t, input.InternalToken)
	require.NotEmpty(t, input.Secrets[contractSourceModule])
	require.NotEmpty(t, input.Secrets[contractRuntimeModule])

	registry, err := business.ParseModulePrincipalRegistry(contractPrincipals)
	require.NoError(t, err)
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
	secrets, err := business.ParseRegistrationSecrets(
		contractSourceModule + ":" + registrationDigest(input.Secrets[contractSourceModule]) + "," +
			contractRuntimeModule + ":" + registrationDigest(input.Secrets[contractRuntimeModule]))
	require.NoError(t, err)
	svc.SetModuleIdentitySecrets(secrets)
	svc.SetModuleAuthorityReads(currentModuleAuthority{}, nil)
	svc.SetModuleCapabilities(nil, nil, registry)
	svc.SetAuditEmitter(&validatingAudit{})
	WithService(svc)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	workContextSingleton.Configure(WorkContextAuthorityConfiguration{
		Issuer: "accounts.contract", KeyID: "contract-key", PrivateKey: key, Authority: &workContextAuthorityFake{},
	})
	require.NoError(t, workContextSingleton.configureErr)
	SetInternalToken(input.InternalToken)
}
