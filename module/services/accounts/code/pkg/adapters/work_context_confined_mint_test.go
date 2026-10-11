package adapters

// A MINT MADE WITH A RESOURCE-CONFINED CREDENTIAL is held to the consumer that
// credential was delivered to (register finding SA-F-MCPAUD, round 3;
// holdConfinedMintToItsConsumer carries the rule).
//
// These run the WHOLE procedure, not the helper, because every refusal has to
// land before anything is signed and because the rule composes with the mint's
// other gates — the audience vocabulary, the boundary collision check, the
// viewer's own authority. A test of the helper alone could not tell a refusal
// from a refusal that arrives too late.
//
// The counterpart is the perimeter's: it is the gateway that resolves which
// installation a resource's consumer is installed as, and the gateway's own
// tests prove the two headers below are stamped from a verified credential and
// stripped from caller input.

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	accountsauth "accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

const (
	confinedInstallation = "019f6bf7-7777-7777-8777-777777777771"
	confinedConsumerID   = "019f6bf7-8888-7888-8888-888888888881"
	confinedResource     = "https://host.example.com/api/solutions/example/proxy/mcp"
)

// confinedConsumer is the installed consumer's ceiling: it may address the
// `documents` module and act on evidence, and nothing else.
func confinedConsumer() *business.InstallationAuthorityFacts {
	return &business.InstallationAuthorityFacts{
		OwnerPrincipalID: renewActorID,
		Actor: &business.Principal{
			ID:               confinedConsumerID,
			Kind:             "agent",
			AgentIdentifier:  "acme/example:1.0.0",
			AllowedAudiences: []string{"documents"},
			AllowedScopes:    []string{"evidence"},
		},
		OrganizationRevision: 12,
	}
}

// confinedCallerContext is the request the perimeter admitted: the viewer's
// identity, the resource their credential is confined to, and the installation
// that admitted it.
// confinedMintRequest is boundaryMintRequest with a Task named, which an
// ordinary (non-solution-scoped) mint requires: the host assigns a boundary only
// to a solution's own credential, and a viewer-owned Task names its own.
func confinedMintRequest(audience string) *gen.StartTaskWorkContextRequest {
	request := boundaryMintRequest(audience)
	request.TaskId = uuid.Must(uuid.NewV7()).String()
	return request
}

func confinedCallerContext(resource, installation string) context.Context {
	ctx := accountsauth.WithVerifiedResourceConfinement(boundaryCaller(), resource)
	return accountsauth.WithVerifiedInstallation(ctx, installation)
}

// withConfiguredOrigin pins the origin the resource indicator must name, the way
// a deployment pins it. Without it a confined mint refuses for a reason that has
// nothing to do with what these tests are about, which is itself asserted below.
func withConfiguredOrigin(t *testing.T) {
	t.Helper()
	previous, _ := accountsauth.ConfiguredPublicOrigin()
	require.NoError(t, accountsauth.SetConfiguredPublicOrigin("https://host.example.com"))
	t.Cleanup(func() { _ = accountsauth.SetConfiguredPublicOrigin(previous) })
}

func newConfinedMintServer(t *testing.T) *WorkContextAuthorityServer {
	t.Helper()
	server, authority := newSolutionBoundaryServer(t)
	authority.installationFcts = confinedConsumer()
	return server
}

// The consumer's own work: the audience its installation declares, a scope its
// agent is granted, in its own organization. Admitted.
func TestConfinedMintIsIssuedInsideItsConsumersCeiling(t *testing.T) {
	solutionBoundaryService(t)
	withConfiguredOrigin(t)
	server := newConfinedMintServer(t)

	issued, err := server.StartTask(
		confinedCallerContext(confinedResource, confinedInstallation),
		confinedMintRequest("documents"))

	require.NoError(t, err)
	require.NotEmpty(t, issued.GetToken())
}

// The three refusals, each on its own, with everything else held equal.
func TestConfinedMintIsRefusedOutsideItsConsumersCeiling(t *testing.T) {
	for name, probe := range map[string]struct {
		request      func() *gen.StartTaskWorkContextRequest
		ctx          func() context.Context
		expectedCode codes.Code
		expectedText string
	}{
		// 1. ANOTHER ORGANIZATION. The viewer may well be a member of it —
		// requireOrgMember would pass — but the installation the perimeter
		// checked is in the organization the credential names.
		"another organization": {
			request: func() *gen.StartTaskWorkContextRequest {
				req := confinedMintRequest("documents")
				req.OrgId = uuid.Must(uuid.NewV7()).String()
				return req
			},
			expectedCode: codes.PermissionDenied,
			expectedText: "that credential's own organization",
		},
		// 2. AN AUDIENCE THE CONSUMER DOES NOT DECLARE. `example` is in the
		// host's vocabulary — a declared module prefix — so the vocabulary gate
		// admits it and only the installation's ceiling refuses it.
		"an audience the consumer does not declare": {
			request:      func() *gen.StartTaskWorkContextRequest { return confinedMintRequest("example") },
			expectedCode: codes.PermissionDenied,
			expectedText: "allowed audiences",
		},
		// 3. A SCOPE OUTSIDE THE INSTALLATION'S GRANT.
		"a scope the installation does not grant": {
			request: func() *gen.StartTaskWorkContextRequest {
				req := confinedMintRequest("documents")
				req.AuthorityScopes = []*gen.WorkContextScope{{
					ResourceKind: "billing", Actions: []string{"write"},
				}}
				return req
			},
			expectedCode: codes.PermissionDenied,
			expectedText: "allowed scopes",
		},
		// A confinement the perimeter admitted without naming an installation.
		// Nothing produces it, and a mint is not issued on a ceiling nobody can
		// be held to.
		"no installation to be held to": {
			request: func() *gen.StartTaskWorkContextRequest { return confinedMintRequest("documents") },
			ctx: func() context.Context {
				return accountsauth.WithVerifiedResourceConfinement(boundaryCaller(), confinedResource)
			},
			expectedCode: codes.FailedPrecondition,
			expectedText: "no consented ceiling",
		},
		// A resource this host does not issue. The confinement is unaccountable,
		// so it refuses rather than passing through unread.
		"a resource this host does not issue": {
			request: func() *gen.StartTaskWorkContextRequest { return confinedMintRequest("documents") },
			ctx: func() context.Context {
				return confinedCallerContext("https://elsewhere.example/api/solutions/example/proxy/mcp", confinedInstallation)
			},
			expectedCode: codes.PermissionDenied,
			expectedText: "does not issue",
		},
	} {
		t.Run(name, func(t *testing.T) {
			solutionBoundaryService(t)
			withConfiguredOrigin(t)
			server := newConfinedMintServer(t)
			ctx := confinedCallerContext(confinedResource, confinedInstallation)
			if probe.ctx != nil {
				ctx = probe.ctx()
			}

			issued, err := server.StartTask(ctx, probe.request())

			require.Error(t, err, "nothing may be signed for %s", name)
			require.Nil(t, issued)
			require.Equal(t, probe.expectedCode, status.Code(err))
			require.Contains(t, err.Error(), probe.expectedText)
		})
	}
}

// THE CONTROLS. Without them every refusal above would also be produced by a
// mint that refused everything.
//
// An UNCONFINED request — every other request this service serves — reaches the
// viewer's own authority and is bounded by it alone: the same audience and the
// same scope the consumer's ceiling refuses above are issued here, so the
// refusals are about the confinement and not about the mint.
func TestAnUnconfinedMintIsNotHeldToAnyConsumer(t *testing.T) {
	solutionBoundaryService(t)
	withConfiguredOrigin(t)
	server := newConfinedMintServer(t)

	request := confinedMintRequest("example")
	request.AuthorityScopes = []*gen.WorkContextScope{{
		ResourceKind: "billing", Actions: []string{"write"},
	}}
	issued, err := server.StartTask(boundaryCaller(), request)

	require.NoError(t, err)
	require.NotEmpty(t, issued.GetToken())
}

// A deployment with no configured origin cannot say what a resource identifier
// IS, so it cannot account for one. It refuses the confined mint, and says which
// of the two refusals it is — an operator fixes a configuration, a client
// re-authorizes.
func TestConfinedMintRefusesWithNoConfiguredOrigin(t *testing.T) {
	solutionBoundaryService(t)
	previous, _ := accountsauth.ConfiguredPublicOrigin()
	require.NoError(t, accountsauth.SetConfiguredPublicOrigin(""))
	t.Cleanup(func() { _ = accountsauth.SetConfiguredPublicOrigin(previous) })
	server := newConfinedMintServer(t)

	issued, err := server.StartTask(
		confinedCallerContext(confinedResource, confinedInstallation),
		confinedMintRequest("documents"))

	require.Error(t, err)
	require.Nil(t, issued)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, err.Error(), "no configured public address")
}

// THE DELIVERY SHAPE, not a constructed context: the headers the gateway
// forwards, through the REAL Connect interceptor, into the REAL StartTask.
//
// Every test above installs the confinement with the context carriers directly,
// which proves the rule and not the route to it. This proves the route: that the
// perimeter's two headers survive the interceptor's strip, reach the mint as
// facts, and are refused when a caller supplies them without the gateway's own
// credential or supplies either one twice.
func forwardedByTheGateway(confined bool) http.Header {
	headers := http.Header{}
	headers.Set("X-Codefly-Gateway-Token", "test-gateway-token")
	headers.Set("X-User-Id", renewActorID)
	headers.Set("X-Org-Id", renewOrgID)
	headers.Set("X-Org-Role", "admin")
	headers.Set("X-Session-Id", renewSession)
	headers.Set("X-Credential-Kind", credentialKindSession)
	headers.Set("X-Scopes", "")
	if confined {
		headers.Set(resourceConfinementHeader, confinedResource)
		headers.Set(installationHeader, confinedInstallation)
	}
	return headers
}

func authorizeAsTheGateway(t *testing.T, headers http.Header) (context.Context, error) {
	t.Helper()
	previous := gatewayToken
	SetGatewayToken("test-gateway-token")
	t.Cleanup(func() { SetGatewayToken(previous) })
	// The procedure name is the policy lookup key, so it is the real one: an
	// unclassified procedure is refused before any header is read.
	return (&connectPolicyInterceptor{}).authorize(
		context.Background(), "/saas.accounts.v1.WorkContextService/StartTask", headers)
}

func TestConfinedMintThroughTheRealInterceptor(t *testing.T) {
	for name, probe := range map[string]struct {
		request func() *gen.StartTaskWorkContextRequest
		want    codes.Code
	}{
		"control: inside the ceiling": {
			request: func() *gen.StartTaskWorkContextRequest { return confinedMintRequest("documents") },
			want:    codes.OK,
		},
		"another organization": {
			request: func() *gen.StartTaskWorkContextRequest {
				request := confinedMintRequest("documents")
				request.OrgId = uuid.Must(uuid.NewV7()).String()
				return request
			},
			want: codes.PermissionDenied,
		},
		"an audience the consumer does not declare": {
			request: func() *gen.StartTaskWorkContextRequest { return confinedMintRequest("example") },
			want:    codes.PermissionDenied,
		},
	} {
		t.Run(name, func(t *testing.T) {
			solutionBoundaryService(t)
			withConfiguredOrigin(t)
			server := newConfinedMintServer(t)
			ctx, err := authorizeAsTheGateway(t, forwardedByTheGateway(true))
			require.NoError(t, err)

			issued, err := server.StartTask(ctx, probe.request())

			require.Equal(t, probe.want, status.Code(err), "err=%v", err)
			if probe.want == codes.OK {
				require.NotEmpty(t, issued.GetToken())
			}
		})
	}
}

// AN EMPTY CEILING IS NOTHING FOR A CONFINED MINT, where it is unrestricted
// everywhere else.
//
// This is the ordinary state, not an edge case: both fields are optional, the
// proto documents empty as unrestricted, and the install dialog leaves them
// empty. Read that way here, a credential delivered to one third-party client
// mints for any audience this host serves and then pairs at every module the
// organization installed — which is the escape the ceiling exists to close.
func TestConfinedMintUnderAnUndeclaredCeilingIsRefused(t *testing.T) {
	for name, strip := range map[string]func(*business.Principal){
		"no declared audiences": func(p *business.Principal) { p.AllowedAudiences = nil },
		"no declared scopes":    func(p *business.Principal) { p.AllowedScopes = nil },
	} {
		t.Run(name, func(t *testing.T) {
			solutionBoundaryService(t)
			withConfiguredOrigin(t)
			server, authority := newSolutionBoundaryServer(t)
			consumer := confinedConsumer()
			strip(consumer.Actor)
			authority.installationFcts = consumer
			ctx, err := authorizeAsTheGateway(t, forwardedByTheGateway(true))
			require.NoError(t, err)

			// `documents` is what this consumer's full ceiling DOES declare, so
			// the only thing that can refuse it here is the empty list itself.
			issued, err := server.StartTask(ctx, confinedMintRequest("documents"))

			require.Error(t, err, "an installation with %s put no bound on a credential delivered to a third party", name)
			require.Nil(t, issued)
			require.Equal(t, codes.FailedPrecondition, status.Code(err))
			require.Contains(t, err.Error(), "declares no allowed")
		})
	}
}

// THE CONTROL for the two above: the same empty ceiling puts no bound on an
// UNCONFINED mint, which is what the field means everywhere else and what an
// installation's own headless work relies on.
func TestAnUndeclaredCeilingStillLeavesAnUnconfinedMintUnbounded(t *testing.T) {
	solutionBoundaryService(t)
	withConfiguredOrigin(t)
	server, authority := newSolutionBoundaryServer(t)
	consumer := confinedConsumer()
	consumer.Actor.AllowedAudiences = nil
	consumer.Actor.AllowedScopes = nil
	authority.installationFcts = consumer

	issued, err := server.StartTask(boundaryCaller(), confinedMintRequest("example"))

	require.NoError(t, err)
	require.NotEmpty(t, issued.GetToken())
}

// The two ceiling headers are read ONLY behind the gateway's own credential, and
// only once each. Without the credential they are deleted — which also leaves the
// request unauthenticated, so there is nothing to mint for either.
func TestCeilingHeadersCannotBeForged(t *testing.T) {
	solutionBoundaryService(t)
	withConfiguredOrigin(t)

	t.Run("no gateway credential", func(t *testing.T) {
		headers := forwardedByTheGateway(true)
		headers.Del("X-Codefly-Gateway-Token")
		ctx, err := authorizeAsTheGateway(t, headers)
		if err == nil {
			_, err = newConfinedMintServer(t).StartTask(ctx, confinedMintRequest("documents"))
		}
		require.Error(t, err)
	})

	for name, header := range map[string]string{
		"a second installation": installationHeader,
		"a second resource":     resourceConfinementHeader,
	} {
		t.Run(name+" is refused", func(t *testing.T) {
			headers := forwardedByTheGateway(true)
			headers.Add(header, "a-value-the-perimeter-did-not-stamp")
			_, err := authorizeAsTheGateway(t, headers)
			require.Error(t, err, "a second %s would let the path that added it choose the ceiling", header)
		})
	}
}
