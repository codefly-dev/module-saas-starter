//go:build pure

package business

import (
	"context"
	"errors"
	"strings"
	"testing"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const tenancyTestOrg = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
const tenancyTestSubject = "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff"

type canonicalTenancyStore struct {
	Store
	PrincipalStore
	t             *testing.T
	member        bool
	principalKind string
	calls         []string
}

func (s *canonicalTenancyStore) pair(org, subject string) {
	s.t.Helper()
	require.Equal(s.t, tenancyTestOrg, org)
	require.Equal(s.t, tenancyTestSubject, subject)
}
func (s *canonicalTenancyStore) WithOrgTx(ctx context.Context, org string, fn func(context.Context) error) error {
	require.Equal(s.t, tenancyTestOrg, org)
	s.calls = append(s.calls, "transaction")
	return fn(ctx)
}
func (s *canonicalTenancyStore) LockOrgMembership(_ context.Context, org, subject string) error {
	s.pair(org, subject)
	s.calls = append(s.calls, "lock")
	return nil
}
func (s *canonicalTenancyStore) OrgMemberExists(_ context.Context, org, subject string) (bool, error) {
	s.pair(org, subject)
	s.calls = append(s.calls, "member")
	return s.member, nil
}
func (s *canonicalTenancyStore) GetPrincipal(_ context.Context, subject string) (*Principal, error) {
	require.Equal(s.t, tenancyTestSubject, subject)
	s.calls = append(s.calls, "principal")
	return &Principal{ID: subject, OrgID: tenancyTestOrg, Kind: s.principalKind}, nil
}
func (s *canonicalTenancyStore) GetTeamOrgID(_ context.Context, subject string) (string, error) {
	require.Equal(s.t, tenancyTestSubject, subject)
	s.calls = append(s.calls, "team")
	return tenancyTestOrg, nil
}
func (s *canonicalTenancyStore) AssignRole(_ context.Context, assignment *gen.RoleAssignment) error {
	s.pair(assignment.OrgId, assignment.SubjectId)
	s.calls = append(s.calls, "write")
	return nil
}
func (s *canonicalTenancyStore) ShareRecord(_ context.Context, share *gen.RecordShare) error {
	s.pair(share.OrgId, share.SubjectId)
	s.calls = append(s.calls, "write")
	return nil
}

func TestSubjectTenancyCanonicalizesBeforeEveryReadAndWrite(t *testing.T) {
	for spelling, spell := range map[string]func(string) string{
		"uppercase": strings.ToUpper,
		"compact":   func(s string) string { return strings.ReplaceAll(s, "-", "") },
		"braced":    func(s string) string { return "{" + s + "}" },
		"urn":       func(s string) string { return "urn:uuid:" + s },
	} {
		for _, subject := range []string{"member", "agent", "service", "team"} {
			for _, write := range []string{"AssignRole", "ShareRecord"} {
				t.Run(spelling+"/"+subject+"/"+write, func(t *testing.T) {
					store := &canonicalTenancyStore{t: t, member: subject == "member", principalKind: subject}
					service := &Service{store: store}
					kind := gen.SubjectKind_SUBJECT_KIND_PRINCIPAL
					want := []string{"transaction", "lock", "member"}
					if subject == "team" {
						kind = gen.SubjectKind_SUBJECT_KIND_TEAM
						want = []string{"transaction", "team"}
					}
					if subject == "agent" || subject == "service" {
						want = append(want, "principal")
					}
					org, id := spell(tenancyTestOrg), spell(tenancyTestSubject)
					if write == "AssignRole" {
						req := &gen.AssignRoleRequest{OrgId: org, SubjectId: id, SubjectKind: kind}
						_, err := service.AssignRole(context.Background(), req)
						require.NoError(t, err)
						require.Equal(t, org, req.OrgId, "do not mutate the caller's request")
						require.Equal(t, id, req.SubjectId)
					} else {
						req := &gen.ShareRecordRequest{OrgId: org, SubjectId: id, SubjectKind: kind}
						_, err := service.ShareRecord(context.Background(), tenancyTestSubject, req)
						require.NoError(t, err)
						require.Equal(t, org, req.OrgId)
						require.Equal(t, id, req.SubjectId)
					}
					require.Equal(t, append(want, "write"), store.calls)
				})
			}
		}
	}
}

func TestSubjectTenancyRefusesMalformedIDsBeforeStoreAccess(t *testing.T) {
	service := &Service{} // Any store access would panic.
	for _, invalid := range []string{"", "not-a-uuid"} {
		for _, pair := range [][2]string{{tenancyTestOrg, invalid}, {invalid, tenancyTestSubject}} {
			org, subject := pair[0], pair[1]
			for _, kind := range []gen.SubjectKind{gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, gen.SubjectKind_SUBJECT_KIND_TEAM} {
				if org != "" { // Empty org retains the separately authorized platform path.
					_, err := service.AssignRole(context.Background(), &gen.AssignRoleRequest{OrgId: org, SubjectId: subject, SubjectKind: kind})
					require.Equal(t, codes.InvalidArgument, status.Code(err))
				}
				_, err := service.ShareRecord(context.Background(), tenancyTestSubject, &gen.ShareRecordRequest{OrgId: org, SubjectId: subject, SubjectKind: kind})
				require.Equal(t, codes.InvalidArgument, status.Code(err))
			}
			require.Equal(t, codes.InvalidArgument, status.Code(service.RemoveOrgMember(context.Background(), tenancyTestSubject, &gen.RemoveOrgMemberRequest{OrgId: org, UserId: subject})))
			require.Equal(t, codes.InvalidArgument, status.Code(service.LeaveOrganization(context.Background(), subject, &gen.LeaveOrganizationRequest{OrgId: org})))
			require.Equal(t, codes.InvalidArgument, status.Code(service.DeleteOrganization(context.Background(), subject, &gen.DeleteOrganizationRequest{OrgId: org})))
		}
	}
}

type bulkRemovalOrderStore struct {
	canonicalTenancyStore
	removalErr error
}

func (s *bulkRemovalOrderStore) As(Identity) Scoped { return &principalLookupScope{} }
func (s *bulkRemovalOrderStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (s *bulkRemovalOrderStore) LockOrgAdministration(_ context.Context, org string) error {
	require.Equal(s.t, tenancyTestOrg, org)
	return nil
}
func (s *bulkRemovalOrderStore) GetOrganization(_ context.Context, org string) (*gen.Organization, error) {
	return &gen.Organization{Id: org, Slug: "example"}, nil
}
func (s *bulkRemovalOrderStore) ListOrgMembers(context.Context, string) ([]*gen.OrgMembership, error) {
	return []*gen.OrgMembership{{OrgId: tenancyTestOrg, UserId: tenancyTestSubject}}, nil
}
func (s *bulkRemovalOrderStore) RemoveAllOrgMembers(_ context.Context, org string) (int64, error) {
	require.Equal(s.t, tenancyTestOrg, org)
	return 0, s.removalErr
}
func (s *bulkRemovalOrderStore) RevokeOrgAPIKeys(context.Context, string) (int64, error) {
	return 0, errors.New("revocation reached before membership locks; can deadlock an assignment")
}

func TestDeleteOrganizationAcquiresMembershipLocksBeforeRevocations(t *testing.T) {
	lockErr := errors.New("membership lock unavailable")
	store := &bulkRemovalOrderStore{canonicalTenancyStore: canonicalTenancyStore{t: t, principalKind: PrincipalKindHuman}, removalErr: lockErr}
	service := &Service{store: store}
	service.SetPolicyLog(newFakePolicyLog(), newFakePolicyLogStore())
	err := service.DeleteOrganization(context.Background(), strings.ToUpper(tenancyTestSubject), &gen.DeleteOrganizationRequest{OrgId: strings.ToUpper(tenancyTestOrg), ConfirmSlug: "example"})
	require.ErrorIs(t, err, lockErr, "bulk membership locks must precede every revocation's trigger locks")
}
