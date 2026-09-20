package adapters

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

const (
	explainActorID   = "019f6bf7-5b1c-730d-9687-fe6d4aff31ea"
	explainOrgID     = "019f6bf7-5b4b-74e5-8c17-092259bb166a"
	explainMemberID  = "019f6bf7-5b4b-74e5-8c17-092259bb166b"
	explainOutsideID = "019f6bf7-5b4b-74e5-8c17-092259bb166c"
	explainAgentID   = "019f6bf7-5b4b-74e5-8c17-092259bb166d"
	explainTeamID    = "019f6bf7-5b4b-74e5-8c17-092259bb166e"
)

type explainStore struct {
	business.Store
	business.PrincipalStore

	actorRole gen.OrgRole
	members   map[string]bool
	principal *business.Principal
	teamOrgs  map[string]string

	// Captured by CheckPermission so a test can assert both that the decision
	// point was reached and what the handler asked it.
	checked        bool
	checkedSubject string
	checkedKind    gen.SubjectKind
	checkedOrg     string
	checkedScope   string
}

type explainScope struct {
	business.Scoped
	identity business.Identity
}

func (s *explainScope) Within(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *explainScope) Identity() business.Identity { return s.identity }

func (s *explainStore) As(identity business.Identity) business.Scoped {
	return &explainScope{identity: identity}
}

func (s *explainStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *explainStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *explainStore) GetPlatformRole(context.Context, string) (string, error) { return "", nil }

func (s *explainStore) GetOrgMembership(_ context.Context, orgID, userID string) (*gen.OrgMembership, error) {
	if orgID == explainOrgID && userID == explainActorID {
		return &gen.OrgMembership{UserId: userID, Role: s.actorRole}, nil
	}
	return nil, nil
}

func (s *explainStore) OrgMemberExists(_ context.Context, orgID, userID string) (bool, error) {
	return orgID == explainOrgID && s.members[userID], nil
}

func (s *explainStore) GetPrincipal(_ context.Context, id string) (*business.Principal, error) {
	if s.principal != nil && s.principal.ID == id {
		return s.principal, nil
	}
	return nil, business.NewStoreError(errors.New("principal not found"), business.ErrTypeNotFound)
}

func (s *explainStore) GetTeamOrgID(_ context.Context, teamID string) (string, error) {
	return s.teamOrgs[teamID], nil
}

func (s *explainStore) CheckPermission(
	_ context.Context,
	subjectID string,
	kind gen.SubjectKind,
	_, _, orgID, scope string,
) (bool, string, error) {
	s.checked = true
	s.checkedSubject, s.checkedKind, s.checkedOrg, s.checkedScope = subjectID, kind, orgID, scope
	return true, "granted via role: admin", nil
}

func installExplainService(t *testing.T, store *explainStore) *explainStore {
	t.Helper()
	previous := service
	svc, err := business.NewService(store)
	require.NoError(t, err)
	service = svc
	t.Cleanup(func() { service = previous })
	return store
}

func explainAdminStore() *explainStore {
	return &explainStore{
		actorRole: gen.OrgRole_ORG_ROLE_ADMIN,
		members:   map[string]bool{explainActorID: true, explainMemberID: true},
		teamOrgs:  map[string]string{explainTeamID: explainOrgID},
	}
}

func explainCtx() context.Context {
	return stampVerifiedIdentity(context.Background(), explainActorID, explainOrgID, auth.Assurance{})
}

func explainRequest(subjectID string, kind gen.SubjectKind) *gen.ExplainPermissionRequest {
	return &gen.ExplainPermissionRequest{
		OrgId:       explainOrgID,
		SubjectId:   subjectID,
		SubjectKind: kind,
		Resource:    "roles",
		Action:      "write",
	}
}

// The answer an administrator reads is the decision point's own, on the subject
// and organization the handler resolved — not a reading of role rows.
func TestExplainPermissionReturnsTheDecisionPointsAnswer(t *testing.T) {
	store := installExplainService(t, explainAdminStore())

	resp, err := (&PermServer{}).ExplainPermission(
		explainCtx(),
		explainRequest(explainMemberID, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL),
	)
	require.NoError(t, err)
	require.True(t, resp.GetAllowed())
	require.Equal(t, "granted via role: admin", resp.GetReason())
	require.Equal(t, explainMemberID, store.checkedSubject)
	require.Equal(t, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, store.checkedKind)
	require.Equal(t, explainOrgID, store.checkedOrg)
}

// A plain member cannot read decisions about other subjects, so the decision
// point is never reached on their behalf.
func TestExplainPermissionRejectsNonAdmin(t *testing.T) {
	store := explainAdminStore()
	store.actorRole = gen.OrgRole_ORG_ROLE_MEMBER
	installExplainService(t, store)

	_, err := (&PermServer{}).ExplainPermission(
		explainCtx(),
		explainRequest(explainMemberID, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL),
	)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.False(t, store.checked, "authorization must fail before the decision is read")
}

// A principal outside the organization is refused before the decision is read:
// a global (NULL-organization) assignment would otherwise answer for a subject
// in another tenant, which is the oracle this gate exists to close.
func TestExplainPermissionRejectsSubjectOutsideOrganization(t *testing.T) {
	store := installExplainService(t, explainAdminStore())

	_, err := (&PermServer{}).ExplainPermission(
		explainCtx(),
		explainRequest(explainOutsideID, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL),
	)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Equal(t, subjectOutsideOrg, status.Convert(err).Message())
	require.False(t, store.checked, "authorization must fail before the decision is read")
}

// An agent principal holds no organization_members row; its organization is on
// its own principal row, and an administrator may ask about it there.
func TestExplainPermissionAllowsAgentPrincipalOfTheOrganization(t *testing.T) {
	store := explainAdminStore()
	store.principal = &business.Principal{ID: explainAgentID, Kind: business.PrincipalKindAgent, OrgID: explainOrgID}
	installExplainService(t, store)

	_, err := (&PermServer{}).ExplainPermission(
		explainCtx(),
		explainRequest(explainAgentID, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL),
	)
	require.NoError(t, err)
	require.Equal(t, explainAgentID, store.checkedSubject)
}

func TestExplainPermissionRejectsAgentPrincipalOfAnotherOrganization(t *testing.T) {
	store := explainAdminStore()
	store.principal = &business.Principal{ID: explainAgentID, Kind: business.PrincipalKindAgent, OrgID: "019f6bf7-5b4b-74e5-8c17-092259bb1670"}
	installExplainService(t, store)

	_, err := (&PermServer{}).ExplainPermission(
		explainCtx(),
		explainRequest(explainAgentID, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL),
	)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.False(t, store.checked)
}

func TestExplainPermissionResolvesTeamSubjectThroughItsOrganization(t *testing.T) {
	store := installExplainService(t, explainAdminStore())

	_, err := (&PermServer{}).ExplainPermission(
		explainCtx(),
		explainRequest(explainTeamID, gen.SubjectKind_SUBJECT_KIND_TEAM),
	)
	require.NoError(t, err)
	require.Equal(t, gen.SubjectKind_SUBJECT_KIND_TEAM, store.checkedKind)
}

func TestExplainPermissionRejectsTeamOfAnotherOrganization(t *testing.T) {
	store := explainAdminStore()
	store.teamOrgs = map[string]string{explainTeamID: "019f6bf7-5b4b-74e5-8c17-092259bb1670"}
	installExplainService(t, store)

	_, err := (&PermServer{}).ExplainPermission(
		explainCtx(),
		explainRequest(explainTeamID, gen.SubjectKind_SUBJECT_KIND_TEAM),
	)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.False(t, store.checked)
}

// The scope travels to the decision point unchanged: an unscoped question and
// a scoped one are different questions, and the administrator asks both.
func TestExplainPermissionPassesScopeThrough(t *testing.T) {
	store := installExplainService(t, explainAdminStore())

	req := explainRequest(explainMemberID, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL)
	req.Scope = "project-7"
	_, err := (&PermServer{}).ExplainPermission(explainCtx(), req)
	require.NoError(t, err)
	require.Equal(t, "project-7", store.checkedScope)
}

// An unspecified subject kind never reaches the store: the enum's zero value
// names neither a principal nor a team.
func TestExplainPermissionRejectsUnspecifiedSubjectKind(t *testing.T) {
	store := installExplainService(t, explainAdminStore())

	_, err := (&PermServer{}).ExplainPermission(
		explainCtx(),
		explainRequest(explainMemberID, gen.SubjectKind_SUBJECT_KIND_UNSPECIFIED),
	)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.False(t, store.checked)
}
