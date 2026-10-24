package adapters

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	"accounts/pkg/business/moduleregistrytest"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// THE BEARER-LESS ARM ADMITS ONE SHAPE, pinned over every mint.
//
// The gateway admits a capability at /v1/<alias>/* with no bearer only when
// AdmitWorkloadOperation says it is a module operation context
// (StartModuleOperationTask). Every row below mints a capability through a real
// mint path and asserts the verdict; only the operation context is admitted.
// A new mint RPC, a new signing entrypoint or a new owner kind fails
// TestWorkloadOperationTableNamesEveryMintAndOwnerKind until it has a row here.

const admissionTestToken = "workload-admission-test"

func admissionContext(t *testing.T) context.Context {
	t.Helper()
	SetInternalToken(admissionTestToken)
	t.Cleanup(func() { SetInternalToken("") })
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-codefly-internal-token", admissionTestToken))
}

func admitWorkload(t *testing.T, server *WorkContextAuthorityServer, token, alias string) *gen.AdmitWorkloadOperationResponse {
	t.Helper()
	answer, err := server.AdmitWorkloadOperation(admissionContext(t), &gen.AdmitWorkloadOperationRequest{WorkContext: token, Alias: alias})
	require.NoError(t, err)
	return answer
}

// admissionMint mints one capability and names the alias it is presented at.
type admissionMint func(t *testing.T) (server *WorkContextAuthorityServer, alias, token string)

// admissionRow is one mint path and the verdict it must get.
type admissionRow struct {
	name string
	// rpc is the saas.accounts.v1 method the capability comes from, empty for
	// a path no RPC answers (an install step's job, a crafted shape).
	rpc string
	// kind is the owner kind the minted capability must carry.
	kind string
	// want is the refusal; UNSPECIFIED means admitted.
	want gen.WorkloadOperationRefusal
	mint admissionMint
}

func fromSealPin(name string) admissionMint {
	return func(t *testing.T) (*WorkContextAuthorityServer, string, string) {
		for _, tc := range sealPinCases() {
			if tc.name != name {
				continue
			}
			server, audience, token, err := tc.mint(t)
			require.NoError(t, err)
			return server, audience, token
		}
		t.Fatalf("no seal pin case %q", name)
		return nil, "", ""
	}
}

func installedExchange(binding, alias string, lookup bool) admissionMint {
	return func(t *testing.T) (*WorkContextAuthorityServer, string, string) {
		c := moduleActorFixture(t, append(moduleActorScopes, &gen.WorkContextScope{ResourceKind: "documents", Actions: []string{"read"}}))
		issued, err := c.exchange(binding, c.parent.Token, c.moduleToken, lookup)
		require.NoError(t, err)
		return c.server, alias, issued.GetToken()
	}
}

func moduleOperationFixture(t *testing.T) (business.ModuleOperationContextAuthority, string) {
	t.Helper()
	installModuleOperationContextService(t, headlessPrincipals)
	installModuleWorkContextAuthority(t)
	principal := business.ModulePrincipalID("documents")
	installation, _, err := service.WorkContextSeal(context.Background(), moduleWorkContextTenant, principal, "")
	require.NoError(t, err)
	return business.ModuleOperationContextAuthority{
		ModuleWorkContextAuthority: business.ModuleWorkContextAuthority{PrincipalID: principal, Tenant: moduleWorkContextTenant, InstallationID: installation},
		BindingID:                  "model",
		Audience:                   "modelservice",
		Scopes:                     []business.ModuleOperationScope{{ResourceKind: "modelservice.profiles", Actions: []string{"invoke", "read"}}},
		Revision:                   1,
	}, installation
}

// operationShape mints what StartModuleOperationTask signs, with change applied
// to the start input or the request, so a row can vary exactly one thing.
func operationShape(change func(*mintRequest)) admissionMint {
	return func(t *testing.T) (*WorkContextAuthorityServer, string, string) {
		authority, installation := moduleOperationFixture(t)
		request := mintRequest{
			Start: workcontext.StartInput{
				TenantID: authority.Tenant, OwnerPrincipalID: authority.PrincipalID,
				OwnerPrincipalKind: business.PrincipalKindService, OrganizationID: authority.Tenant,
				TaskID: uuid.NewString(), Audience: "modelservice", InstallationID: installation,
				AuthorityScopes: []*basev0.WorkScopeV1{{ResourceKind: "modelservice.profiles", Actions: []string{"read"}}},
			},
			Replay: workcontext.ReplayIdempotent,
			TTL:    business.ModuleOperationContextTTL,
		}
		change(&request)
		token, _, err := workContextSingleton.mint(context.Background(), request)
		require.NoError(t, err)
		return workContextSingleton, "modelservice", token
	}
}

func workloadOperationRows() []admissionRow {
	refused := func(r gen.WorkloadOperationRefusal) gen.WorkloadOperationRefusal { return r }
	person := refused(gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_OWNER_NOT_SERVICE)
	notInspected := refused(gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_NOT_INSPECTED)
	notShape := refused(gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_NOT_OPERATION_CONTEXT)
	admitted := gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_UNSPECIFIED
	human, svc := business.PrincipalKindHuman, business.PrincipalKindService
	return []admissionRow{
		// A person's own session, and every re-mint of it.
		{"StartTask", "WorkContextService/StartTask", human, person, fromSealPin("StartTask")},
		{"StartRootSession", "WorkContextService/StartRootSession", human, person, fromSealPin("StartRootSession")},
		{"ExchangeAudience", "WorkContextService/ExchangeAudience", human, person, fromSealPin("ExchangeAudience")},
		{"RenewWorkContext", "WorkContextService/RenewWorkContext", human, person, fromSealPin("RenewWorkContext")},
		{"StartChildSession", "WorkContextService/StartChildSession", human, person, fromSealPin("StartChildSession for the parent's audience")},
		// A module exchanging a person's capability, in every binding mode.
		{"operation exchange, plain binding", "ModuleCapabilitiesService/ExchangeDelegatedOperationAudience", human, person,
			fromSealPin("a module's exchange through its operation binding")},
		{"read exchange, plain binding", "ModuleCapabilitiesService/ExchangeDelegatedReadAudience", human, person,
			fromSealPin("a module's read exchange through its read binding")},
		{"operation exchange, content_read, invoke", "ModuleCapabilitiesService/ExchangeDelegatedOperationAudience", human, person,
			installedExchange("evidence", "documents", false)},
		{"operation exchange, content_read, lookup", "ModuleCapabilitiesService/ExchangeDelegatedOperationAudience", human, person,
			installedExchange("evidence", "documents", true)},
		{"operation exchange, module_actor, invoke", "ModuleCapabilitiesService/ExchangeDelegatedOperationAudience", human, person,
			installedExchange("act", "compute", false)},
		{"operation exchange, module_actor, lookup", "ModuleCapabilitiesService/ExchangeDelegatedOperationAudience", human, person,
			installedExchange("act", "compute", true)},
		{"operation exchange, source delegation reference", "ModuleCapabilitiesService/ExchangeDelegatedOperationAudience", human, person,
			func(t *testing.T) (*WorkContextAuthorityServer, string, string) {
				installDelegationBindingService(t)
				issued, err := exchangeByDelegation(presentedAs(t, identityOf(t, "ingestservice")), "ingest")
				require.NoError(t, err)
				return workContextSingleton, "reports", issued.GetToken()
			}},
		{"operation exchange, source delegation parent", "ModuleCapabilitiesService/ExchangeDelegatedOperationAudience", human, person,
			func(t *testing.T) (*WorkContextAuthorityServer, string, string) {
				installDelegationBindingService(t)
				// A binding no wider than the delegation's own hop, so the
				// re-mint attenuates rather than widens.
				registry, err := moduleregistrytest.Parse(delegationBindingPrincipals)
				require.NoError(t, err)
				target := registry[business.ModulePrincipalID("ingestservice")]
				target.OperationAudiences["ingest-write"] = business.ModuleOperationAudience{Audience: "reports",
					InvokeScopes: []business.ModuleOperationScope{{ResourceKind: "collections", Actions: []string{"write"}}}}
				service.SetModuleCapabilities(nil, nil, registry)
				parent, err := mintSourceContext("docstore", moduleToken(t, "docstore"), business.SourceDelegationRef{DelegationID: delegationID})
				require.NoError(t, err)
				issued, err := ModuleCapabilitiesSingleton().ExchangeDelegatedOperationAudience(presentedAs(t, identityOf(t, "ingestservice")),
					&gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: "ingest-write", ParentWorkContextToken: parent.GetToken()})
				require.NoError(t, err)
				return workContextSingleton, "reports", issued.GetToken()
			}},
		// Person-owned unattended work: sealed elsewhere by design, still a person's.
		{"source-operation context", "ModuleCapabilitiesService/MintSourceOperationContext", human, person,
			func(t *testing.T) (*WorkContextAuthorityServer, string, string) {
				installSourceDelegationService(t)
				resp, err := mintSourceContext("docstore", moduleToken(t, "docstore"), business.SourceDelegationRef{DelegationID: delegationID})
				require.NoError(t, err)
				return workContextSingleton, "ingestservice", resp.GetToken()
			}},
		{"installation task", "WorkContextService/StartInstallationTask", human, person,
			func(t *testing.T) (*WorkContextAuthorityServer, string, string) {
				server := newInstallationMintServer(t, healthyInstallationFacts(), nil)
				issued, err := server.StartInstallationTask(installInternalContext(), installTaskRequest())
				require.NoError(t, err)
				return server, installTaskRequest().GetAudience(), issued.GetToken()
			}},
		{"installation step", "", human, person,
			func(t *testing.T) (*WorkContextAuthorityServer, string, string) {
				server := newInstallationMintServer(t, healthyInstallationFacts(), nil)
				issued, err := server.signInstallationStep(context.Background(), installTaskRequest(), healthyInstallationFacts(), nil)
				require.NoError(t, err)
				return server, installTaskRequest().GetAudience(), issued.GetToken()
			}},
		// A module's own identity is addressed to the capability surface, which
		// is never a route.
		{"module identity", "ModuleCapabilitiesService/MintModuleWorkContext", svc, notInspected,
			func(t *testing.T) (*WorkContextAuthorityServer, string, string) {
				installModuleWorkContextService(t, documentsPrincipals)
				installModuleWorkContextAuthority(t)
				resp, err := mintModuleWorkContext(t, "documents", moduleToken(t, "documents"))
				require.NoError(t, err)
				return workContextSingleton, ModuleWorkContextAudience, resp.GetToken()
			}},
		{"installer token route", "", svc, notInspected,
			func(t *testing.T) (*WorkContextAuthorityServer, string, string) {
				for _, entry := range mintEntrypoints() {
					if entry.name != "installer token route" {
						continue
					}
					prefix, mint := entry.install(t)
					token, err := mint(moduleToken(t, prefix))
					require.NoError(t, err)
					return workContextSingleton, ModuleWorkContextAudience, token
				}
				t.Fatal("no installer token route entrypoint")
				return nil, "", ""
			}},
		{"module identity at a module route", "", svc, notInspected,
			func(t *testing.T) (*WorkContextAuthorityServer, string, string) {
				installModuleWorkContextService(t, documentsPrincipals)
				installModuleWorkContextAuthority(t)
				resp, err := mintModuleWorkContext(t, "documents", moduleToken(t, "documents"))
				require.NoError(t, err)
				return workContextSingleton, "modelservice", resp.GetToken()
			}},
		// THE ONE ADMITTED SHAPE.
		{"module operation context", "ModuleCapabilitiesService/MintModuleOperationContext", svc, admitted,
			func(t *testing.T) (*WorkContextAuthorityServer, string, string) {
				installModuleOperationContextService(t, headlessPrincipals)
				installModuleWorkContextAuthority(t)
				resp, err := mintModuleOperationContext("documents", moduleToken(t, "documents"), "model")
				require.NoError(t, err)
				return workContextSingleton, "modelservice", resp.GetToken()
			}},
		{"module operation context at another alias", "", svc, notInspected,
			func(t *testing.T) (*WorkContextAuthorityServer, string, string) {
				installModuleOperationContextService(t, headlessPrincipals)
				installModuleWorkContextAuthority(t)
				resp, err := mintModuleOperationContext("documents", moduleToken(t, "documents"), "model")
				require.NoError(t, err)
				return workContextSingleton, "reportservice", resp.GetToken()
			}},
		{"module operation context for its own module", "", svc, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_OWNER_IS_TARGET,
			func(t *testing.T) (*WorkContextAuthorityServer, string, string) {
				authority, _ := moduleOperationFixture(t)
				authority.Audience = "documents"
				token, _, err := workContextSingleton.StartModuleOperationTask(context.Background(), authority)
				require.NoError(t, err)
				return workContextSingleton, "documents", token
			}},
		{"module operation context sealed to another installation", "", svc, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_SEAL_NOT_OWNERS,
			func(t *testing.T) (*WorkContextAuthorityServer, string, string) {
				authority, _ := moduleOperationFixture(t)
				authority.InstallationID = uuid.NewString()
				token, _, err := workContextSingleton.StartModuleOperationTask(context.Background(), authority)
				require.NoError(t, err)
				return workContextSingleton, "modelservice", token
			}},
		{"service principal no module declares", "", svc, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_SEAL_NOT_OWNERS,
			operationShape(func(r *mintRequest) { r.Start.OwnerPrincipalID = uuid.NewString() })},
		{"operation context shape, single-use", "", svc, notShape,
			operationShape(func(r *mintRequest) { r.Replay = workcontext.ReplaySingleUse })},
		{"operation context shape, longer window", "", svc, notShape,
			operationShape(func(r *mintRequest) { r.TTL = 5 * time.Minute })},
		{"operation context shape, no authority scope", "", svc, notShape,
			operationShape(func(r *mintRequest) { r.Start.AuthorityScopes = nil })},
		{"operation context shape, with an actor hop", "", svc, notShape,
			operationShape(func(r *mintRequest) {
				r.Hops = []delegationHop{{
					PrincipalID: r.Start.OwnerPrincipalID, PrincipalKind: business.PrincipalKindService, DelegationID: uuid.NewString(),
					GrantedScopes: []*basev0.WorkScopeV1{{ResourceKind: "modelservice.profiles", Actions: []string{"read"}}},
				}}
			})},
		{"agent owner", "", business.PrincipalKindAgent, person,
			operationShape(func(r *mintRequest) { r.Start.OwnerPrincipalKind = business.PrincipalKindAgent })},
		{"human owner in the operation context shape", "", human, person,
			operationShape(func(r *mintRequest) { r.Start.OwnerPrincipalKind = business.PrincipalKindHuman })},
		{"an owner kind this issuer does not name", "", "Service", person,
			operationShape(func(r *mintRequest) { r.Start.OwnerPrincipalKind = "Service" })},
	}
}

// Guard A: every mint path gets an asserted verdict, and only
// StartModuleOperationTask is admitted.
func TestEveryMintHasAWorkloadOperationVerdict(t *testing.T) {
	admittedRows := 0
	for _, row := range workloadOperationRows() {
		t.Run(row.name, func(t *testing.T) {
			server, alias, token := row.mint(t)
			require.NotEmpty(t, token)
			decoded, err := workcontext.Decode(token)
			require.NoError(t, err)
			claims := decoded.Context()
			require.Equal(t, row.kind, claims.GetOwnerPrincipalKind(), "the row mints the owner kind it claims to")

			answer := admitWorkload(t, server, token, alias)
			if row.want == gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_UNSPECIFIED {
				admittedRows++
				require.True(t, answer.GetAdmitted(), "refused: %s", answer.GetRefusal())
				require.Equal(t, claims.GetTenantId(), answer.GetTenantId())
				require.Equal(t, claims.GetSeal().GetInstallationId(), answer.GetInstallationId())
				require.Equal(t, claims.GetTaskId(), answer.GetTaskId())
				return
			}
			require.False(t, answer.GetAdmitted())
			require.Equal(t, row.want, answer.GetRefusal())
			require.Empty(t, answer.GetTenantId()+answer.GetInstallationId()+answer.GetTaskId(), "a refusal names nothing")
		})
	}
	require.Equal(t, 1, admittedRows, "exactly one shape is admitted")
}

// mintEntrypointRows maps every adapters function that signs a capability
// (calls core's Start or Child, mint or remint) to the rows that exercise it.
var mintEntrypointRows = map[string][]string{
	"mint":                     {"StartTask", "module operation context", "operation context shape, single-use"},
	"remint":                   {"StartRootSession", "ExchangeAudience", "RenewWorkContext", "operation exchange, plain binding", "read exchange, plain binding"},
	"StartTask":                {"StartTask"},
	"StartRootSession":         {"StartRootSession"},
	"exchangeVerifiedParent":   {"ExchangeAudience", "operation exchange, plain binding", "read exchange, plain binding"},
	"RenewWorkContext":         {"RenewWorkContext"},
	"StartChildSession":        {"StartChildSession"},
	"exchangeInstalledBinding": {"operation exchange, content_read, invoke", "operation exchange, module_actor, invoke"},
	"startSourceOperation":     {"source-operation context", "operation exchange, source delegation reference", "operation exchange, source delegation parent"},
	"mintInstallationTask":     {"installation task", "installation step"},
	"StartModuleTask":          {"module identity"},
	// The installer route's handler calls its mint field, StartModuleTask.
	"token":                    {"installer token route"},
	"StartModuleOperationTask": {"module operation context", "module operation context for its own module"},
}

// signingFunctions walks the package's non-test sources and returns every
// function whose body calls a method named Start, Child, mint or remint.
func signingFunctions(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	found := map[string]bool{}
	fset := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		require.NoError(t, err)
		file, err := parser.ParseFile(fset, path, src, 0)
		require.NoError(t, err)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					switch sel.Sel.Name {
					case "Start", "Child", "mint", "remint":
						found[fn.Name.Name] = true
					}
				}
				return true
			})
		}
	}
	return found
}

// ownerKindConstants reads every PrincipalKind* constant business declares.
func ownerKindConstants(t *testing.T) map[string]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join("..", "business", "principals.go"), nil, 0)
	require.NoError(t, err)
	kinds := map[string]bool{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value := spec.(*ast.ValueSpec)
			for i, name := range value.Names {
				if !strings.HasPrefix(name.Name, "PrincipalKind") || i >= len(value.Values) {
					continue
				}
				lit, ok := value.Values[i].(*ast.BasicLit)
				require.True(t, ok, "%s is not a literal", name.Name)
				kind, err := strconv.Unquote(lit.Value)
				require.NoError(t, err)
				kinds[kind] = true
			}
		}
	}
	require.NotEmpty(t, kinds)
	return kinds
}

// Guard B: the table above is complete. It fails when a saas.accounts.v1 RPC
// answers a token no row mints through, when an adapters function starts
// signing capabilities without a row, and when business declares an owner kind
// no row mints.
func TestWorkloadOperationTableNamesEveryMintAndOwnerKind(t *testing.T) {
	rows := map[string]admissionRow{}
	rpcs := map[string]bool{}
	kinds := map[string]bool{}
	for _, row := range workloadOperationRows() {
		require.NotContains(t, rows, row.name, "row names are unique")
		rows[row.name] = row
		if row.rpc != "" {
			rpcs[row.rpc] = true
		}
		kinds[row.kind] = true
	}

	var minting []string
	protoregistry.GlobalFiles.RangeFilesByPackage("saas.accounts.v1", func(file protoreflect.FileDescriptor) bool {
		for i := 0; i < file.Services().Len(); i++ {
			service := file.Services().Get(i)
			for j := 0; j < service.Methods().Len(); j++ {
				method := service.Methods().Get(j)
				if method.Output().Fields().ByName("token") != nil {
					minting = append(minting, string(service.Name())+"/"+string(method.Name()))
				}
			}
		}
		return true
	})
	require.NotEmpty(t, minting)
	sort.Strings(minting)
	for _, rpc := range minting {
		require.True(t, rpcs[rpc], "%s answers a token and no row mints through it: add one to workloadOperationRows", rpc)
	}
	for rpc := range rpcs {
		require.Contains(t, minting, rpc, "a row names %s, which answers no token", rpc)
	}

	signing := signingFunctions(t)
	for fn := range signing {
		named, listed := mintEntrypointRows[fn]
		require.True(t, listed, "%s signs a capability and mintEntrypointRows does not name it: add a row that mints through it", fn)
		for _, name := range named {
			require.Contains(t, rows, name, "%s names row %q, which does not exist", fn, name)
		}
	}
	for fn := range mintEntrypointRows {
		require.True(t, signing[fn], "mintEntrypointRows names %s, which no longer signs a capability", fn)
	}

	for kind := range ownerKindConstants(t) {
		require.True(t, kinds[kind], "owner kind %q has no row: say what the arm answers for it", kind)
	}
}

// AdmitWorkloadOperation consumes nothing. A single-use capability it was shown
// any number of times still verifies once at its callee, and only once; an
// admitted operation context still verifies at its callee afterwards.
func TestAdmitWorkloadOperationConsumesNothing(t *testing.T) {
	t.Run("single-use", func(t *testing.T) {
		exchangeInstallationService(t)
		server := newCoreTestServer(t, &workContextAuthorityFake{facts: &business.WorkContextAuthorityFacts{OrganizationRevision: 12}}, newSealAnswersFake(12))
		token, _ := mintTestCapability(t, server, testCapability{
			Audience: "documents", Tenant: renewOrgID, Owner: renewActorID, Replay: workcontext.ReplaySingleUse,
			Scopes: []*basev0.WorkScopeV1{{ResourceKind: "evidence", Actions: []string{"read"}}},
		})
		for range 3 {
			answer := admitWorkload(t, server, token, "documents")
			require.Equal(t, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_OWNER_NOT_SERVICE, answer.GetRefusal(),
				"the shape check ran, so the capability was inspected and read")
		}
		_, err := server.verifyFor(context.Background(), token, "documents")
		require.NoError(t, err, "the callee's one use is still there")
		_, err = server.verifyFor(context.Background(), token, "documents")
		require.ErrorIs(t, err, workcontext.ErrReplayed, "and it was single-use: the callee spent it, admission did not")
	})
	t.Run("admitted operation context", func(t *testing.T) {
		installModuleOperationContextService(t, headlessPrincipals)
		installModuleWorkContextAuthority(t)
		resp, err := mintModuleOperationContext("documents", moduleToken(t, "documents"), "model")
		require.NoError(t, err)
		for range 3 {
			require.True(t, admitWorkload(t, workContextSingleton, resp.GetToken(), "modelservice").GetAdmitted())
		}
		_, err = workContextSingleton.verifyFor(context.Background(), resp.GetToken(), "modelservice")
		require.NoError(t, err)
	})
}

// The RPC is internal-tier and fails closed: no internal credential is
// Unauthenticated, an unconfigured issuer or a live read that cannot answer is
// Unavailable, and neither is an admission.
func TestAdmitWorkloadOperationIsInternalAndFailsClosed(t *testing.T) {
	installModuleOperationContextService(t, headlessPrincipals)
	installModuleWorkContextAuthority(t)
	resp, err := mintModuleOperationContext("documents", moduleToken(t, "documents"), "model")
	require.NoError(t, err)
	request := &gen.AdmitWorkloadOperationRequest{WorkContext: resp.GetToken(), Alias: "modelservice"}

	SetInternalToken(admissionTestToken)
	t.Cleanup(func() { SetInternalToken("") })
	_, err = workContextSingleton.AdmitWorkloadOperation(context.Background(), request)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	bearer := stampVerifiedIdentity(context.Background(), readOwner, readOrg, auth.Assurance{})
	_, err = workContextSingleton.AdmitWorkloadOperation(bearer, request)
	require.Equal(t, codes.Unauthenticated, status.Code(err), "a person's identity is not the internal credential")

	unconfigured := &WorkContextAuthorityServer{}
	_, err = unconfigured.AdmitWorkloadOperation(admissionContext(t), request)
	require.Equal(t, codes.Unavailable, status.Code(err))

	service.SetModuleAuthorityReads(unavailableModuleAuthority{currentModuleAuthority{service: service}}, nil)
	answer, err := workContextSingleton.AdmitWorkloadOperation(admissionContext(t), request)
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Nil(t, answer)
}

// unavailableModuleAuthority is a live authority read that cannot answer.
type unavailableModuleAuthority struct{ currentModuleAuthority }

func (unavailableModuleAuthority) LiveModuleAuthority(context.Context, string, string, string) (*business.LiveModuleAuthority, error) {
	return nil, errors.New("authority read unavailable")
}
