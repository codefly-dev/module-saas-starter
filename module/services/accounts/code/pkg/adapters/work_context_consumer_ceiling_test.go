package adapters

// THE CEILING OF A CONFINED MINT IS WHAT ITS CONSUMER DECLARES IT CONSUMES
// (issue #1083), and the installation's lists may only narrow it.
//
// The consumer's declaration reaches this host as its registry grant: the
// invoke bindings of its delivered contract, held to the envelope and to the
// organization's installation scope. These tests wire that grant through the
// real seams — the installation's delivered principal read from the store, the
// live authority read, AuthorizeModuleCapability — and run the whole StartTask.

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

const (
	// declaringConsumerPrefix is the consumer's own module principal: the
	// principal its delivered authority is issued to, NOT the installation's
	// per-organization agent.
	declaringConsumerPrefix  = "example-consumer"
	declaringConsumerBinding = "workspace.prod.example-consumer"
)

// declaredConsumption is what the consumer's contract declares it calls on a
// person's behalf: `documents`, where it may read and write evidence and read
// notes, and `rows`, where it may read evidence. It declares nothing at
// `example`, which IS a module this host serves.
func declaredConsumption() map[string]business.ModuleOperationAudience {
	return map[string]business.ModuleOperationAudience{
		"documents": {Audience: "documents", InvokeScopes: []business.ModuleOperationScope{
			{ResourceKind: "evidence", Actions: []string{"read", "write"}},
			{ResourceKind: "notes", Actions: []string{"read"}},
		}},
		"rows": {Audience: "rows", InvokeScopes: []business.ModuleOperationScope{
			{ResourceKind: "evidence", Actions: []string{"read"}},
		}},
	}
}

// consumerFixture is the declaring consumer's state on this host, each piece
// replaceable by the test that is about it.
type consumerFixture struct {
	service *business.Service
	// state is the installation row: its delivered principal and binding.
	state *business.InstallationAuthorityState
	// scope, when set, is the organization's installation scope in place of the
	// full grant; liveErr replaces the live read outright.
	scope   *business.InstallationScope
	liveErr error

	// The authority use span, observed: how many were opened, whether one is
	// open now, and whether the live read and the owner's membership lookup —
	// which comes after the ceiling decision and before the signature — each
	// ran inside one.
	spans       int
	open        bool
	readInSpan  bool
	ownerInSpan bool
}

type authorityUseSpanKey struct{}

func (f *consumerFixture) inSpan(ctx context.Context) bool {
	return f.open && ctx.Value(authorityUseSpanKey{}) != nil
}

func (f *consumerFixture) declare(bindings map[string]business.ModuleOperationAudience) {
	registry := f.service.ModulePrincipals()
	registry[business.ModulePrincipalID(declaringConsumerPrefix)] = business.ModulePrincipalGrant{
		Prefix:             declaringConsumerPrefix,
		PresenceBinding:    declaringConsumerBinding,
		OperationAudiences: bindings,
	}
	f.service.SetModulePrincipals(registry)
}

// declaredConsumerStore reads the installation's delivered principal.
type declaredConsumerStore struct {
	renewMembershipStore
	fixture *consumerFixture
}

func (s declaredConsumerStore) InstallationAuthorityState(
	_ context.Context, _, id string,
) (*business.InstallationAuthorityState, error) {
	if s.fixture.state == nil || id != confinedInstallation {
		return nil, business.NewStoreError(business.ErrModuleInstallationInactive, business.ErrTypeNotFound)
	}
	state := *s.fixture.state
	return &state, nil
}

func (s declaredConsumerStore) GetOrgMembership(ctx context.Context, orgID, userID string) (*gen.OrgMembership, error) {
	s.fixture.ownerInSpan = s.fixture.inSpan(ctx)
	return s.renewMembershipStore.GetOrgMembership(ctx, orgID, userID)
}

func (declaredConsumerStore) NarrowInstallationScope(
	context.Context, string, string, uint64, business.InstallationScope, business.ModulePrincipalRegistry, uint64,
) error {
	return errors.New("a mint never narrows an installation")
}

// declaredConsumerAuthority is the live authority read: the installation the
// perimeter named, current, consenting to the whole grant unless told otherwise.
type declaredConsumerAuthority struct{ fixture *consumerFixture }

func (a declaredConsumerAuthority) LiveModuleAuthority(
	ctx context.Context, principal, _, _ string,
) (*business.LiveModuleAuthority, error) {
	a.fixture.readInSpan = a.fixture.inSpan(ctx)
	if a.fixture.liveErr != nil {
		return nil, a.fixture.liveErr
	}
	grant := a.fixture.service.ModulePrincipals()[principal]
	scope := business.FullInstallationScope(grant)
	if a.fixture.scope != nil {
		scope = *a.fixture.scope
	}
	return &business.LiveModuleAuthority{
		InstallationID: confinedInstallation, RegistryBindingID: grant.PresenceBinding, Scope: scope,
	}, nil
}

// BeginModuleAuthorityUse is re-entrant the way the Postgres store's is: a
// context already carrying a span reuses it, and only the outermost release
// ends it.
func (a declaredConsumerAuthority) BeginModuleAuthorityUse(ctx context.Context) (context.Context, func(), error) {
	if ctx.Value(authorityUseSpanKey{}) != nil {
		return ctx, func() {}, nil
	}
	a.fixture.spans++
	a.fixture.open = true
	return context.WithValue(ctx, authorityUseSpanKey{}, true), func() { a.fixture.open = false }, nil
}

// confinedConsumerService is solutionBoundaryService with the consumer the
// confined credential was delivered to installed and declaring its consumption.
func confinedConsumerService(t *testing.T) *consumerFixture {
	t.Helper()
	previous := service
	fixture := &consumerFixture{state: &business.InstallationAuthorityState{
		Kind:        "solution",
		PrincipalID: business.ModulePrincipalID(declaringConsumerPrefix),
		BindingID:   declaringConsumerBinding,
		Revision:    1,
	}}
	svc, err := business.NewService(declaredConsumerStore{fixture: fixture})
	require.NoError(t, err)
	fixture.service = svc
	declareTestAudiences(svc)
	fixture.declare(declaredConsumption())
	svc.SetModuleAuthorityReads(declaredConsumerAuthority{fixture: fixture}, nil)
	service = svc
	t.Cleanup(func() { service = previous })
	return fixture
}

func confinedMint(t *testing.T, request *gen.StartTaskWorkContextRequest, actor func(*business.Principal)) (*gen.IssuedWorkContext, error) {
	t.Helper()
	withConfiguredOrigin(t)
	server, authority := newSolutionBoundaryServer(t)
	consumer := confinedConsumer()
	if actor != nil {
		actor(consumer.Actor)
	}
	authority.installationFcts = consumer
	return server.StartTask(confinedCallerContext(confinedResource, confinedInstallation), request)
}

func scoped(audience, kind string, actions ...string) *gen.StartTaskWorkContextRequest {
	request := confinedMintRequest(audience)
	request.AuthorityScopes = []*gen.WorkContextScope{{ResourceKind: kind, Actions: actions}}
	return request
}

// THE ISSUE'S OWN TEST: an audience the consumer does not declare is refused
// even when the installation's lists are empty or wider. `example` is a module
// this host serves, so the vocabulary admits it and nothing else refuses it.
func TestConfinedMintForAnUndeclaredAudienceIsRefusedWhateverTheInstallationLists(t *testing.T) {
	for name, lists := range map[string]func(*business.Principal){
		"the installation's lists are empty": func(p *business.Principal) {
			p.AllowedAudiences, p.AllowedScopes = nil, nil
		},
		"the installation's lists are wider": func(p *business.Principal) {
			p.AllowedAudiences = []string{"documents", "rows", "example"}
			p.AllowedScopes = []string{"evidence", "notes", "billing"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			confinedConsumerService(t)

			issued, err := confinedMint(t, confinedMintRequest("example"), lists)

			require.Error(t, err, "nothing may be signed for an audience the consumer never declared")
			require.Nil(t, issued)
			require.Equal(t, codes.PermissionDenied, status.Code(err))
			require.Contains(t, err.Error(), `"example" is not a module the consumer`)
		})
	}
}

// The same for the scopes: a declared audience, asked for a kind or an action
// the consumer does not declare there, refused with wide or empty lists.
func TestConfinedMintOutsideTheDeclaredScopesIsRefusedWhateverTheInstallationLists(t *testing.T) {
	for name, request := range map[string]func() *gen.StartTaskWorkContextRequest{
		"a kind it does not declare at the module":     func() *gen.StartTaskWorkContextRequest { return scoped("documents", "billing", "write") },
		"an action it does not declare on the kind":    func() *gen.StartTaskWorkContextRequest { return scoped("documents", "notes", "write") },
		"a kind it declares only at another module":    func() *gen.StartTaskWorkContextRequest { return scoped("rows", "notes", "read") },
		"an action it declares only at another module": func() *gen.StartTaskWorkContextRequest { return scoped("rows", "evidence", "write") },
	} {
		for lists, actor := range map[string]func(*business.Principal){
			"empty lists": func(p *business.Principal) { p.AllowedAudiences, p.AllowedScopes = nil, nil },
			"wider lists": func(p *business.Principal) {
				p.AllowedAudiences = []string{"documents", "rows", "example"}
				p.AllowedScopes = []string{"evidence", "notes", "billing"}
			},
		} {
			t.Run(name+", "+lists, func(t *testing.T) {
				confinedConsumerService(t)

				issued, err := confinedMint(t, request(), actor)

				require.Error(t, err)
				require.Nil(t, issued)
				require.Equal(t, codes.PermissionDenied, status.Code(err))
				require.Contains(t, err.Error(), "outside what the consumer")
			})
		}
	}
}

// THE CONTROLS. Every declared (audience, scope) is issued — with the
// installation's lists empty, which used to refuse every confined mint and now
// adds no bound, because the declaration is the ceiling.
func TestConfinedMintInsideTheDeclarationIsIssuedWithEmptyInstallationLists(t *testing.T) {
	for name, request := range map[string]func() *gen.StartTaskWorkContextRequest{
		"documents, evidence read":  func() *gen.StartTaskWorkContextRequest { return scoped("documents", "evidence", "read") },
		"documents, evidence write": func() *gen.StartTaskWorkContextRequest { return scoped("documents", "evidence", "write") },
		"documents, notes read":     func() *gen.StartTaskWorkContextRequest { return scoped("documents", "notes", "read") },
		"rows, evidence read":       func() *gen.StartTaskWorkContextRequest { return scoped("rows", "evidence", "read") },
	} {
		t.Run(name, func(t *testing.T) {
			confinedConsumerService(t)

			issued, err := confinedMint(t, request(), func(p *business.Principal) {
				p.AllowedAudiences, p.AllowedScopes = nil, nil
			})

			require.NoError(t, err)
			require.NotEmpty(t, issued.GetToken())
		})
	}
}

// THE INSTALLATION'S LISTS STILL NARROW. Each request here is inside the
// declaration, so only the list can refuse it.
func TestTheInstallationsListsNarrowTheDeclaredCeiling(t *testing.T) {
	t.Run("an audience the declaration names and the list does not", func(t *testing.T) {
		confinedConsumerService(t)

		// confinedConsumer's list names `documents` only.
		issued, err := confinedMint(t, scoped("rows", "evidence", "read"), nil)

		require.Nil(t, issued)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		require.Contains(t, err.Error(), "allowed audiences")
	})
	t.Run("a kind the declaration names and the list does not", func(t *testing.T) {
		confinedConsumerService(t)

		// confinedConsumer's list names `evidence` only.
		issued, err := confinedMint(t, scoped("documents", "notes", "read"), nil)

		require.Nil(t, issued)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		require.Contains(t, err.Error(), "allowed scopes")
	})
}

// The declaration is read AS THIS ORGANIZATION CONSENTED TO IT, not as
// delivered: a narrowed installation scope, a consumer that declares nothing, an
// installation with no delivered authority, and a withdrawn installation each
// refuse a mint the delivered declaration alone would admit.
func TestTheDeclaredCeilingIsReadAsConsented(t *testing.T) {
	inside := func() *gen.StartTaskWorkContextRequest { return scoped("documents", "evidence", "read") }

	t.Run("the installation scope dropped the module", func(t *testing.T) {
		fixture := confinedConsumerService(t)
		narrowed := business.FullInstallationScope(fixture.service.ModulePrincipals()[business.ModulePrincipalID(declaringConsumerPrefix)])
		delete(narrowed.OperationAudiences, "documents")
		fixture.scope = &narrowed

		issued, err := confinedMint(t, inside(), nil)

		require.Nil(t, issued)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		require.Contains(t, err.Error(), `"documents" is not a module the consumer`)
	})
	t.Run("the consumer declares no consumption", func(t *testing.T) {
		fixture := confinedConsumerService(t)
		fixture.declare(nil)

		issued, err := confinedMint(t, inside(), nil)

		require.Nil(t, issued)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		require.Contains(t, err.Error(), "is not a module the consumer")
	})
	t.Run("the installation records no delivered authority", func(t *testing.T) {
		fixture := confinedConsumerService(t)
		fixture.state.PrincipalID = ""

		issued, err := confinedMint(t, inside(), nil)

		require.Nil(t, issued)
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
		require.Contains(t, err.Error(), "records no delivered authority")
	})
	t.Run("the installation was withdrawn", func(t *testing.T) {
		fixture := confinedConsumerService(t)
		fixture.state = nil

		issued, err := confinedMint(t, inside(), nil)

		require.Nil(t, issued)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		require.Contains(t, err.Error(), "no longer active")
	})
	t.Run("the live authority cannot be read", func(t *testing.T) {
		fixture := confinedConsumerService(t)
		fixture.liveErr = errors.New("connection refused")

		issued, err := confinedMint(t, inside(), nil)

		require.Nil(t, issued)
		require.Equal(t, codes.Unavailable, status.Code(err), "an unanswered question is not an allow and not a denial")
	})
}

// The consumer's authority is held from the decision through the mint: one use
// span, opened before the ceiling is read and released only once StartTask has
// returned. An uninstall or a narrowed installation therefore waits for the
// capability rather than landing between the decision and the signature.
func TestConfinedMintHoldsTheConsumersAuthorityThroughTheMint(t *testing.T) {
	fixture := confinedConsumerService(t)

	issued, err := confinedMint(t, scoped("documents", "evidence", "read"), nil)

	require.NoError(t, err)
	require.NotEmpty(t, issued.GetToken())
	require.Equal(t, 1, fixture.spans, "one span, shared by the ceiling read")
	require.True(t, fixture.readInSpan, "the consumer's live authority is read inside the span")
	require.True(t, fixture.ownerInSpan, "the span is still held after the ceiling decision, while the mint proceeds")
	require.False(t, fixture.open, "the span is released once the mint returns")
}
