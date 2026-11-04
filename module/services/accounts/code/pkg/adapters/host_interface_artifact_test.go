package adapters

import (
	"context"
	"strings"
	"testing"

	artifactv1 "accounts/pkg/gen/host/artifact/v1"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	installationv1 "accounts/pkg/gen/host/installation/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func testHostArtifactPolicyIsolation(t *testing.T, ctx context.Context, conn grpc.ClientConnInterface, admin context.Context, module, credential string) {
	authority := authorityv1.NewAuthorityServiceClient(conn)
	parent := func(policy string) string {
		issued, err := authority.Issue(admin, &authorityv1.IssueRequest{
			As: &authorityv1.IssueRequest_Person{Person: &authorityv1.PersonSession{}}, Audience: module,
			Scopes: []*authorityv1.WorkScopeV1{wireScope(policy, "activate", "run")},
		})
		require.NoError(t, err)
		return issued.Token
	}
	policyA, policyB := module+".intent", module+".preview"
	tokenA, tokenB := parent(policyA), parent(policyB)
	current, err := installationv1.NewInstallationServiceClient(conn).Current(ctx, &installationv1.CurrentInstallationRequest{ParentToken: tokenA, ActorCredential: credential})
	require.NoError(t, err)
	identity := &artifactv1.Identity{Schema: "example.artifact/v1", Source: current.Namespace, Subject: []byte("policy-isolation"), ExpectedRevision: 1,
		Contracts: []*artifactv1.Contract{{Kind: "engine", Name: "example", Digest: "sha256:" + strings.Repeat("ab", 32)}}}
	request := func(policy, token string) *artifactv1.ArtifactRequest {
		return &artifactv1.ArtifactRequest{ParentToken: token, ActorCredential: credential, InstallationId: current.Id, Policy: policy, Identity: identity}
	}
	client := artifactv1.NewArtifactServiceClient(conn)
	a, err := client.Approve(ctx, request(policyA, tokenA))
	require.NoError(t, err)
	_, err = client.Authorize(ctx, request(policyB, tokenB))
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	revoke := func(policy, token, name string) error {
		_, err := client.Revoke(ctx, &artifactv1.RevokeRequest{ParentToken: token, ActorCredential: credential, InstallationId: current.Id, Policy: policy, ApprovalId: strings.TrimPrefix(name, "host/approved-artifacts/")})
		return err
	}
	require.Equal(t, codes.PermissionDenied, status.Code(revoke(policyB, tokenB, a.QualifiedName)))
	b, err := client.Approve(ctx, request(policyB, tokenB))
	require.NoError(t, err)
	require.NotEqual(t, a.QualifiedName, b.QualifiedName)
	same, err := client.Approve(ctx, request(policyA, tokenA))
	require.NoError(t, err)
	require.Equal(t, a.QualifiedName, same.QualifiedName)
	require.NoError(t, revoke(policyA, tokenA, a.QualifiedName))
	_, err = client.Authorize(ctx, request(policyB, tokenB))
	require.NoError(t, err)
	for _, call := range []func(context.Context, *artifactv1.ArtifactRequest, ...grpc.CallOption) (*artifactv1.Approval, error){client.Approve, client.Authorize} {
		empty := request(policyB, tokenB)
		empty.Identity = nil
		_, err = call(ctx, empty)
		require.Equal(t, codes.InvalidArgument, status.Code(err), "omitted identity must not select revoke semantics: %v", err)
	}
}
