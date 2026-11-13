package adapters

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	"accounts/pkg/datasource/connector/connectortest"
	"accounts/pkg/datasource/operations"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// grantedOnlyTo answers the scope-grant query for one subject alone, and
// records who was asked.
type grantedOnlyTo struct {
	*sourceWireStore
	subject string
	kind    gen.SubjectKind
	asked   []string
}

func (s *grantedOnlyTo) CanReadScopeNode(_ context.Context, _, subject string, kind gen.SubjectKind, _, _, _ string) (bool, error) {
	s.asked = append(s.asked, kind.String()+":"+subject)
	return subject == s.subject && kind == s.kind, nil
}

// Every read of and call on a source is decided by whoever answers for the
// session (business.SourceHolderFor): the person in their own session, and the
// team a cast acts as, alone, in a cast. The person's grant is never the
// cast's, and a cast is never the organization administrator.
func TestASourceOperationIsDecidedByWhoeverAnswersForTheSession(t *testing.T) {
	const org, person, team = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "44444444-4444-4444-8444-444444444444"
	declaration, err := operations.Admit(connectortest.OperationDeclaration())
	require.NoError(t, err)
	ctx := stampVerifiedIdentity(t.Context(), person, org, auth.Assurance{})
	self, cast := business.SourceHolderFor(person, ""), business.SourceHolderFor(person, team)
	require.False(t, self.Cast())
	require.True(t, cast.Cast())

	decisions := map[string]func(*business.Service, business.SourceBoundaryHolder) error{
		"list operations": func(svc *business.Service, who business.SourceBoundaryHolder) error {
			_, err := svc.ListSourceOperations(ctx, who, person, org, "source")
			return err
		},
		"invoke": func(svc *business.Service, who business.SourceBoundaryHolder) error {
			_, _, err := svc.SourceOperationAuthority(ctx, who, person, org, "source", declaration.Name, "effect", "", false)
			return err
		},
		"look up an invoke": func(svc *business.Service, who business.SourceBoundaryHolder) error {
			_, _, err := svc.SourceOperationAuthority(ctx, who, person, org, "source", declaration.Name, "effect", "", true)
			return err
		},
		"look up a receipt prune": func(svc *business.Service, who business.SourceBoundaryHolder) error {
			return svc.SourceReceiptRetentionAuthority(ctx, who, person, org, "source", "effect", true)
		},
	}
	for name, decide := range decisions {
		t.Run(name, func(t *testing.T) {
			for _, c := range []struct {
				name      string
				granted   business.SourceBoundaryHolder
				holder    business.SourceBoundaryHolder
				refusedAs string
			}{
				{"the person, in their own session", self, self, ""},
				{"a cast whose person holds the grant and whose team does not", self, cast, "team " + team + ", which this session acts as, does not hold datasource:"},
				{"a cast whose team holds the grant", cast, cast, ""},
				{"the person, where only a team they cast onto holds it", cast, self, "source permission required"},
			} {
				t.Run(c.name, func(t *testing.T) {
					store := &grantedOnlyTo{sourceWireStore: &sourceWireStore{declaration: declaration,
						source: &business.DatasourceSource{ID: "source", OrgID: org, Provider: business.DatasourceProviderAPI, API: &business.APIDatasourceConfig{}, BoundaryNodeID: "node", PersonalOwnerUserID: person}},
						subject: c.granted.SubjectID, kind: c.granted.SubjectKind}
					svc, err := business.NewService(store)
					require.NoError(t, err)
					err = decide(svc, c.holder)
					require.Equal(t, []string{c.holder.SubjectKind.String() + ":" + c.holder.SubjectID}, store.asked, "only the holder's grants are read")
					if c.refusedAs != "" {
						require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
						require.Contains(t, status.Convert(err).Message(), c.refusedAs)
						return
					}
					require.NotEqual(t, codes.PermissionDenied, status.Code(err), "%v", err)
				})
			}
		})
	}

	t.Run("a cast calls no source but its person's own, whatever its team holds", func(t *testing.T) {
		governed := &business.SourceGovernance{InstallationID: "installation", Kind: "kind", Action: "write"}
		for _, c := range []struct {
			owner      string
			governance *business.SourceGovernance
		}{{"", nil}, {"55555555-5555-4555-8555-555555555555", nil}, {person, governed}} {
			owner := c.owner
			store := &grantedOnlyTo{sourceWireStore: &sourceWireStore{declaration: declaration,
				source: &business.DatasourceSource{ID: "source", OrgID: org, Provider: business.DatasourceProviderAPI, API: &business.APIDatasourceConfig{}, BoundaryNodeID: "node", PersonalOwnerUserID: owner, Governance: c.governance}},
				subject: cast.SubjectID, kind: cast.SubjectKind}
			svc, err := business.NewService(store)
			require.NoError(t, err)
			_, _, err = svc.SourceOperationAuthority(ctx, cast, person, org, "source", declaration.Name, "effect", "", false)
			require.Equal(t, codes.PermissionDenied, status.Code(err), "owner %q: %v", owner, err)
		}
	})

	t.Run("a cast is never the organization administrator for a receipt prune", func(t *testing.T) {
		for _, c := range []struct {
			holder  business.SourceBoundaryHolder
			refused bool
		}{{self, false}, {cast, true}} {
			store := &grantedOnlyTo{sourceWireStore: &sourceWireStore{role: gen.OrgRole_ORG_ROLE_ADMIN,
				source: &business.DatasourceSource{ID: "source", OrgID: org, Provider: business.DatasourceProviderAPI, API: &business.APIDatasourceConfig{}, BoundaryNodeID: "node"}},
				subject: c.holder.SubjectID, kind: c.holder.SubjectKind}
			svc, err := business.NewService(store)
			require.NoError(t, err)
			err = svc.SourceReceiptRetentionAuthority(ctx, c.holder, person, org, "source", "effect", true)
			if c.refused {
				require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
				require.Contains(t, status.Convert(err).Message(), "source owner or organization administrator required")
				continue
			}
			require.NoError(t, err, "an administrator in their own session maintains an organization's source")
		}
	})
}
