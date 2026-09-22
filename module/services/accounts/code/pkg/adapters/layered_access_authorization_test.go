package adapters

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

const (
	layeredActorID = "019f6bf7-5b1c-730d-9687-fe6d4aff31ee"
	layeredOrgID   = "019f6bf7-5b4b-74e5-8c17-092259bb1663"
)

type layeredAuthzStore struct {
	business.Store
	role       gen.OrgRole
	listCalled bool

	// Captured by ListAccessibleScopes so a test can assert the subject the
	// handler resolved from the bearer rather than from the request.
	scopeOrg     string
	scopeSubject string
	scopeKind    gen.SubjectKind
}

func (f *layeredAuthzStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (f *layeredAuthzStore) GetPlatformRole(context.Context, string) (string, error) { return "", nil }

func (f *layeredAuthzStore) GetOrgMembership(_ context.Context, orgID, userID string) (*gen.OrgMembership, error) {
	if orgID == layeredOrgID && userID == layeredActorID {
		return &gen.OrgMembership{UserId: userID, Role: f.role}, nil
	}
	return nil, nil
}

func (f *layeredAuthzStore) ListShares(context.Context, string, string, string) ([]*gen.RecordShare, error) {
	f.listCalled = true
	return nil, nil
}

func (f *layeredAuthzStore) ListAccessibleScopes(_ context.Context, orgID, subjectID string, kind gen.SubjectKind, _, _, _ string, _ int) ([]*gen.AccessibleScope, error) {
	f.scopeOrg, f.scopeSubject, f.scopeKind = orgID, subjectID, kind
	return []*gen.AccessibleScope{{NodeId: "node-b", ScopePath: "sol.b", Kind: "collection"}}, nil
}

func installLayeredAuthzService(t *testing.T, store business.Store) {
	t.Helper()
	previous := service
	svc, err := business.NewService(store)
	require.NoError(t, err)
	service = svc
	t.Cleanup(func() { service = previous })
}

func layeredListSharesRequest() *gen.ListSharesRequest {
	return &gen.ListSharesRequest{OrgId: layeredOrgID, ResourceType: "doc", ResourceId: "doc-1"}
}

// A record's share list is its ACL. A plain org member must be denied before the
// store is ever consulted — otherwise any member could enumerate who a record
// they cannot see is shared with.
func TestListSharesRejectsNonAdminMember(t *testing.T) {
	store := &layeredAuthzStore{role: gen.OrgRole_ORG_ROLE_MEMBER}
	installLayeredAuthzService(t, store)

	_, err := (&PermServer{}).ListShares(
		stampVerifiedIdentity(context.Background(), layeredActorID, layeredOrgID, auth.Assurance{}),
		layeredListSharesRequest(),
	)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.False(t, store.listCalled, "authorization must fail before the store is read")
}

func TestListSharesAllowsOrgAdmin(t *testing.T) {
	store := &layeredAuthzStore{role: gen.OrgRole_ORG_ROLE_ADMIN}
	installLayeredAuthzService(t, store)

	_, err := (&PermServer{}).ListShares(
		stampVerifiedIdentity(context.Background(), layeredActorID, layeredOrgID, auth.Assurance{}),
		layeredListSharesRequest(),
	)
	require.NoError(t, err)
	require.True(t, store.listCalled)
}

// ListMyAccessibleScopes resolves the subject from the bearer's principal — the
// request carries no subject_id — so a member sees their own boundaries and can
// never probe another principal's.
func TestListMyAccessibleScopesUsesBearerSubject(t *testing.T) {
	store := &layeredAuthzStore{role: gen.OrgRole_ORG_ROLE_MEMBER}
	installLayeredAuthzService(t, store)

	resp, err := (&AccessibleScopeServer{}).ListMyAccessibleScopes(
		stampVerifiedIdentity(context.Background(), layeredActorID, layeredOrgID, auth.Assurance{}),
		&gen.ListMyAccessibleScopesRequest{OrgId: layeredOrgID, ResourceType: "doc", Action: "read"},
	)
	require.NoError(t, err)
	require.Equal(t, layeredActorID, store.scopeSubject, "subject must be the bearer's principal")
	require.Equal(t, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, store.scopeKind)
	require.Equal(t, layeredOrgID, store.scopeOrg)
	require.Len(t, resp.GetScopes(), 1)
	require.Equal(t, "sol.b", resp.GetScopes()[0].GetScopePath())
}

// A non-member is denied before the store is ever queried, so the RPC cannot be
// an oracle about an org the caller does not belong to.
func TestListMyAccessibleScopesRejectsNonMember(t *testing.T) {
	store := &layeredAuthzStore{role: gen.OrgRole_ORG_ROLE_MEMBER}
	installLayeredAuthzService(t, store)

	const otherOrg = "019f6bf7-9999-7aaa-8bbb-cccccccccccc"
	_, err := (&AccessibleScopeServer{}).ListMyAccessibleScopes(
		stampVerifiedIdentity(context.Background(), layeredActorID, otherOrg, auth.Assurance{}),
		&gen.ListMyAccessibleScopesRequest{OrgId: otherOrg, ResourceType: "doc", Action: "read"},
	)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Empty(t, store.scopeSubject, "authorization must fail before the store is read")
}
