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
