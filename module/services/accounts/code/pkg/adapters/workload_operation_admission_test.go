package adapters

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

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

// admissionTarget is the route's target every row asks about, and
// admissionTargetRow the tenant's ACTIVE installation of it: a row that is
// none of the minted capabilities' seals.
const (
	admissionTarget    = "019f6bf7-7777-7777-8777-777777777777"
	admissionTargetRow = "019f6bf7-8888-7888-8888-888888888888"
)

// installTargetRows answers the tenant's ACTIVE installations of
// admissionTarget with rows, for the rest of the test.
func installTargetRows(t *testing.T, rows ...string) {
	t.Helper()
	previous := workloadTargetInstallations
	t.Cleanup(func() { workloadTargetInstallations = previous })
	workloadTargetInstallations = func(_ context.Context, _, target string) ([]string, error) {
		if target != admissionTarget {
			return nil, nil
		}
		return rows, nil
	}
}

// admitWorkload asks the issuer about token at alias, for admissionTarget. A
// test that has not said which rows the target has gets admissionTargetRow.
func admitWorkload(t *testing.T, server *WorkContextAuthorityServer, token, alias string) *gen.AdmitWorkloadOperationResponse {
	t.Helper()
	if workloadTargetInstallations == nil {
		installTargetRows(t, admissionTargetRow)
	}
	answer, err := server.AdmitWorkloadOperation(admissionContext(t),
		&gen.AdmitWorkloadOperationRequest{WorkContext: token, Alias: alias, TargetId: admissionTarget})
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
		{"module operation context after its module was reinstalled", "", svc, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_NOT_CURRENT,
			func(t *testing.T) (*WorkContextAuthorityServer, string, string) {
				authority, _ := moduleOperationFixture(t)
				// Minted and read back against the business service's live
				// module authority, which holds the principal to its one
				// installation as the store does; between the two the module
				// is reinstalled, so the capability no longer describes live
				// state (the read-back finds its revision superseded).
				live := &sealedModuleAuthority{currentModuleAuthority: currentModuleAuthority{service: service}, installation: "installation-current"}
				service.SetModuleAuthorityReads(live, nil)
				workContextSingleton.seals = service
				authority.InstallationID = "installation-current"
				token, _, err := workContextSingleton.StartModuleOperationTask(context.Background(), authority)
				require.NoError(t, err)
				live.installation = "installation-replaced"
				return workContextSingleton, "modelservice", token
			}},
		{"module operation context after its module was uninstalled", "", svc, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_NOT_CURRENT,
			func(t *testing.T) (*WorkContextAuthorityServer, string, string) {
				authority, _ := moduleOperationFixture(t)
				token, _, err := workContextSingleton.StartModuleOperationTask(context.Background(), authority)
				require.NoError(t, err)
				// Read back against the business service, whose live module
				// authority now holds no installation for the principal.
				service.SetModuleAuthorityReads(uninstalledModuleAuthority{currentModuleAuthority{service: service}}, nil)
				workContextSingleton.seals = service
				return workContextSingleton, "modelservice", token
			}},
		{"module operation context after its module's tenant changed", "", svc, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_NOT_CURRENT,
			func(t *testing.T) (*WorkContextAuthorityServer, string, string) {
				authority, _ := moduleOperationFixture(t)
				token, _, err := workContextSingleton.StartModuleOperationTask(context.Background(), authority)
				require.NoError(t, err)
				moved, err := moduleregistrytest.Parse(strings.Replace(headlessPrincipals, moduleWorkContextTenant, "66666666-6666-4666-8666-666666666666", 1))
				require.NoError(t, err)
				service.SetModuleCapabilities(nil, nil, moved)
				workContextSingleton.seals = service
				return workContextSingleton, "modelservice", token
			}},
		{"module operation context, target not installed", "", svc, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_TARGET_NOT_INSTALLED,
			func(t *testing.T) (*WorkContextAuthorityServer, string, string) {
				installTargetRows(t)
				installModuleOperationContextService(t, headlessPrincipals)
				installModuleWorkContextAuthority(t)
				resp, err := mintModuleOperationContext("documents", moduleToken(t, "documents"), "model")
				require.NoError(t, err)
				return workContextSingleton, "modelservice", resp.GetToken()
			}},
		{"module operation context sealed to one of the target's rows", "", svc, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_SEAL_ON_TARGET,
			func(t *testing.T) (*WorkContextAuthorityServer, string, string) {
				installModuleOperationContextService(t, headlessPrincipals)
				installModuleWorkContextAuthority(t)
				resp, err := mintModuleOperationContext("documents", moduleToken(t, "documents"), "model")
				require.NoError(t, err)
				decoded, err := workcontext.Decode(resp.GetToken())
				require.NoError(t, err)
				installTargetRows(t, admissionTargetRow, decoded.Seal().GetInstallationId())
				return workContextSingleton, "modelservice", resp.GetToken()
			}},
		{"a capability whose revision moved since it was minted", "", human, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_NOT_CURRENT,
			func(t *testing.T) (*WorkContextAuthorityServer, string, string) {
				exchangeInstallationService(t)
				seals := newSealAnswersFake(12)
				server := newCoreTestServer(t, &workContextAuthorityFake{facts: &business.WorkContextAuthorityFacts{OrganizationRevision: 12}}, seals)
				token, _ := mintTestCapability(t, server, testCapability{
					Audience: "documents", Tenant: renewOrgID, Owner: renewActorID,
					Scopes: []*basev0.WorkScopeV1{{ResourceKind: "evidence", Actions: []string{"read"}}},
				})
				seals.revision = 13
				return server, "documents", token
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

// mintEntrypointRows maps every adapters declaration that signs a capability to
// the rows that exercise it, keyed by receiver type and name ("var name" for a
// package-level variable). A declaration signs when it references a method of
// core's *workcontext.Authority other than ReadBack (Start, Child, Grant, as a
// call, a method value or a method expression), or this server's own mint or
// remint.
var mintEntrypointRows = map[string][]string{
	"(*WorkContextAuthorityServer).mint":                     {"StartTask", "module operation context", "operation context shape, single-use"},
	"(*WorkContextAuthorityServer).remint":                   {"StartRootSession", "ExchangeAudience", "RenewWorkContext", "operation exchange, plain binding", "read exchange, plain binding"},
	"(*WorkContextAuthorityServer).StartTask":                {"StartTask"},
	"(*WorkContextAuthorityServer).StartRootSession":         {"StartRootSession"},
	"(*WorkContextAuthorityServer).exchangeVerifiedParent":   {"ExchangeAudience", "operation exchange, plain binding", "read exchange, plain binding"},
	"(*WorkContextAuthorityServer).RenewWorkContext":         {"RenewWorkContext"},
	"(*WorkContextAuthorityServer).StartChildSession":        {"StartChildSession"},
	"(*WorkContextAuthorityServer).exchangeInstalledBinding": {"operation exchange, content_read, invoke", "operation exchange, module_actor, invoke"},
	"(*WorkContextAuthorityServer).startSourceOperation":     {"source-operation context", "operation exchange, source delegation reference", "operation exchange, source delegation parent"},
	"(*WorkContextAuthorityServer).mintInstallationTask":     {"installation task", "installation step"},
	"(*WorkContextAuthorityServer).StartModuleTask":          {"module identity", "installer token route"},
	"(*WorkContextAuthorityServer).StartModuleOperationTask": {"module operation context", "module operation context for its own module"},
}

// isSigningMethod reports the methods a signing function references: every method
// of core's Authority except the read-back, and this server's own two signing
// helpers.
func isSigningMethod(fn *types.Func) bool {
	recv := fn.Type().(*types.Signature).Recv()
	if recv == nil {
		return false
	}
	target := types.Unalias(recv.Type())
	if pointer, ok := target.(*types.Pointer); ok {
		target = types.Unalias(pointer.Elem())
	}
	typeName, ok := target.(*types.Named)
	if !ok {
		return false
	}
	object := typeName.Obj()
	switch {
	case object.Pkg() != nil && object.Pkg().Path() == "github.com/codefly-dev/core/workcontext" && object.Name() == "Authority":
		return fn.Name() != "ReadBack"
	case object.Pkg() != nil && object.Pkg().Path() == "accounts/pkg/adapters" && object.Name() == "WorkContextAuthorityServer":
		return fn.Name() == "mint" || fn.Name() == "remint"
	}
	return false
}

// signingFunctions type-checks the package's non-test sources and returns
// every function that references a signing method, by the type the method is
// declared on rather than by its name: a call, a method value bound to a
// variable and a method expression all count.
func signingFunctions(t *testing.T) map[string]bool {
	t.Helper()
	// Export data for every dependency, from the go command, so the check
	// resolves imports exactly as the build does.
	cmd := exec.Command("go", "list", "-export", "-deps", "-f", "{{.ImportPath}}\t{{.Export}}", ".")
	cmd.Env = append(os.Environ(), "GOWORK=off")
	listed, err := cmd.Output()
	require.NoError(t, err, "go list -export")
	exports := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(listed)), "\n") {
		path, export, _ := strings.Cut(line, "\t")
		exports[path] = export
	}
	fset := token.NewFileSet()
	paths, err := filepath.Glob("*.go")
	require.NoError(t, err)
	var files []*ast.File
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		require.NoError(t, err)
		files = append(files, file)
	}
	info := &types.Info{Selections: map[*ast.SelectorExpr]*types.Selection{}, Uses: map[*ast.Ident]types.Object{}}
	config := types.Config{Importer: importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		export, ok := exports[path]
		if !ok || export == "" {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(export)
	})}
	_, err = config.Check("accounts/pkg/adapters", fset, files, info)
	require.NoError(t, err)

	found := map[string]bool{}
	signs := func(node ast.Node) bool {
		signing := false
		ast.Inspect(node, func(node ast.Node) bool {
			sel, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			var method *types.Func
			if selection, ok := info.Selections[sel]; ok {
				method, _ = selection.Obj().(*types.Func)
			} else {
				method, _ = info.Uses[sel.Sel].(*types.Func)
			}
			if method != nil && isSigningMethod(method) {
				signing = true
			}
			return true
		})
		return signing
	}
	for _, file := range files {
		for _, decl := range file.Decls {
			switch decl := decl.(type) {
			case *ast.FuncDecl:
				if decl.Body == nil || !signs(decl.Body) {
					continue
				}
				name := decl.Name.Name
				if decl.Recv != nil && len(decl.Recv.List) == 1 {
					name = "(" + types.ExprString(decl.Recv.List[0].Type) + ")." + name
				}
				found[name] = true
			case *ast.GenDecl:
				for _, spec := range decl.Specs {
					value, ok := spec.(*ast.ValueSpec)
					if !ok || !signs(value) {
						continue
					}
					for _, name := range value.Names {
						found["var "+name.Name] = true
					}
				}
			}
		}
	}
	return found
}

// serviceParentRow presents a service principal's capability, shaped like a
// module operation context, as the parent of a re-mint, and pins the verdict:
// every re-mint refuses it at the mint. The owner a re-mint resolves current
// authority for must hold an organization membership and authorization
// revision, which a declared module principal does not. The two delegated
// exchanges are also run against PostgreSQL
// (pkg/infra/service_parent_exchange_test.go), the store this fixture's fake
// answers for.
type serviceParentRow struct {
	rpc  string
	code codes.Code
	mint func(t *testing.T) (*WorkContextAuthorityServer, string, error)
}

// serviceParent is a service principal's capability for audience, sealed to
// installation: the module operation context's shape, owned by module.
func serviceParent(t *testing.T, server *WorkContextAuthorityServer, tenant, module, audience, installation string, scopes ...*basev0.WorkScopeV1) string {
	t.Helper()
	if len(scopes) == 0 {
		scopes = []*basev0.WorkScopeV1{{ResourceKind: "evidence", Actions: []string{"read"}}}
	}
	token, _ := mintTestCapability(t, server, testCapability{
		Audience: audience, Tenant: tenant, Owner: business.ModulePrincipalID(module), OwnerKind: business.PrincipalKindService,
		Scopes:       scopes,
		Installation: installation, TTL: business.ModuleOperationContextTTL,
	})
	return token
}

func serviceParentRows() []serviceParentRow {
	// The caller is the module principal itself: the most a re-mint could be
	// asked by the owner of the parent.
	owner := func() context.Context {
		return stampVerifiedIdentity(context.Background(), business.ModulePrincipalID("tool"), renewOrgID, auth.Assurance{})
	}
	renewServer := func(t *testing.T) (*WorkContextAuthorityServer, string) {
		exchangeInstallationService(t)
		server := newRenewTestServer(t)
		return server, serviceParent(t, server, renewOrgID, "tool", "tool.test", testInstallation("tool.test"))
	}
	return []serviceParentRow{
		{rpc: "WorkContextService/StartRootSession", code: codes.PermissionDenied, mint: func(t *testing.T) (*WorkContextAuthorityServer, string, error) {
			server, parent := renewServer(t)
			token, err := sealPinIssued(server.StartRootSession(owner(), &gen.StartRootSessionWorkContextRequest{
				OrgId: renewOrgID, ParentWorkContextToken: parent, SessionId: renewSession, Audience: "documents", TtlSeconds: 60,
			}))
			return server, token, err
		}},
		{rpc: "WorkContextService/ExchangeAudience", code: codes.PermissionDenied, mint: func(t *testing.T) (*WorkContextAuthorityServer, string, error) {
			server, parent := renewServer(t)
			token, err := sealPinIssued(server.ExchangeAudience(owner(), &gen.ExchangeWorkContextAudienceRequest{
				OrgId: renewOrgID, ParentWorkContextToken: parent, Audience: "documents", TtlSeconds: 60,
				AttenuatedScopes: []*gen.WorkContextScope{{ResourceKind: "evidence", Actions: []string{"read"}}},
			}))
			return server, token, err
		}},
		{rpc: "WorkContextService/StartChildSession", code: codes.PermissionDenied, mint: func(t *testing.T) (*WorkContextAuthorityServer, string, error) {
			server, parent := renewServer(t)
			token, err := sealPinIssued(server.StartChildSession(owner(), sealPinChildRequest(parent, "tool.test")))
			return server, token, err
		}},
		{rpc: "WorkContextService/RenewWorkContext", code: codes.PermissionDenied, mint: func(t *testing.T) (*WorkContextAuthorityServer, string, error) {
			server, parent := renewServer(t)
			token, err := sealPinIssued(server.RenewWorkContext(owner(), &gen.RenewWorkContextRequest{
				OrgId: renewOrgID, ParentWorkContextToken: parent, TtlSeconds: 60,
			}))
			return server, token, err
		}},
		{rpc: "ModuleCapabilitiesService/ExchangeDelegatedOperationAudience", code: codes.FailedPrecondition, mint: func(t *testing.T) (*WorkContextAuthorityServer, string, error) {
			_, _, client, _ := sourceReadFixture(t)
			service.SetModuleAuthorityReads(currentModuleAuthority{service: service}, nil)
			service.SetModuleCapabilities(nil, nil, installedOperationRegistry(true))
			// A declared module's own capability, addressed to the exchanging one.
			parent := serviceParent(t, workContextSingleton, readOrg, "example", "example", "installation-example",
				&basev0.WorkScopeV1{ResourceKind: "results", Actions: []string{"read", "write"}})
			req := connect.NewRequest(&gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: "generate", ParentWorkContextToken: parent})
			req.Header().Set(workContextHeader, mintModuleIdentity(t, business.ModulePrincipalID("example"), readOrg))
			req.Header().Set("x-codefly-internal-token", "source-read-test-perimeter")
			out, err := client.ExchangeDelegatedOperationAudience(context.Background(), req)
			if err != nil {
				return workContextSingleton, "", err
			}
			return workContextSingleton, out.Msg.GetToken(), nil
		}},
		{rpc: "ModuleCapabilitiesService/ExchangeDelegatedReadAudience", code: codes.FailedPrecondition, mint: func(t *testing.T) (*WorkContextAuthorityServer, string, error) {
			_, _, client, _ := sourceReadFixture(t)
			installReadBinding()
			// A declared module's own capability, addressed to the exchanging one.
			parent := serviceParent(t, workContextSingleton, readOrg, "example", "example", "installation-example",
				&basev0.WorkScopeV1{ResourceKind: "results", Actions: []string{"read", "write"}})
			out, err := client.ExchangeDelegatedReadAudience(context.Background(), readExchangeRequest(t, parent))
			if err != nil {
				return workContextSingleton, "", err
			}
			return workContextSingleton, out.Msg.GetToken(), nil
		}},
	}
}

// A module operation context presented to every re-mint is refused at the mint.
func TestEveryRemintOfAServicePrincipalsParentHasAVerdict(t *testing.T) {
	for _, row := range serviceParentRows() {
		t.Run(row.rpc, func(t *testing.T) {
			_, token, err := row.mint(t)
			require.Error(t, err)
			code := status.Code(err)
			if code == codes.Unknown {
				// The module exchanges answer through the Connect client.
				code = codes.Code(connect.CodeOf(err))
			}
			require.Equal(t, row.code, code, "%v", err)
			require.Empty(t, token)
		})
	}
}

// accountsMethods lists "Service/Method" for every saas.accounts.v1 method
// matching keep, sorted.
func accountsMethods(keep func(protoreflect.MethodDescriptor) bool) []string {
	var out []string
	protoregistry.GlobalFiles.RangeFilesByPackage("saas.accounts.v1", func(file protoreflect.FileDescriptor) bool {
		for i := 0; i < file.Services().Len(); i++ {
			service := file.Services().Get(i)
			for j := 0; j < service.Methods().Len(); j++ {
				if method := service.Methods().Get(j); keep(method) {
					out = append(out, string(service.Name())+"/"+string(method.Name()))
				}
			}
		}
		return true
	})
	sort.Strings(out)
	return out
}

// capabilityFields are the field names a signed capability travels under.
var capabilityFields = map[protoreflect.Name]bool{"token": true, "work_context": true, "work_context_token": true}

// answersCapability reports whether a message carries a signed capability
// anywhere inside it: IssuedWorkContext, or a string field of an accounts
// message named as one, at any depth. Another package's field of the same name
// is not this issuer's capability (saas.jobs.v1.Job's token is a lease id).
func answersCapability(message protoreflect.MessageDescriptor) bool {
	return containsField(message, map[protoreflect.FullName]bool{}, func(field protoreflect.FieldDescriptor) bool {
		return field.Kind() == protoreflect.StringKind && capabilityFields[field.Name()] &&
			field.ContainingMessage().ParentFile().Package() == "saas.accounts.v1"
	}, "saas.accounts.v1.IssuedWorkContext")
}

// takesParent reports whether a message carries a parent capability at any depth.
func takesParent(message protoreflect.MessageDescriptor) bool {
	return containsField(message, map[protoreflect.FullName]bool{}, func(field protoreflect.FieldDescriptor) bool {
		return field.Name() == "parent_work_context_token"
	}, "")
}

func containsField(message protoreflect.MessageDescriptor, seen map[protoreflect.FullName]bool,
	match func(protoreflect.FieldDescriptor) bool, wanted protoreflect.FullName,
) bool {
	if message.FullName() == wanted {
		return true
	}
	if seen[message.FullName()] {
		return false
	}
	seen[message.FullName()] = true
	fields := message.Fields()
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		if match(field) {
			return true
		}
		if field.Message() != nil && containsField(field.Message(), seen, match, wanted) {
			return true
		}
	}
	return false
}

// The capability detection reaches through nesting: a response that wraps
// IssuedWorkContext, or a capability field, in another message is found.
func TestCapabilityDetectionIsTransitive(t *testing.T) {
	issued, err := protoregistry.GlobalFiles.FindDescriptorByName("saas.accounts.v1.IssuedWorkContext")
	require.NoError(t, err)
	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:       proto.String("saas/accounts/v1/zz_capability_detection.proto"),
		Package:    proto.String("saas.accounts.v1"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{issued.ParentFile().Path()},
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("ZzDetectionWrapped"), Field: []*descriptorpb.FieldDescriptorProto{{
				Name: proto.String("issued"), Number: proto.Int32(1), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: proto.String(".saas.accounts.v1.IssuedWorkContext"),
			}}},
			{Name: proto.String("ZzDetectionInner"), Field: []*descriptorpb.FieldDescriptorProto{{
				Name: proto.String("work_context"), Number: proto.Int32(1), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			}, {
				Name: proto.String("parent_work_context_token"), Number: proto.Int32(2), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			}}},
			{Name: proto.String("ZzDetectionOuter"), Field: []*descriptorpb.FieldDescriptorProto{{
				Name: proto.String("inner"), Number: proto.Int32(1), Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
				Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: proto.String(".saas.accounts.v1.ZzDetectionInner"),
			}}},
			{Name: proto.String("ZzDetectionPlain"), Field: []*descriptorpb.FieldDescriptorProto{{
				Name: proto.String("next_page_token"), Number: proto.Int32(1), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			}}},
		},
	}, protoregistry.GlobalFiles)
	require.NoError(t, err)
	messages := file.Messages()
	require.True(t, answersCapability(messages.ByName("ZzDetectionWrapped")), "IssuedWorkContext one level down")
	require.True(t, answersCapability(messages.ByName("ZzDetectionOuter")), "a capability field two levels down")
	require.True(t, takesParent(messages.ByName("ZzDetectionOuter")), "a parent field two levels down")
	require.False(t, answersCapability(messages.ByName("ZzDetectionPlain")))
	require.False(t, takesParent(messages.ByName("ZzDetectionPlain")))
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

	minting := accountsMethods(func(method protoreflect.MethodDescriptor) bool {
		return answersCapability(method.Output())
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

	// Every re-mint (an RPC that answers a capability and takes a parent) has a
	// service-parent row, and a module's re-mint is also run against
	// PostgreSQL, whose refusal the fake only mirrors.
	parents := map[string]bool{}
	for _, row := range serviceParentRows() {
		parents[row.rpc] = true
	}
	postgres, err := os.ReadFile(filepath.Join("..", "infra", "service_parent_exchange_test.go"))
	require.NoError(t, err)
	remints := accountsMethods(func(method protoreflect.MethodDescriptor) bool {
		return answersCapability(method.Output()) && takesParent(method.Input())
	})
	require.NotEmpty(t, remints)
	for _, rpc := range remints {
		require.True(t, parents[rpc], "%s re-mints a parent and serviceParentRows has no row for it", rpc)
		service, method, _ := strings.Cut(rpc, "/")
		if service == "ModuleCapabilitiesService" {
			require.Contains(t, string(postgres), "."+method+"(", "%s has no PostgreSQL-backed row in pkg/infra/service_parent_exchange_test.go", rpc)
		}
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
	request := &gen.AdmitWorkloadOperationRequest{WorkContext: resp.GetToken(), Alias: "modelservice", TargetId: admissionTarget}
	installTargetRows(t, admissionTargetRow)

	SetInternalToken(admissionTestToken)
	t.Cleanup(func() { SetInternalToken("") })
	_, err = workContextSingleton.AdmitWorkloadOperation(context.Background(), request)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	_, err = workContextSingleton.AdmitWorkloadOperation(context.Background(), &gen.AdmitWorkloadOperationRequest{})
	require.Equal(t, codes.Unauthenticated, status.Code(err), "the credential is checked before the request is validated")
	bearer := stampVerifiedIdentity(context.Background(), readOwner, readOrg, auth.Assurance{})
	_, err = workContextSingleton.AdmitWorkloadOperation(bearer, request)
	require.Equal(t, codes.Unauthenticated, status.Code(err), "a person's identity is not the internal credential")

	unconfigured := &WorkContextAuthorityServer{}
	_, err = unconfigured.AdmitWorkloadOperation(admissionContext(t), request)
	require.Equal(t, codes.Unavailable, status.Code(err))

	// The target's installations cannot be read.
	workloadTargetInstallations = func(context.Context, string, string) ([]string, error) {
		return nil, errors.New("installations unavailable")
	}
	answer, err := workContextSingleton.AdmitWorkloadOperation(admissionContext(t), request)
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Nil(t, answer)

	// The read-back's live seal cannot be read: not a refusal of the
	// capability, so not an answer either.
	installTargetRows(t, admissionTargetRow)
	service.SetModuleAuthorityReads(unavailableModuleAuthority{currentModuleAuthority{service: service}}, nil)
	workContextSingleton.seals = service
	answer, err = workContextSingleton.AdmitWorkloadOperation(admissionContext(t), request)
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Nil(t, answer)
}

// unavailableModuleAuthority is a live authority read that cannot answer.
type unavailableModuleAuthority struct{ currentModuleAuthority }

func (unavailableModuleAuthority) LiveModuleAuthority(context.Context, string, string, string) (*business.LiveModuleAuthority, error) {
	return nil, errors.New("authority read unavailable")
}

// sealedModuleAuthority answers a module principal's one installation and, as
// the store's read does, nothing for any other installation id.
type sealedModuleAuthority struct {
	currentModuleAuthority
	installation string
}

func (f *sealedModuleAuthority) LiveModuleAuthority(ctx context.Context, principal, org, installation string) (*business.LiveModuleAuthority, error) {
	live, err := f.currentModuleAuthority.LiveModuleAuthority(ctx, principal, org, installation)
	if err != nil {
		return nil, err
	}
	live.InstallationID = f.installation
	if installation != "" && installation != live.InstallationID {
		return nil, business.ErrModuleInstallationInactive
	}
	return live, nil
}

// uninstalledModuleAuthority is a live authority read that finds no ACTIVE
// installation for the principal: the module was uninstalled.
type uninstalledModuleAuthority struct{ currentModuleAuthority }

func (uninstalledModuleAuthority) LiveModuleAuthority(context.Context, string, string, string) (*business.LiveModuleAuthority, error) {
	return nil, business.ErrModuleInstallationInactive
}
