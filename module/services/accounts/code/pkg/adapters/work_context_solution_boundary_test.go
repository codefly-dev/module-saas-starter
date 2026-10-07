package adapters

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"strings"
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
	"io/fs"
	"os"
	"path/filepath"
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
	// Binding ids, not UUIDs: the boundary is derived from the declared
	// presence binding, and a binding id may carry characters a UUID may not.
	boundarySeedA     = "binding-solution-a-0001"
	boundarySeedB     = "binding-solution-b-0002"
	boundaryPublisher = "solution:example"
	// A second organization, to show one tenant's boundary is not another's.
	boundaryOtherOrg = "019f6bf7-5b4b-74e5-8c17-092259bb1672"
)

// derivedBoundary is what the host seals for one solution in one organization.
// The test derives it the same way the mint does rather than hard-coding a
// digest, so the assertion is about the rule and not about today's output.
func derivedBoundary(t *testing.T, seed, orgID string) string {
	t.Helper()
	boundary, err := business.SolutionRuntimeBoundary(seed, orgID)
	require.NoError(t, err)
	return boundary
}

// solutionBoundaryAuthorityFake is the renewal authority plus the registry read
// the mint makes. It records every lookup so a test can prove which solution id
// reached the registry — the whole cross-solution property is that the id comes
// from the verified credential and from nowhere else.
type solutionBoundaryAuthorityFake struct {
	workContextAuthorityFake
	seeds          map[string]string
	errs           map[string]error
	publisher      string
	backendStopped bool
	seedsErr       error
	asked          []string
	seedsAsked     int
}

func (f *solutionBoundaryAuthorityFake) SolutionRuntimeBoundarySeed(
	_ context.Context, solutionID string,
) (business.SolutionBoundarySeed, error) {
	f.asked = append(f.asked, solutionID)
	if err, ok := f.errs[solutionID]; ok {
		return business.SolutionBoundarySeed{}, err
	}
	seed, ok := f.seeds[solutionID]
	if !ok {
		return business.SolutionBoundarySeed{}, business.ErrSolutionRegistrationNotFound
	}
	missing := ""
	if f.backendStopped {
		missing = "backend_revision and backend_upstream"
	}
	return business.SolutionBoundarySeed{
		BindingID:          seed,
		MissingBackendHalf: missing,
		Publisher:          f.publisher,
		BackendServing:     !f.backendStopped,
	}, nil
}

// SolutionRuntimeBoundarySeeds answers the collision check. It returns every
// seed, as the relation does — tombstones included — so a test can prove an
// ordinary mint cannot name one.
func (f *solutionBoundaryAuthorityFake) SolutionRuntimeBoundarySeeds(
	_ context.Context,
) ([]string, error) {
	f.seedsAsked++
	if f.seedsErr != nil {
		return nil, f.seedsErr
	}
	out := make([]string, 0, len(f.seeds))
	for _, seed := range f.seeds {
		out = append(out, seed)
	}
	return out, nil
}

func newSolutionBoundaryServer(t *testing.T) (*WorkContextAuthorityServer, *solutionBoundaryAuthorityFake) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	authority := &solutionBoundaryAuthorityFake{
		workContextAuthorityFake: workContextAuthorityFake{
			facts: &business.WorkContextAuthorityFacts{OrganizationRevision: 12},
		},
		seeds: map[string]string{
			boundarySolutionA: boundarySeedA,
			boundarySolutionB: boundarySeedB,
		},
		errs:      map[string]error{},
		publisher: boundaryPublisher,
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
	ctx, err := accountsauth.WithVerifiedSolution(boundaryCaller(), boundarySolutionA, boundaryPublisher)
	require.NoError(t, err)

	first, err := server.StartTask(ctx, boundaryMintRequest("runtime.tasks"))
	require.NoError(t, err)
	second, err := server.StartTask(ctx, boundaryMintRequest("runtime.tasks"))
	require.NoError(t, err)
	otherAudience, err := server.StartTask(ctx, boundaryMintRequest("documents"))
	require.NoError(t, err)

	boundary := derivedBoundary(t, boundarySeedA, renewOrgID)
	require.Equal(t, boundary, boundaryOf(t, server, "runtime.tasks", first))
	require.Equal(t, boundary, boundaryOf(t, server, "runtime.tasks", second),
		"a renewed context for the same solution must carry the same boundary")
	require.Equal(t, boundary, boundaryOf(t, server, "documents", otherAudience),
		"a mint for another audience is still the same solution's boundary")
	require.NotEqual(t, boundarySeedA, boundary,
		"the sealed boundary is derived from the seed, never the seed itself")
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
	ctx, err := accountsauth.WithVerifiedSolution(boundaryCaller(), boundarySolutionB, boundaryPublisher)
	require.NoError(t, err)

	// B asks with A's boundary in every field a caller controls on this RPC: the
	// Task it would have named, the Session it roots in, and the workspace and
	// project attribution. None of them reaches the boundary.
	request := boundaryMintRequest("runtime.tasks")
	workspace, project := derivedBoundary(t, boundarySeedA, renewOrgID), boundarySeedA
	request.WorkspaceId, request.ProjectId = &workspace, &project

	issued, err := server.StartTask(ctx, request)
	require.NoError(t, err)
	require.Equal(t, derivedBoundary(t, boundarySeedB, renewOrgID),
		boundaryOf(t, server, "runtime.tasks", issued))
	require.NotEqual(t, derivedBoundary(t, boundarySeedA, renewOrgID),
		boundaryOf(t, server, "runtime.tasks", issued))
	require.Equal(t, []string{boundarySolutionB}, authority.asked)

	// And naming A's boundary as the task_id is refused outright rather than
	// quietly replaced, so a solution reaching for another's gets an error and
	// not a capability it might mistake for one.
	request = boundaryMintRequest("runtime.tasks")
	request.TaskId = derivedBoundary(t, boundarySeedA, renewOrgID)
	_, err = server.StartTask(ctx, request)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// A caller-supplied task_id on a solution mint is refused.
func TestSolutionScopedMintRefusesACallerSuppliedTaskID(t *testing.T) {
	solutionBoundaryService(t)
	server, _ := newSolutionBoundaryServer(t)
	ctx, err := accountsauth.WithVerifiedSolution(boundaryCaller(), boundarySolutionA, boundaryPublisher)
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

	delete(authority.seeds, boundarySolutionA)
	ctx, err := accountsauth.WithVerifiedSolution(boundaryCaller(), boundarySolutionA, boundaryPublisher)
	require.NoError(t, err)
	_, err = server.StartTask(ctx, boundaryMintRequest("runtime.tasks"))
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "not registered")

	authority.errs[boundarySolutionB] = business.ErrSolutionRegistrationTombstoned
	ctx, err = accountsauth.WithVerifiedSolution(boundaryCaller(), boundarySolutionB, boundaryPublisher)
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

	t.Run("a_task_that_is_nobody_else_s_boundary_is_the_boundary", func(t *testing.T) {
		request := boundaryMintRequest("tool.test")
		request.TaskId = renewTaskID

		issued, err := server.StartTask(boundaryCaller(), request)
		require.NoError(t, err)
		require.Equal(t, renewTaskID, boundaryOf(t, server, "tool.test", issued))
		require.Empty(t, authority.asked,
			"an ordinary mint never resolves a solution's own registration")
	})

	t.Run("no_task_and_no_solution_is_refused", func(t *testing.T) {
		_, err := server.StartTask(boundaryCaller(), boundaryMintRequest("tool.test"))
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Contains(t, status.Convert(err).Message(), "task_id is required")
	})

	// A module mint takes no context at all — StartModuleTask and
	// StartModuleOperationTask are called with an authority struct, so there is
	// nothing for a verified solution to reach. What is worth pinning is that
	// each still draws its own fresh Task and never a solution's boundary.
	t.Run("module_mints_draw_their_own_fresh_task", func(t *testing.T) {
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
		require.NotEqual(t, derivedBoundary(t, boundarySeedA, renewOrgID), moduleFirst.GetTaskId())

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
		require.NotEqual(t, derivedBoundary(t, boundarySeedA, renewOrgID), operationFirst.GetTaskId())
		require.Empty(t, authority.asked)
	})
}

// NOTHING ON THE WIRE SETS A VERIFIED SOLUTION, and a request that claims one
// is REFUSED BY NAME.
//
// This test asserted the opposite until the cutover: that the solution identity
// is trusted from a trusted forwarder. That was correct while the gateway proved
// the claim from a solution's registration credential. Runtime
// self-registration is deleted, so the gateway stamps neither header and there
// is nothing left to prove it from — which turns "trusted from the gateway"
// into "trusted from whoever reached the gateway", i.e. any authenticated
// viewer naming any solution.
//
// Refused rather than ignored: a miswired runtime that silently received an
// ordinary capability would look like it worked, and would read nothing of the
// solution's runs. The message names the prerequisite instead.
func TestAClaimedSolutionIdentityIsRefusedByNameAndNeverTrusted(t *testing.T) {
	previousGateway := gatewayToken
	SetGatewayToken("test-gateway-token")
	t.Cleanup(func() { SetGatewayToken(previousGateway) })

	// Behind a VALID gateway token — the trusted path, which is exactly where
	// the old contract believed the claim.
	_, err := (&connectPolicyInterceptor{getMinter: nil}).authorize(
		context.Background(), "/saas.accounts.v1.WorkContextService/StartTask", http.Header{
			"X-Codefly-Gateway-Token":      []string{"test-gateway-token"},
			"X-Credential-Kind":            []string{credentialKindSession},
			"X-Scopes":                     []string{""},
			"X-User-Id":                    []string{renewActorID},
			"X-Org-Id":                     []string{renewOrgID},
			"X-Codefly-Solution-Id":        []string{boundarySolutionA},
			"X-Codefly-Solution-Publisher": []string{boundaryPublisher},
		})
	require.Error(t, err, "a claimed solution identity behind the gateway token must be refused")
	require.Contains(t, err.Error(), "solution attestation is not delivered on this host",
		"the refusal must name the missing attestation, not a generic forwarded-identity error")

	_, err = (&grpcPolicyAuthorizer{getMinter: nil, exposure: rpcExposureTenant}).authorize(
		metadata.NewIncomingContext(context.Background(), metadata.Pairs(
			"x-codefly-gateway-token", "test-gateway-token",
			"x-credential-kind", credentialKindSession,
			"x-scopes", "",
			"x-user-id", renewActorID,
			"x-org-id", renewOrgID,
			"x-codefly-solution-id", boundarySolutionA,
			"x-codefly-solution-publisher", boundaryPublisher,
		)), "/saas.accounts.v1.WorkContextService/StartTask")
	require.Error(t, err, "same claim over gRPC metadata")
	require.Contains(t, err.Error(), "solution attestation is not delivered on this host")

	// Either header ALONE is a claim. Refusing only the complete pair would
	// make the refusal depend on how completely a caller lied.
	for name, headers := range map[string]http.Header{
		"id alone":        {"X-Codefly-Solution-Id": []string{boundarySolutionA}},
		"publisher alone": {"X-Codefly-Solution-Publisher": []string{boundaryPublisher}},
	} {
		t.Run(name, func(t *testing.T) {
			headers.Set("X-Codefly-Gateway-Token", "test-gateway-token")
			headers.Set("X-Credential-Kind", credentialKindSession)
			headers.Set("X-Scopes", "")
			headers.Set("X-User-Id", renewActorID)
			_, err := (&connectPolicyInterceptor{getMinter: nil}).authorize(
				context.Background(), "/saas.accounts.v1.WorkContextService/StartTask", headers)
			require.Error(t, err)
			require.Contains(t, err.Error(), "solution attestation is not delivered on this host")
		})
	}

	// WITHOUT a gateway token the headers are deleted rather than refused: an
	// anonymous prober gets the ordinary answer for its credential, not a
	// signal that these headers mean something. Either way no solution is set.
	minter := func() accountsauth.JWTMinter {
		return &fixedAccessMinter{identity: &accountsauth.Identity{
			UserID:    uuid.MustParse(renewActorID),
			SessionID: uuid.Must(uuid.NewV7()),
		}}
	}
	forgedConnect, err := (&connectPolicyInterceptor{getMinter: minter}).authorize(
		context.Background(), "/saas.accounts.v1.WorkContextService/StartTask", http.Header{
			"Authorization":                []string{"Bearer any"},
			"X-Codefly-Solution-Id":        []string{boundarySolutionA},
			"X-Codefly-Solution-Publisher": []string{boundaryPublisher},
		})
	require.NoError(t, err)
	_, ok := accountsauth.VerifiedSolution(forgedConnect)
	require.False(t, ok, "a caller-injected solution must not select a boundary")

	forgedGRPC, err := (&grpcPolicyAuthorizer{getMinter: minter, exposure: rpcExposureTenant}).authorize(
		metadata.NewIncomingContext(context.Background(), metadata.Pairs(
			"authorization", "Bearer any",
			"x-codefly-solution-id", boundarySolutionA,
			"x-codefly-solution-publisher", boundaryPublisher,
		)), "/saas.accounts.v1.WorkContextService/StartTask")
	require.NoError(t, err)
	_, ok = accountsauth.VerifiedSolution(forgedGRPC)
	require.False(t, ok)
}

// The source-level half of the same promise: no non-test code may put a
// verified solution into a context. A refusal in the interceptors is only as
// good as the absence of another writer, and an interceptor is easy to add.
func TestNoProductionCodeSetsAVerifiedSolution(t *testing.T) {
	root := ".."
	var writers []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(body), "WithVerifiedSolution(") &&
			!strings.HasSuffix(path, "solution_identity.go") {
			writers = append(writers, path)
		}
		return nil
	}))
	require.Empty(t, writers,
		"WithVerifiedSolution is called outside its own declaration: a solution identity can only come from a delivered attestation, which does not exist on this host yet")
}

// The hole this closes (issue #1015, BLOCKER of the PR #1017 review). A
// boundary is not a secret a consumer keeps: the runtime's execution reads
// report the Work Context task a run was admitted under, so one read any caller
// is entitled to hands them a value that is now stable for the life of the
// registration. If an ORDINARY mint would sign it, that caller — holding only a
// viewer's bearer, which a solution's passthrough forwards to its backend —
// could reach another solution's runs for every tenant, with no rotation.
//
// So a caller-named task_id that is any registered solution's boundary is
// refused. The refusal does not name the solution: the caller learns only that
// the value is not theirs to name. The seed itself is refused too, in case it
// ever reaches a caller by another route.
func TestOrdinaryMintRefusesARegisteredSolutionsBoundary(t *testing.T) {
	solutionBoundaryService(t)
	server, authority := newSolutionBoundaryServer(t)

	// The BINDING ID behind a boundary is no longer in this set, and that is a
	// consequence of the re-key rather than a gap. A task_id is a UUID; a
	// binding id is not, and is refused on shape before this check is reached —
	// so asserting the boundary message here would assert the wrong refusal.
	// TestNamingABindingIDAsATaskIDIsRefused keeps that case covered on its own
	// terms, and the derived boundaries below are the values a caller could
	// actually guess at.
	for name, taskID := range map[string]string{
		"another solution's derived boundary": derivedBoundary(t, boundarySeedA, renewOrgID),
		"its own derived boundary":            derivedBoundary(t, boundarySeedB, renewOrgID),
	} {
		t.Run(name, func(t *testing.T) {
			request := boundaryMintRequest("runtime.tasks")
			request.TaskId = taskID

			_, err := server.StartTask(boundaryCaller(), request)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			require.Contains(t, status.Convert(err).Message(), "registered solution's runtime boundary")
			require.NotContains(t, status.Convert(err).Message(), boundarySolutionA)
			require.NotContains(t, status.Convert(err).Message(), boundarySolutionB)
		})
	}

	// The headless installation mint names its own Task the same way, so it is
	// held to the same refusal. It is checked before the installation is
	// resolved, so the refusal does not depend on a live installation.
	t.Run("the headless installation mint too", func(t *testing.T) {
		SetInternalToken("solution-boundary-test-token")
		t.Cleanup(func() { SetInternalToken("") })
		internal := metadata.NewIncomingContext(boundaryCaller(),
			metadata.Pairs("x-codefly-internal-token", "solution-boundary-test-token"))

		_, err := server.StartInstallationTask(internal, &gen.StartInstallationTaskRequest{
			OrgId:          renewOrgID,
			InstallationId: renewTaskID,
			TaskId:         derivedBoundary(t, boundarySeedA, renewOrgID),
			SessionId:      renewSession,
			Audience:       "runtime.tasks",
			AuthorityScopes: []*gen.WorkContextScope{{
				ResourceKind: "evidence",
				Actions:      []string{"read"},
			}},
		})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Contains(t, status.Convert(err).Message(), "registered solution's runtime boundary")
	})

	// A registry that cannot answer fails the mint CLOSED. Signing anyway would
	// be the whole hole: the capability cannot be shown not to be a solution's.
	t.Run("a registry that cannot answer refuses the mint", func(t *testing.T) {
		authority.seedsErr = errors.New("registry unavailable")
		t.Cleanup(func() { authority.seedsErr = nil })

		request := boundaryMintRequest("tool.test")
		request.TaskId = renewTaskID
		_, err := server.StartTask(boundaryCaller(), request)
		require.Equal(t, codes.Internal, status.Code(err))
	})
}

// One tenant's boundary is not another's. A run is filed under (tenant,
// boundary), so a single per-solution value would make every tenant of a
// solution share one — and an org admin who can read one execution would hold
// the boundary every other tenant's runs are filed under.
func TestSolutionBoundaryIsPerOrganization(t *testing.T) {
	solutionBoundaryService(t)
	server, _ := newSolutionBoundaryServer(t)
	ctx, err := accountsauth.WithVerifiedSolution(boundaryCaller(), boundarySolutionA, boundaryPublisher)
	require.NoError(t, err)

	here, err := server.StartTask(ctx, boundaryMintRequest("runtime.tasks"))
	require.NoError(t, err)
	require.Equal(t, derivedBoundary(t, boundarySeedA, renewOrgID),
		boundaryOf(t, server, "runtime.tasks", here))
	require.NotEqual(t, derivedBoundary(t, boundarySeedA, boundaryOtherOrg),
		boundaryOf(t, server, "runtime.tasks", here),
		"two organizations of one solution must not share a boundary")
}

// The credential binds a solution id to the publisher holding its secret; the
// registry binds the same id to the publisher that claimed it. A secret
// re-provisioned to a different publisher can write nothing — the registry
// refuses it — and must mint nothing either.
func TestSolutionScopedMintRefusesAForeignPublisher(t *testing.T) {
	solutionBoundaryService(t)
	server, _ := newSolutionBoundaryServer(t)
	ctx, err := accountsauth.WithVerifiedSolution(boundaryCaller(), boundarySolutionA, "solution:someone-else")
	require.NoError(t, err)

	_, err = server.StartTask(ctx, boundaryMintRequest("runtime.tasks"))
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "another publisher")
}

// The backend half is the half that mints. One whose lease lapsed is a
// deployment that stopped renewing, and the gateway has already stopped routing
// to it, so minting its boundary hands out authority for a solution nothing can
// reach.
func TestSolutionScopedMintRefusesANonServingBackend(t *testing.T) {
	solutionBoundaryService(t)
	server, authority := newSolutionBoundaryServer(t)
	authority.backendStopped = true
	ctx, err := accountsauth.WithVerifiedSolution(boundaryCaller(), boundarySolutionA, boundaryPublisher)
	require.NoError(t, err)

	_, err = server.StartTask(ctx, boundaryMintRequest("runtime.tasks"))
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "not serving")
}

// The collision check compares strings, so what spellings can reach it matters.
//
// uuid.Parse accepts a braced, unhyphenated or URN form of the same value, and
// any of those would walk past a string compare of the canonical one. They do
// not reach the handler: the schema's uuid rule on task_id admits only the
// canonical hyphenated form, so the single variance left is case — which the
// check folds. This pins both halves, because the guard is only safe while the
// schema keeps rejecting the rest: relax that rule and the refusal becomes
// bypassable with no test failing anywhere near it.
func TestRegisteredBoundaryRefusalCoversEverySpellingThatReachesIt(t *testing.T) {
	solutionBoundaryService(t)
	server, _ := newSolutionBoundaryServer(t)
	boundary := derivedBoundary(t, boundarySeedA, renewOrgID)

	t.Run("an upper-case spelling is still refused", func(t *testing.T) {
		request := boundaryMintRequest("runtime.tasks")
		request.TaskId = strings.ToUpper(boundary)

		_, err := server.StartTask(boundaryCaller(), request)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Contains(t, status.Convert(err).Message(), "registered solution's runtime boundary")
	})

	// Rejected by Validate before the handler decides anything, so the refusal
	// above is the only one that has to recognise a boundary.
	for name, spelling := range map[string]string{
		"braced":       "{" + boundary + "}",
		"unhyphenated": strings.ReplaceAll(boundary, "-", ""),
		"urn":          "urn:uuid:" + boundary,
	} {
		t.Run(name+" does not reach the handler", func(t *testing.T) {
			request := boundaryMintRequest("runtime.tasks")
			request.TaskId = spelling

			_, err := server.StartTask(boundaryCaller(), request)
			require.Error(t, err)
			require.NotContains(t, status.Convert(err).Message(), "registered solution's runtime boundary",
				"the schema must refuse this spelling, so the guard never sees it")
		})
	}
}

// TestNamingABindingIDAsATaskIDIsRefused keeps the case the boundary table
// above gave up when the seed stopped being a UUID.
//
// A declared presence binding id is not a task_id and never was; what changed
// is that it is now the boundary's INPUT, so a caller who learned one might try
// naming it. It is refused — and the assertion is only that it is refused and
// that the message names no solution, because the reason is a shape refusal
// rather than the boundary collision, and pinning it to the collision's wording
// would be asserting a path this value cannot reach.
func TestNamingABindingIDAsATaskIDIsRefused(t *testing.T) {
	solutionBoundaryService(t)
	server, _ := newSolutionBoundaryServer(t)

	request := boundaryMintRequest("runtime.tasks")
	request.TaskId = boundarySeedA

	_, err := server.StartTask(boundaryCaller(), request)
	require.Error(t, err)
	require.NotContains(t, status.Convert(err).Message(), boundarySolutionA)
	require.NotContains(t, status.Convert(err).Message(), boundarySolutionB)
}

// TestTheSealedClaimIsTheBindingIDsDerivationAndTheRefusalNamesTheMissingHalf
// holds the two halves of the serving ruling, on the FAKE-backed path so it
// runs in CI rather than skipping on a missing DSN — which is how the mint's
// total break reached a pushed head in the first place.
//
// Half one: the boundary a mint seals is the derivation of the declared
// presence BINDING ID, not of a per-registration random. Asserted by value
// against business.SolutionRuntimeBoundary rather than by re-deriving inside
// the test, so a change to the derivation moves both sides and this still
// compares the sealed claim to the binding id's answer.
//
// Half two: a declared row with no backend half refuses "not serving" AND
// names the columns it read. A refusal that only says "not serving" sends an
// operator to look for a lease that no longer exists.
func TestTheSealedClaimIsTheBindingIDsDerivationAndTheRefusalNamesTheMissingHalf(t *testing.T) {
	solutionBoundaryService(t)
	server, authority := newSolutionBoundaryServer(t)

	ctx, err := accountsauth.WithVerifiedSolution(boundaryCaller(), boundarySolutionA, boundaryPublisher)
	require.NoError(t, err)

	issued, err := server.StartTask(ctx, boundaryMintRequest("runtime.tasks"))
	require.NoError(t, err)
	want, err := business.SolutionRuntimeBoundary(boundarySeedA, renewOrgID)
	require.NoError(t, err)
	// Read off the SIGNED capability, not the response field: a consumer
	// verifies the first.
	require.Equal(t, want, boundaryOf(t, server, "runtime.tasks", issued),
		"the sealed claim must be the BINDING ID's derivation")
	require.NotEqual(t, boundarySeedA, issued.GetTaskId(),
		"the binding id itself is the input, never the sealed value")

	// The backend half stops being delivered: no lease lapses, the declaration
	// simply no longer carries it.
	authority.backendStopped = true
	_, err = server.StartTask(ctx, boundaryMintRequest("runtime.tasks"))
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	message := status.Convert(err).Message()
	require.Contains(t, message, "not serving")
	require.Contains(t, message, "backend_revision")
	require.Contains(t, message, "backend_upstream")
	require.NotContains(t, message, "lease", "a lapsed lease is not why a declaration is absent")
}
