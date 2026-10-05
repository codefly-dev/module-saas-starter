package adapters

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"testing"

	accountsauth "accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/sdk-go/workcontext"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// The host assigns a solution's runtime boundary (issue #1015).
//
// A runtime task is reachable only under the boundary of the Work Context that
// admitted it — its task_id. Accounts used to sign whatever task_id the minting
// caller sent, and a solution's passthrough asks for a fresh one per minted
// context, so a run a page admitted stopped being visible the moment that
// context was replaced. A stable per-solution boundary fixes that, and is safe
// only because the host assigns it: a boundary a solution could name would let
// one solution mint for another's and read, answer and recover its runs.
//
// These tests pin the four outcomes the issue names, against the real signer.

const (
	boundarySolutionA = "example-solution-a"
	boundarySolutionB = "example-solution-b"
	boundaryValueA    = "019f6c01-aaaa-7aaa-8aaa-aaaaaaaaaa01"
	boundaryValueB    = "019f6c01-bbbb-7bbb-8bbb-bbbbbbbbbb02"
)

// solutionBoundaryAuthorityFake is the renewal authority plus the registry read
// the mint makes. It records every lookup so a test can prove which solution id
// reached the registry — the whole cross-solution property is that the id comes
// from the verified credential and from nowhere else.
type solutionBoundaryAuthorityFake struct {
	workContextAuthorityFake
	boundaries map[string]string
	errs       map[string]error
	asked      []string
}

func (f *solutionBoundaryAuthorityFake) SolutionRuntimeBoundary(
	_ context.Context, solutionID string,
) (string, error) {
	f.asked = append(f.asked, solutionID)
	if err, ok := f.errs[solutionID]; ok {
		return "", err
	}
	boundary, ok := f.boundaries[solutionID]
	if !ok {
		return "", business.ErrSolutionRegistrationNotFound
	}
	return boundary, nil
}

func newSolutionBoundaryServer(t *testing.T) (*WorkContextAuthorityServer, *solutionBoundaryAuthorityFake) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	authority := &solutionBoundaryAuthorityFake{
		workContextAuthorityFake: workContextAuthorityFake{
			facts: &business.WorkContextAuthorityFacts{OrganizationRevision: 12},
		},
		boundaries: map[string]string{
			boundarySolutionA: boundaryValueA,
			boundarySolutionB: boundaryValueB,
		},
		errs: map[string]error{},
	}
	server := &WorkContextAuthorityServer{}
	server.Configure(WorkContextAuthorityConfiguration{
		Issuer:     "accounts.test",
		KeyID:      "accounts-test-key",
		PrivateKey: privateKey,
		Authority:  authority,
	})
	require.NoError(t, server.configureErr)
	return server, authority
}

// solutionBoundaryService installs the membership store requireOrgMember reads.
func solutionBoundaryService(t *testing.T) {
	t.Helper()
	previous := service
	svc, err := business.NewService(renewMembershipStore{})
	require.NoError(t, err)
	service = svc
	t.Cleanup(func() { service = previous })
}

// boundaryCaller is the viewer behind the passthrough: the person whose bearer
// the solution forwarded, owner and actor of the Task.
func boundaryCaller() context.Context {
	return stampVerifiedIdentity(
		context.Background(), renewActorID, renewOrgID, accountsauth.Assurance{},
	)
}

func boundaryMintRequest(audience string) *gen.StartTaskWorkContextRequest {
	return &gen.StartTaskWorkContextRequest{
		OrgId:     renewOrgID,
		SessionId: renewSession,
		Audience:  audience,
		AuthorityScopes: []*gen.WorkContextScope{{
			ResourceKind: "evidence",
			Actions:      []string{"read"},
		}},
	}
}

// boundaryOf reads the sealed task_id back out of the signed capability, so the
// assertion is about what a consumer will verify rather than about a field the
// handler happened to copy.
func boundaryOf(
	t *testing.T, server *WorkContextAuthorityServer, audience string, issued *gen.IssuedWorkContext,
) string {
	t.Helper()
	token, err := workcontext.ParseWorkContextToken(issued.GetToken())
	require.NoError(t, err)
	verified, err := server.verifier.Verify(token, workcontext.WorkContextExpectations{
		Issuer:   "accounts.test",
		Audience: audience,
	})
	require.NoError(t, err)
	// The response field and the signed claim must agree: a consumer reads the
	// second, so an assertion about the first alone would prove nothing.
	require.Equal(t, verified.GetTaskId(), issued.GetTaskId())
	return verified.GetTaskId()
}

// A renewed context — and a context minted for another audience or scope set,
// which is the same thing from the runtime's point of view — carries the same
// boundary. That is the whole point: the run stays reachable after the one
// context that admitted it is gone.
func TestSolutionScopedMintCarriesTheSameBoundaryOnEveryMint(t *testing.T) {
	solutionBoundaryService(t)
	server, authority := newSolutionBoundaryServer(t)
	ctx, err := accountsauth.WithVerifiedSolution(boundaryCaller(), boundarySolutionA)
	require.NoError(t, err)

	first, err := server.StartTask(ctx, boundaryMintRequest("runtime.tasks"))
	require.NoError(t, err)
	second, err := server.StartTask(ctx, boundaryMintRequest("runtime.tasks"))
	require.NoError(t, err)
	otherAudience, err := server.StartTask(ctx, boundaryMintRequest("documents"))
	require.NoError(t, err)

	require.Equal(t, boundaryValueA, boundaryOf(t, server, "runtime.tasks", first))
	require.Equal(t, boundaryValueA, boundaryOf(t, server, "runtime.tasks", second),
		"a renewed context for the same solution must carry the same boundary")
	require.Equal(t, boundaryValueA, boundaryOf(t, server, "documents", otherAudience),
		"a mint for another audience is still the same solution's boundary")
	require.Equal(t,
		[]string{boundarySolutionA, boundarySolutionA, boundarySolutionA},
		authority.asked,
		"the boundary is read from the registry under the verified solution id, every mint")
}

// Solution B cannot obtain A's boundary. There is no request field that names a
// solution, so the only lever a caller has is the credential — and the id it
// proves is the id the registry is asked about.
func TestSolutionScopedMintCannotReachAnotherSolutionsBoundary(t *testing.T) {
	solutionBoundaryService(t)
	server, authority := newSolutionBoundaryServer(t)
	ctx, err := accountsauth.WithVerifiedSolution(boundaryCaller(), boundarySolutionB)
	require.NoError(t, err)

	// B asks with A's boundary in every field a caller controls on this RPC: the
	// Task it would have named, the Session it roots in, and the workspace and
	// project attribution. None of them reaches the boundary.
	request := boundaryMintRequest("runtime.tasks")
	workspace, project := boundaryValueA, boundaryValueA
	request.WorkspaceId, request.ProjectId = &workspace, &project

	issued, err := server.StartTask(ctx, request)
	require.NoError(t, err)
	require.Equal(t, boundaryValueB, boundaryOf(t, server, "runtime.tasks", issued))
	require.NotEqual(t, boundaryValueA, boundaryOf(t, server, "runtime.tasks", issued))
	require.Equal(t, []string{boundarySolutionB}, authority.asked)

	// And naming A's boundary as the task_id is refused outright rather than
	// quietly replaced, so a solution reaching for another's gets an error and
	// not a capability it might mistake for one.
	request = boundaryMintRequest("runtime.tasks")
	request.TaskId = boundaryValueA
	_, err = server.StartTask(ctx, request)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// A caller-supplied task_id on a solution mint is refused.
func TestSolutionScopedMintRefusesACallerSuppliedTaskID(t *testing.T) {
	solutionBoundaryService(t)
	server, _ := newSolutionBoundaryServer(t)
	ctx, err := accountsauth.WithVerifiedSolution(boundaryCaller(), boundarySolutionA)
	require.NoError(t, err)

	request := boundaryMintRequest("runtime.tasks")
	request.TaskId = renewTaskID

	_, err = server.StartTask(ctx, request)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "may not name a task_id")
}

// A solution whose registration is gone — never made, or deregistered — cannot
// mint under a boundary its runs are filed against. The two refusals stay
// apart: one sends an operator to the deployment, the other to the removal.
func TestSolutionScopedMintRefusesAnUnregisteredOrRemovedSolution(t *testing.T) {
	solutionBoundaryService(t)
	server, authority := newSolutionBoundaryServer(t)

	delete(authority.boundaries, boundarySolutionA)
	ctx, err := accountsauth.WithVerifiedSolution(boundaryCaller(), boundarySolutionA)
	require.NoError(t, err)
	_, err = server.StartTask(ctx, boundaryMintRequest("runtime.tasks"))
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "not registered")

	authority.errs[boundarySolutionB] = business.ErrSolutionRegistrationTombstoned
	ctx, err = accountsauth.WithVerifiedSolution(boundaryCaller(), boundarySolutionB)
	require.NoError(t, err)
	_, err = server.StartTask(ctx, boundaryMintRequest("runtime.tasks"))
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "tombstoned")
}

// Every mint that is not solution-scoped is exactly what it was: the host's own
// pages, a composed module, a delegated agent. The caller names its own Task
// and the handler signs that one; naming none is refused, which is what the
// schema used to refuse before the field became conditional.
func TestOrdinaryMintsAreUnchangedByTheBoundary(t *testing.T) {
	solutionBoundaryService(t)
	server, authority := newSolutionBoundaryServer(t)

	t.Run("a_named_task_is_the_boundary", func(t *testing.T) {
		request := boundaryMintRequest("tool.test")
		request.TaskId = renewTaskID

		issued, err := server.StartTask(boundaryCaller(), request)
		require.NoError(t, err)
		require.Equal(t, renewTaskID, boundaryOf(t, server, "tool.test", issued))
		require.Empty(t, authority.asked, "an ordinary mint never reads the registry")
	})

	t.Run("no_task_and_no_solution_is_refused", func(t *testing.T) {
		_, err := server.StartTask(boundaryCaller(), boundaryMintRequest("tool.test"))
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Contains(t, status.Convert(err).Message(), "task_id is required")
	})

	// A module mint draws nothing from the request and nothing from the
	// registry, so a verified solution in the context must not reach it: each of
	// these keeps generating its own fresh Task, as it always has.
	t.Run("module_mints_ignore_a_verified_solution", func(t *testing.T) {
		_, moduleFirst, err := server.StartModuleTask(business.ModuleWorkContextAuthority{
			Tenant:      renewOrgID,
			PrincipalID: renewOwnerID,
		})
		require.NoError(t, err)
		_, moduleSecond, err := server.StartModuleTask(business.ModuleWorkContextAuthority{
			Tenant:      renewOrgID,
			PrincipalID: renewOwnerID,
		})
		require.NoError(t, err)
		require.NotEqual(t, moduleFirst.GetTaskId(), moduleSecond.GetTaskId())
		require.NotEqual(t, boundaryValueA, moduleFirst.GetTaskId())

		operation := business.ModuleOperationContextAuthority{
			ModuleWorkContextAuthority: business.ModuleWorkContextAuthority{
				Tenant:      renewOrgID,
				PrincipalID: renewOwnerID,
			},
			Audience: "runtime.operations",
			Revision: 12,
			Scopes: []business.ModuleOperationScope{{
				ResourceKind: "evidence",
				Actions:      []string{"read"},
			}},
		}
		_, operationFirst, err := server.StartModuleOperationTask(operation)
		require.NoError(t, err)
		_, operationSecond, err := server.StartModuleOperationTask(operation)
		require.NoError(t, err)
		require.NotEqual(t, operationFirst.GetTaskId(), operationSecond.GetTaskId())
		require.NotEqual(t, boundaryValueA, operationFirst.GetTaskId())
		require.Empty(t, authority.asked)
	})
}

// The solution identity reaches a handler only from a trusted forwarder, on
// both transports. Without that, an authenticated viewer could name any
// solution and mint under its boundary — the cross-boundary access the host
// assigns the boundary to prevent.
func TestForwardedSolutionIdentityIsOnlyTrustedBehindTheGateway(t *testing.T) {
	previousGateway := gatewayToken
	SetGatewayToken("test-gateway-token")
	t.Cleanup(func() { SetGatewayToken(previousGateway) })

	trustedConnect, err := (&connectPolicyInterceptor{getMinter: nil}).authorize(
		context.Background(), "/saas.accounts.v1.WorkContextService/StartTask", http.Header{
			"X-Codefly-Gateway-Token": []string{"test-gateway-token"},
			"X-Credential-Kind":       []string{credentialKindSession},
			"X-Scopes":                []string{""},
			"X-User-Id":               []string{renewActorID},
			"X-Org-Id":                []string{renewOrgID},
			"X-Codefly-Solution-Id":   []string{boundarySolutionA},
		})
	require.NoError(t, err)

	trustedGRPC, err := (&grpcPolicyAuthorizer{getMinter: nil, exposure: rpcExposureTenant}).authorize(
		metadata.NewIncomingContext(context.Background(), metadata.Pairs(
			"x-codefly-gateway-token", "test-gateway-token",
			"x-credential-kind", credentialKindSession,
			"x-scopes", "",
			"x-user-id", renewActorID,
			"x-org-id", renewOrgID,
			"x-codefly-solution-id", boundarySolutionA,
		)), "/saas.accounts.v1.WorkContextService/StartTask")
	require.NoError(t, err)

	for name, ctx := range map[string]context.Context{
		"gateway to Connect": trustedConnect,
		"gateway to gRPC":    trustedGRPC,
	} {
		solution, ok := accountsauth.VerifiedSolution(ctx)
		require.True(t, ok, name)
		require.Equal(t, boundarySolutionA, solution, name)
	}

	minter := func() accountsauth.JWTMinter {
		return &fixedAccessMinter{identity: &accountsauth.Identity{
			UserID:    uuid.MustParse(renewActorID),
			SessionID: uuid.Must(uuid.NewV7()),
		}}
	}
	forgedConnect, err := (&connectPolicyInterceptor{getMinter: minter}).authorize(
		context.Background(), "/saas.accounts.v1.WorkContextService/StartTask", http.Header{
			"Authorization":         []string{"Bearer any"},
			"X-Codefly-Solution-Id": []string{boundarySolutionA},
		})
	require.NoError(t, err)
	_, ok := accountsauth.VerifiedSolution(forgedConnect)
	require.False(t, ok, "a caller-injected solution must not select a boundary")

	forgedGRPC, err := (&grpcPolicyAuthorizer{getMinter: minter, exposure: rpcExposureTenant}).authorize(
		metadata.NewIncomingContext(context.Background(), metadata.Pairs(
			"authorization", "Bearer any",
			"x-codefly-solution-id", boundarySolutionA,
		)), "/saas.accounts.v1.WorkContextService/StartTask")
	require.NoError(t, err)
	_, ok = accountsauth.VerifiedSolution(forgedGRPC)
	require.False(t, ok)

	// A trusted forwarder's value that is not a catalog identity is refused
	// rather than dropped: minting with it would seal a boundary nobody owns.
	_, err = (&connectPolicyInterceptor{getMinter: nil}).authorize(
		context.Background(), "/saas.accounts.v1.WorkContextService/StartTask", http.Header{
			"X-Codefly-Gateway-Token": []string{"test-gateway-token"},
			"X-Credential-Kind":       []string{credentialKindSession},
			"X-Scopes":                []string{""},
			"X-User-Id":               []string{renewActorID},
			"X-Codefly-Solution-Id":   []string{"Not A Solution"},
		})
	require.Error(t, err)
}
