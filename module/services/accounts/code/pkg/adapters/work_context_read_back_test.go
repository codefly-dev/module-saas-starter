package adapters

import (
	"context"
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
	"google.golang.org/protobuf/types/known/timestamppb"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// The host's read-backs answer questions about a viewer's capability; they do
// not spend it. The documented consumer flow is: verify the capability, consume
// it through ConsumeSingleUse, then ask the host what it may read — record by
// record, page by page. Every one of those questions must still be answered for
// a SINGLE_USE capability, and none of them may consume it.

// singleUseViewer is a single-use capability readOwner holds for audience,
// reading kind — limited to ids when any are named.
func singleUseViewer(t *testing.T, audience, kind string, ids ...string) (string, *basev0.WorkContextV1) {
	t.Helper()
	return mintTestCapability(t, workContextSingleton, testCapability{
		Audience: audience, Tenant: readOrg, Owner: readOwner,
		Task: "019f6bf7-1111-7111-8111-111111111111", Session: "019f6bf7-2222-7222-8222-222222222222",
		Replay: workcontext.ReplaySingleUse,
		Scopes: []*basev0.WorkScopeV1{{ResourceKind: kind, Actions: []string{"read"}, ResourceIds: ids}},
	})
}

// consumeAsTheConsumer is ConsumeSingleUse as a consumer calls it after
// verifying: the token's own nonce, its tenant and expiry.
func consumeAsTheConsumer(t *testing.T, signed *basev0.WorkContextV1) error {
	t.Helper()
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-codefly-internal-token", "source-read-test-perimeter"))
	_, err := workContextSingleton.ConsumeSingleUse(ctx, &gen.ConsumeSingleUseWorkContextRequest{
		OrgId: signed.GetTenantId(), ContextId: signed.GetNonce(),
		ExpiresAt: timestamppb.New(time.Unix(signed.GetExpiresAtUnix(), 0)),
	})
	return err
}

func recordAccessService(t *testing.T) {
	t.Helper()
	svc, err := business.NewService(&exactRecordStore{})
	require.NoError(t, err)
	svc.SetModuleAuthorityReads(currentModuleAuthority{service: svc}, nil)
	svc.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{business.ModulePrincipalID("rows"): {Prefix: "rows", Resources: []string{"rows"}}})
	service = svc
	declareTestAudiences(svc)
}

func TestR1093_AConsumedSingleUseViewerCapabilityCanStillBeAskedAbout(t *testing.T) {
	_, _, client, _ := sourceReadFixture(t)
	recordAccessService(t)
	token, signed := singleUseViewer(t, "rows", "rows", "record-a")

	require.NoError(t, consumeAsTheConsumer(t, signed), "the consumer verifies and consumes its capability first")

	out, err := client.CheckWorkContextRecordAccess(context.Background(), exactRequest(token, "record-a"))
	require.NoError(t, err, "and can still ask the host what it may read with it")
	require.True(t, out.Msg.Allowed)
}

func TestR1093_TwoQuestionsAboutOneSingleUseCapability(t *testing.T) {
	_, _, client, _ := sourceReadFixture(t)
	recordAccessService(t)
	token, signed := singleUseViewer(t, "rows", "rows", "record-a")

	for range 2 {
		out, err := client.CheckWorkContextRecordAccess(context.Background(), exactRequest(token, "record-a"))
		require.NoError(t, err, "a question asks; it does not spend the capability")
		require.True(t, out.Msg.Allowed)
	}
	require.NoError(t, consumeAsTheConsumer(t, signed), "the questions left it for its consumer")
	err := consumeAsTheConsumer(t, signed)
	require.Equal(t, codes.AlreadyExists, status.Code(err), "which still consumes it exactly once")
}

// A paginated listing is drained with one viewer capability, as the source-read
// SDK says: every page is a read-back of the same single-use capability.
func TestR1093_EveryPageOfASourceListingReadsTheSameSingleUseCapability(t *testing.T) {
	_, _, client, _ := sourceReadFixture(t)
	token, signed := singleUseViewer(t, "documents", "documents")
	require.NoError(t, consumeAsTheConsumer(t, signed))

	first, err := client.ListReadableSourceCollections(context.Background(), sourceReadRequest(token))
	require.NoError(t, err)
	require.NotEmpty(t, first.Msg.NextPageToken, "the fixture has more than one page")
	next := sourceReadRequest(token)
	next.Msg.PageToken = first.Msg.NextPageToken
	_, err = client.ListReadableSourceCollections(context.Background(), next)
	require.NoError(t, err, "page two reads back the same capability")
}

// Reading back is still a full-strength check: a capability whose authority has
// moved is refused, consumed or not.
func TestReadBackStillRefusesAStaleCapability(t *testing.T) {
	_, facts, client, _ := sourceReadFixture(t)
	token, _ := singleUseViewer(t, "documents", "documents")
	facts.facts.OrganizationRevision++

	_, err := client.ListReadableSourceCollections(context.Background(), sourceReadRequest(token))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "%v", err)
}

// A module asking about a person's parent (here, the installation it was
// admitted under) reads the parent back; asking twice spends nothing.
func TestR1093_AModuleAsksAboutASingleUseParentMoreThanOnce(t *testing.T) {
	_, _, client, _ := sourceReadFixture(t)
	id, target := uuid.NewString(), uuid.NewString()
	store := &currentInstallationStore{
		artifactTransportStore: artifactTransportStore{member: true},
		installation:           &gen.Installation{Id: id, OrgId: readOrg, TargetId: target, Status: gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE},
		target:                 &business.SolutionTarget{ID: target, BindingID: "binding-acme-example-0001", SolutionID: "acme/example"},
	}
	var err error
	service, err = business.NewService(store)
	require.NoError(t, err)
	service.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{business.ModulePrincipalID("example"): {Prefix: "example", Tenant: readOrg}})
	installCurrentInstallationCaller(t, service, readOrg)
	parent, _ := singleUseViewer(t, "example", "definitions")

	for range 2 {
		r := connect.NewRequest(&gen.ModuleCurrentInstallationRequest{InstallationId: id, ParentWorkContextToken: parent})
		for k, v := range readExchangeRequest(t, parent).Header() {
			r.Header()[k] = append([]string{}, v...)
		}
		out, err := client.GetCurrentInstallation(context.Background(), r)
		require.NoError(t, err, "a question about the parent does not spend it")
		require.Equal(t, id, out.Msg.InstallationId)
	}
}
