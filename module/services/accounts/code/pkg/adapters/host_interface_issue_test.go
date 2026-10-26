package adapters

import (
	"context"
	"fmt"
	"testing"
	"time"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	accountsauth "accounts/pkg/auth"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
)

// requiringPolicy is a delivered method policy requiring one scope, held as
// the host's delivery check holds it; it records what the call was held to.
type requiringPolicy struct {
	required *basev0.WorkScopeV1
	held     []*basev0.WorkScopeV1
}

func (p *requiringPolicy) CheckDeliveredMethod(_ context.Context, audience, method string, held []*basev0.WorkScopeV1) error {
	p.held = held
	if !workcontext.ScopeContained(p.required, held) {
		return fmt.Errorf("%s requires %s for %s, which the call does not hold", audience, scopeString(p.required), method)
	}
	return nil
}

// A method is held to what the call's current actor was delegated, not to the
// owner's authority above it: an owner holding read and write who delegated
// read alone does not pass a method requiring write through that actor.
func TestCheckHoldsAMethodToWhatTheActorWasDelegated(t *testing.T) {
	issuer, server, _ := hostChildFixture(t)
	policy := &requiringPolicy{required: hopScope("evidence", []string{"write"})}
	server.methods = policy
	owned := hopScope("evidence", []string{"read", "write"})
	delegated, _ := mintTestCapability(t, issuer, testCapability{
		Audience: "documents", Tenant: renewOrgID, Owner: renewActorID, Scopes: []*basev0.WorkScopeV1{owned},
		Actors: []delegationHop{agentHop("actor-1", uuid.NewString(), hopScope("evidence", []string{"read"}))},
	})
	const method = "/example.v1.EvidenceService/Write"

	_, err := server.Check(context.Background(), &authorityv1.CheckRequest{Token: delegated, Audience: "documents", Method: method})
	requireRefused(t, err, codes.PermissionDenied, "requires evidence:write")
	requireScopes(t, []*basev0.WorkScopeV1{hopScope("evidence", []string{"read"})}, policy.held)

	// The owner acting directly holds write, and the same method passes.
	direct, _ := mintTestCapability(t, issuer, testCapability{
		Audience: "documents", Tenant: renewOrgID, Owner: renewActorID, Scopes: []*basev0.WorkScopeV1{owned},
	})
	_, err = server.Check(context.Background(), &authorityv1.CheckRequest{Token: direct, Audience: "documents", Method: method})
	require.NoError(t, err)
}

// workloadPrincipals composes documents, whose workload issues through its
// "model" binding (headless: invoke and read on one profile; lookup: read),
// and the two audiences its bindings name.
const workloadPrincipals = `{"documents":{"workload":{"service_account":"module","namespace":"acme-prod","container":"app"},"tenant":"` + moduleWorkContextTenant + `","operation_audiences":{` +
	`"model":{"audience":"modelservice",` +
	`"invoke_scopes":[{"resource_kind":"modelservice.profiles","actions":["invoke","read"]}],` +
	`"lookup_scopes":[{"resource_kind":"modelservice.profiles","actions":["read"]}],` +
	`"headless_scopes":[{"resource_kind":"modelservice.profiles","actions":["invoke","read"],"resource_ids":["profile-digest"]}]},` +
	`"interactive":{"audience":"reportservice",` +
	`"invoke_scopes":[{"resource_kind":"reports","actions":["read","write"]}]}}},` +
	`"modelservice":{"workload":{"service_account":"model","namespace":"acme-prod","container":"app"},"tenant":"` + moduleWorkContextTenant + `","resources":["modelservice.profiles"]},` +
	`"reportservice":{"workload":{"service_account":"reports","namespace":"acme-prod","container":"app"},"tenant":"` + moduleWorkContextTenant + `","resources":["reports"]}}`

type workloadFixture struct {
	server     *HostAuthorityServer
	credential string
}

func newWorkloadFixture(t *testing.T) workloadFixture {
	t.Helper()
	installModuleOperationContextService(t, workloadPrincipals)
	installModuleWorkContextAuthority(t)
	return workloadFixture{server: NewHostAuthorityServer(WorkContextSingleton(), ModuleCapabilitiesSingleton()), credential: moduleToken(t, "documents")}
}

func (f workloadFixture) issue(req *authorityv1.IssueRequest) (*authorityv1.IssueResponse, error) {
	req.As = &authorityv1.IssueRequest_Workload{Workload: &authorityv1.WorkloadCredential{Credential: f.credential}}
	return f.server.Issue(context.Background(), req)
}

func lifetime(wc *basev0.WorkContextV1) time.Duration {
	return time.Duration(wc.GetExpiresAtUnix()-wc.GetIssuedAtUnix()) * time.Second
}

// A workload's issue is what it asked for, below what its binding grants it:
// a read-only lookup on one profile for five seconds is exactly that, not the
// binding's whole headless grant for a minute.
func TestAWorkloadIssueIsWhatItAskedForWithinItsBinding(t *testing.T) {
	f := newWorkloadFixture(t)

	out, err := f.issue(&authorityv1.IssueRequest{
		OperationBindingId: "model", Operation: "lookup", TtlSeconds: 5,
		Scopes: []*authorityv1.WorkScopeV1{ask("modelservice.profiles", []string{"read"}, "profile-digest")},
	})
	require.NoError(t, err)
	wc := claims(t, out)
	require.Equal(t, "modelservice", wc.GetAudience())
	requireScopes(t, []*basev0.WorkScopeV1{hopScope("modelservice.profiles", []string{"read"}, "profile-digest")}, wc.GetAuthorityScopes())
	require.LessOrEqual(t, lifetime(wc), 5*time.Second)
	_, err = WorkContextSingleton().verifyFor(context.Background(), out.GetToken(), "modelservice")
	require.NoError(t, err)

	// An empty lookup is the binding's lookup within its headless grant.
	out, err = f.issue(&authorityv1.IssueRequest{OperationBindingId: "model", Operation: "lookup"})
	require.NoError(t, err)
	requireScopes(t, []*basev0.WorkScopeV1{hopScope("modelservice.profiles", []string{"read"}, "profile-digest")}, claims(t, out).GetAuthorityScopes())

	// An empty headless ask is the whole headless grant, at the mint's ceiling.
	out, err = f.issue(&authorityv1.IssueRequest{OperationBindingId: "model", TtlSeconds: 3600})
	require.NoError(t, err)
	wc = claims(t, out)
	requireScopes(t, []*basev0.WorkScopeV1{hopScope("modelservice.profiles", []string{"invoke", "read"}, "profile-digest")}, wc.GetAuthorityScopes())
	require.LessOrEqual(t, lifetime(wc), 60*time.Second)
}

// Nothing outside the binding's grant is issued, and every refusal names why.
func TestAWorkloadIssueBeyondItsBindingIsRefusedByName(t *testing.T) {
	f := newWorkloadFixture(t)
	for name, c := range map[string]struct {
		req    *authorityv1.IssueRequest
		code   codes.Code
		reason string
	}{
		"an action the binding does not grant": {&authorityv1.IssueRequest{OperationBindingId: "model",
			Scopes: []*authorityv1.WorkScopeV1{ask("modelservice.profiles", []string{"write"}, "profile-digest")}}, codes.PermissionDenied, "widens what operation binding \"model\" grants it for headless"},
		"the kind beyond the one profile granted": {&authorityv1.IssueRequest{OperationBindingId: "model",
			Scopes: []*authorityv1.WorkScopeV1{ask("modelservice.profiles", []string{"read"})}}, codes.PermissionDenied, "widens"},
		"invoke under a lookup": {&authorityv1.IssueRequest{OperationBindingId: "model", Operation: "lookup",
			Scopes: []*authorityv1.WorkScopeV1{ask("modelservice.profiles", []string{"invoke"}, "profile-digest")}}, codes.PermissionDenied, "for lookup"},
		"an audience the binding does not address": {&authorityv1.IssueRequest{OperationBindingId: "model", Audience: "reportservice"},
			codes.PermissionDenied, `"model" is addressed to modelservice, not reportservice`},
		"invoke, a person's operation": {&authorityv1.IssueRequest{OperationBindingId: "model", Operation: "invoke"},
			codes.InvalidArgument, "invoke is a person's operation"},
		"a lookup the binding does not declare": {&authorityv1.IssueRequest{OperationBindingId: "interactive", Operation: "lookup"},
			codes.PermissionDenied, `"interactive" declares no lookup`},
		"a negative lifetime": {&authorityv1.IssueRequest{OperationBindingId: "model", TtlSeconds: -1},
			codes.InvalidArgument, "not negative"},
		"scopes on its own context": {&authorityv1.IssueRequest{Audience: ModuleWorkContextAudience,
			Scopes: []*authorityv1.WorkScopeV1{ask("modelservice.profiles", []string{"read"})}}, codes.InvalidArgument, "carries no scopes"},
		"an operation on its own context": {&authorityv1.IssueRequest{Audience: ModuleWorkContextAudience, Operation: "lookup"},
			codes.InvalidArgument, "names no operation"},
		"an audience that is not composed": {&authorityv1.IssueRequest{OperationBindingId: "model", Audience: "nowhere"},
			codes.NotFound, `audience "nowhere" is not composed`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.issue(c.req)
			requireRefused(t, err, c.code, c.reason)
		})
	}
}

// A workload's own context is reachable: addressed to the module capability
// surface, which is never a composed module, with the lifetime it asked for.
func TestAWorkloadIssuesItsOwnContext(t *testing.T) {
	f := newWorkloadFixture(t)
	out, err := f.issue(&authorityv1.IssueRequest{Audience: ModuleWorkContextAudience, TtlSeconds: 30})
	require.NoError(t, err)
	wc := claims(t, out)
	require.Equal(t, ModuleWorkContextAudience, wc.GetAudience())
	require.LessOrEqual(t, lifetime(wc), 30*time.Second)
	caller, err := WorkContextSingleton().VerifyModuleWorkContext(context.Background(), out.GetToken())
	require.NoError(t, err)
	require.Equal(t, moduleWorkContextTenant, caller.BoundOrg)
}

// A credential that proves nothing is refused naming why, and a caller proving
// nothing learns nothing of which modules are composed: a bearer-less person
// or exchange is Unauthenticated whatever the audience.
func TestAnIssueAuthenticatesBeforeItAnswersForTheComposition(t *testing.T) {
	f := newWorkloadFixture(t)
	f.credential = "not-a-token"
	_, err := f.issue(&authorityv1.IssueRequest{OperationBindingId: "model"})
	requireRefused(t, err, codes.PermissionDenied, "workload credential refused: the presented token was reviewed and refused")

	for _, audience := range []string{"modelservice", "nowhere"} {
		_, err = f.server.Issue(context.Background(), &authorityv1.IssueRequest{
			As: &authorityv1.IssueRequest_Person{Person: &authorityv1.PersonSession{}}, Audience: audience,
		})
		requireRefused(t, err, codes.Unauthenticated, "signed-in person")
		_, err = f.server.Issue(context.Background(), &authorityv1.IssueRequest{
			As: &authorityv1.IssueRequest_ParentToken{ParentToken: "p"}, Audience: audience,
		})
		requireRefused(t, err, codes.Unauthenticated, "carries no bearer")
	}
}

// An issue from a delegation names the module acting before anything is read.
func TestAnIssueFromADelegationNamesItsActor(t *testing.T) {
	_, err := (&HostAuthorityServer{}).Issue(context.Background(), &authorityv1.IssueRequest{
		As: &authorityv1.IssueRequest_Delegation{Delegation: &authorityv1.DelegationRef{DelegationId: "d"}}, Audience: "documents",
	})
	requireRefused(t, err, codes.Unauthenticated, "names the module acting")
}

// exchangeFixture is the owner of a capability addressed to tool.test, signed
// in, over the interface.
func exchangeFixture(t *testing.T) (*HostAuthorityServer, string, context.Context) {
	t.Helper()
	exchangeInstallationService(t)
	issuer := newRenewTestServer(t)
	parent := personCapability(t, issuer, "tool.test", testInstallation("tool.test"))
	return NewHostAuthorityServer(issuer, nil), parent, exchangeCaller()
}

// An exchange through Issue forwards the organisation, the request's or the
// parent's own, and an empty ask is the parent's own authority.
func TestAnIssueExchangesAParentToAnotherAudience(t *testing.T) {
	for name, req := range map[string]*authorityv1.IssueRequest{
		"scopes asked, organisation named": {Audience: "documents", OrganizationId: renewOrgID,
			Scopes: []*authorityv1.WorkScopeV1{ask("evidence", []string{"read"})}},
		"empty ask, organisation the parent's": {Audience: "documents"},
	} {
		t.Run(name, func(t *testing.T) {
			server, parent, ctx := exchangeFixture(t)
			req.As = &authorityv1.IssueRequest_ParentToken{ParentToken: parent}
			out, err := server.Issue(ctx, req)
			require.NoError(t, err)
			wc := claims(t, out)
			require.Equal(t, "documents", wc.GetAudience())
			require.Equal(t, renewOrgID, wc.GetTenantId())
			require.Equal(t, renewActorID, wc.GetOwnerPrincipalId())
			requireScopes(t, []*basev0.WorkScopeV1{hopScope("evidence", []string{"read"})}, wc.GetAuthorityScopes())
		})
	}
}

// At the parent's own audience the issue is a renewal: the same task, a fresh
// session and window, the authority kept as it stands. An owner's own
// capability is renewed by its owner; a delegated one by its current actor,
// who may narrow it.
func TestAnIssueAtTheParentsAudienceRenewsIt(t *testing.T) {
	server, parent, ctx := exchangeFixture(t)
	delegated := mintDelegatedContext(t, server.workContexts)
	for name, c := range map[string]struct {
		parent string
		asked  []*authorityv1.WorkScopeV1
		want   *basev0.WorkScopeV1
	}{
		"an owner's own":            {parent, nil, hopScope("evidence", []string{"read"})},
		"a delegated one":           {delegated, nil, hopScope("evidence", []string{"append", "read"})},
		"a delegated one, narrowed": {delegated, []*authorityv1.WorkScopeV1{ask("evidence", []string{"read"})}, hopScope("evidence", []string{"read"})},
	} {
		t.Run(name, func(t *testing.T) {
			decoded, err := workcontext.Decode(c.parent)
			require.NoError(t, err)
			out, err := server.Issue(ctx, &authorityv1.IssueRequest{
				As: &authorityv1.IssueRequest_ParentToken{ParentToken: c.parent}, Audience: "tool.test", TtlSeconds: 600, Scopes: c.asked,
			})
			require.NoError(t, err)
			wc := claims(t, out)
			require.Equal(t, "tool.test", wc.GetAudience())
			require.Equal(t, decoded.Context().GetTaskId(), wc.GetTaskId())
			require.NotEqual(t, decoded.Context().GetSessionId(), wc.GetSessionId())
			requireScopes(t, []*basev0.WorkScopeV1{c.want}, effectiveScopes(wc))
			_, err = server.workContexts.verifyFor(context.Background(), out.GetToken(), "tool.test")
			require.NoError(t, err)
		})
	}

	// An owner's renewal keeps its authority: narrowing it is an exchange.
	_, err := server.Issue(ctx, &authorityv1.IssueRequest{
		As: &authorityv1.IssueRequest_ParentToken{ParentToken: parent}, Audience: "tool.test",
		Scopes: []*authorityv1.WorkScopeV1{ask("evidence", []string{"read"})},
	})
	requireRefused(t, err, codes.InvalidArgument, "names no scopes")
	// Another person's bearer renews nothing of it.
	stranger := stampVerifiedIdentity(context.Background(), renewOwnerID, renewOrgID, accountsauth.Assurance{})
	_, err = server.Issue(stranger, &authorityv1.IssueRequest{
		As: &authorityv1.IssueRequest_ParentToken{ParentToken: parent}, Audience: "tool.test",
	})
	require.Error(t, err)
}

// A kind the chain narrowed to nothing stays dropped: it is carried as a
// denial, a scope naming the kind and no action, and no later hop gets any of
// it back; a kind narrowed to two actions on different resources keeps each
// action's own resources; a kind the chain never named is not limited.
func TestAKindTheChainDroppedStaysDropped(t *testing.T) {
	n := parentNarrowings(&basev0.WorkContextV1{
		AuthorityScopes: []*basev0.WorkScopeV1{
			hopScope("tool.items", []string{"read"}), {ResourceKind: "datasets"},
			hopScope("profiles", []string{"read"}, "a"), hopScope("profiles", []string{"invoke"}, "b"),
		},
		ActorChain: []*basev0.WorkActorV1{{GrantedScopes: []*basev0.WorkScopeV1{hopScope("tool.items", []string{"write"})}}},
	})
	require.Empty(t, n.keep([]*basev0.WorkScopeV1{hopScope("tool.items", []string{"read"})}), "read and write meet in nothing")
	require.Empty(t, n.keep([]*basev0.WorkScopeV1{hopScope("datasets", []string{"read"})}), "a denied kind gives nothing back")
	require.Equal(t, []*basev0.WorkScopeV1{hopScope("profiles", []string{"read"}, "a")},
		n.keep([]*basev0.WorkScopeV1{hopScope("profiles", []string{"invoke", "read"}, "a")}), "each action keeps its own resources")
	require.Len(t, n.keep([]*basev0.WorkScopeV1{hopScope("unnamed", []string{"read"})}), 1, "a kind never named is not limited")

	carried := n.outside([]*basev0.WorkScopeV1{hopScope("example-records", []string{"read"})})
	require.Contains(t, carried, &basev0.WorkScopeV1{ResourceKind: "datasets"}, "the denial is carried")
	require.Contains(t, carried, &basev0.WorkScopeV1{ResourceKind: "tool.items"}, "a kind narrowed to nothing is carried as a denial")
	require.Contains(t, carried, hopScope("profiles", []string{"read"}, "a"))
	require.Contains(t, carried, hopScope("profiles", []string{"invoke"}, "b"))

	denied := n.deny([]*basev0.WorkScopeV1{hopScope("example-records", []string{"read"}), hopScope("records.types", []string{"read"})},
		[]*basev0.WorkScopeV1{hopScope("example-records", []string{"read"})})
	require.Empty(t, denied.keep([]*basev0.WorkScopeV1{hopScope("records.types", []string{"read"})}), "a held kind the grant left out is dropped")
}

// The hop's mint runs the structural audience rule every mint runs.
func TestAHopMintRefusesTheModuleCapabilitySurface(t *testing.T) {
	f := newHopFixture(t, "tool.items:read", "example-records:read")
	_, parent := f.parent(t, "tools", hopScope("tool.items", []string{"read"}))
	_, err := f.issuer.mintBindingHop(context.Background(), parent, "tools", "records", ModuleWorkContextAudience,
		[]*basev0.WorkScopeV1{hopScope("example-records", []string{"read"})}, nil, time.Minute, delegatedHop{})
	requireRefused(t, err, codes.PermissionDenied, "module capability surface")
}

// A person's root names, as a denial, every kind of the audience its ask left
// out; a kind it carries is never denied, and nothing is denied without the
// interface's mark.
func TestARootDeniesTheAudienceKindsItsAskLeftOut(t *testing.T) {
	ctx := withDroppedKinds(context.Background(), []string{"datasets", "profiles"})
	carried := []*basev0.WorkScopeV1{hopScope("profiles", []string{"read"}, "a")}
	require.Equal(t, []*basev0.WorkScopeV1{{ResourceKind: "datasets"}}, droppedKindScopes(ctx, carried))
	require.Empty(t, droppedKindScopes(context.Background(), carried))
}
