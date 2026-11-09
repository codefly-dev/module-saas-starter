package business

import (
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/decide"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/stretchr/testify/require"
)

// An update's review carries what each role reached as the consent approved
// it, so the host can name what the update asks beyond it; a fresh install,
// an installation not active, and a consent recorded before reach was kept
// carry none, and the roles held together are no role's.
func TestAnUpdateReviewCarriesTheApprovedReachOfEachRole(t *testing.T) {
	approved := map[string][]domain.ReachRow{
		"viewer": {
			{Path: []string{"example"}, Module: "example", Via: decide.ReachRole, Confinement: decide.ConfinedToInstallation,
				Scopes: []*basev0.WorkScopeV1{{ResourceKind: "example.page", Actions: []string{"read"}}}, Operations: []string{"/example.v1.Pages/Get"}},
			{Path: []string{"example", "assistant", "documents"}, Module: "documents", Via: decide.ReachBinding,
				Scopes: []*basev0.WorkScopeV1{{ResourceKind: "documents.document", Actions: []string{"read"}}}},
		},
		decide.JointRoles: {{Path: []string{"example"}, Module: "example", Via: decide.ReachRole}},
	}
	text, digest, err := decide.EncodeReach(approved)
	require.NoError(t, err)
	inst := domain.Installation{ID: "inst-1", Status: domain.Active, Consent: domain.ConsentReceipt{OperationID: "op", Reach: text, ReachDigest: digest}}

	roles, err := approvedRoles(inst)
	require.NoError(t, err)
	require.Len(t, roles, 1, "the roles held together are no role's")
	require.Equal(t, "viewer", roles[0].GetName())
	require.Len(t, roles[0].GetReach(), 2)
	through := roles[0].GetReach()[1]
	require.Equal(t, []string{"example", "assistant", "documents"}, through.GetPath())
	require.Equal(t, "documents.document", through.GetScopes()[0].GetResourceKind())
	require.Equal(t, []string{"read"}, through.GetScopes()[0].GetActions())

	for name, other := range map[string]domain.Installation{
		"a fresh install":            {},
		"an installation not active": {ID: "inst-1", Status: domain.Pending, Consent: inst.Consent},
		"a consent before reach":     {ID: "inst-1", Status: domain.Active, Consent: domain.ConsentReceipt{OperationID: "op"}},
	} {
		roles, err := approvedRoles(other)
		require.NoError(t, err, name)
		require.Empty(t, roles, name)
	}
}
