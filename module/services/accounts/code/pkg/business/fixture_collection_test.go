package business_test

import (
	"accounts/fixtures"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFixtureCollectionGrants(t *testing.T) {
	clearData(t)
	fixturePath := filepath.Join(t.TempDir(), "collection-demo.yaml")
	t.Setenv("DEV_FIXTURE_PATH", fixturePath)
	require.NoError(t, os.WriteFile(fixturePath, []byte(`users:
  - email: owner@example.com
    name: Jane Doe
    provider: email
    provider_id: collection-owner
  - email: reader@example.com
    name: Example Reader
    provider: email
    provider_id: collection-reader
  - email: member@example.com
    name: Example Member
    provider: email
    provider_id: collection-member
organizations:
  - id: 00000000-0000-7000-8000-00000000c645
    name: ExampleCorp
    owner: owner@example.com
    members:
      - email: reader@example.com
        role: member
      - email: member@example.com
        role: member
roles:
  - org: ExampleCorp
    name: Collection reader
    permissions:
      - resource: documents
        action: read
collections:
  - org: ExampleCorp
    label: Wiki
    role: Collection reader
    readers:
      - reader@example.com
`), 0600))
	const orgID = "00000000-0000-7000-8000-00000000c645"
	for range 2 {
		require.NoError(t, fixtures.Seed(testCtx, testService, "collection-demo"))
	}
	collections, err := testService.ListCollectionAccess(testCtx, &gen.ListCollectionAccessRequest{OrgId: orgID})
	require.NoError(t, err)
	require.Len(t, collections.Collections, 1)
	collection := collections.Collections[0]
	require.Len(t, collection.ReadGrants, 1)
	require.Equal(t, "owner@example.com", collection.ReadGrants[0].ActorLabel)
	require.Equal(t, "reader@example.com", collection.ReadGrants[0].SubjectLabel)
	readerID := seededUUIDFor(t, testCtx, "collection-reader")
	ownerID := seededUUIDFor(t, testCtx, "collection-owner")
	for _, identity := range []string{"collection-owner", "collection-member", "collection-reader"} {
		scopes, err := testService.ListAccessibleScopes(testCtx, &gen.ListAccessibleScopesRequest{OrgId: orgID, SubjectId: seededUUIDFor(t, testCtx, identity), SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, ResourceType: "documents", Action: "read"})
		require.NoError(t, err)
		if identity == "collection-reader" {
			require.Len(t, scopes.Scopes, 1)
		} else {
			require.Empty(t, scopes.Scopes)
		}
	}
	grant := collection.ReadGrants[0].Grant
	require.NoError(t, testService.RevokeScope(testCtx, ownerID, &gen.RevokeScopeRequest{OrgId: orgID, SubjectId: readerID, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, ScopePath: grant.ScopePath, RoleId: grant.RoleId}))
	scopes, err := testService.ListAccessibleScopes(testCtx, &gen.ListAccessibleScopesRequest{OrgId: orgID, SubjectId: readerID, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, ResourceType: "documents", Action: "read"})
	require.NoError(t, err)
	require.Empty(t, scopes.Scopes)
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		for _, eventType := range []string{string(business.EventScopeGranted), string(business.EventScopeRevoked)} {
			events, _, _, err := testStore.QueryAuditLog(ctx, business.AuditQuery{OrgID: orgID, EventType: eventType, PageSize: 100})
			require.NoError(t, err)
			require.NotEmpty(t, events)
			require.Equal(t, ownerID, events[0].ActorID)
		}
		return nil
	}))
}
