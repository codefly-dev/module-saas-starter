package adapters

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// TestInstallAccessRequestsAreBounded holds InstallReviewedSolution's access
// fields to their declared bounds: at most 64 roles each way, a role named by
// 1 to 128 characters, a team named by its id, no role ticked twice.
func TestInstallAccessRequestsAreBounded(t *testing.T) {
	request := func(edit func(*gen.InstallReviewedSolutionRequest)) *gen.InstallReviewedSolutionRequest {
		req := &gen.InstallReviewedSolutionRequest{OrgId: fixtureOrganization, Review: &gen.ReviewedSolutionInstall{},
			RoleTeams: map[string]string{"viewer": fixtureOrganization}, InstallerRoles: []string{"installer", "viewer"}}
		edit(req)
		return req
	}
	many := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("role-%d", i)
		}
		return out
	}
	require.NoError(t, Validate(request(func(*gen.InstallReviewedSolutionRequest) {})))
	require.NoError(t, Validate(request(func(r *gen.InstallReviewedSolutionRequest) { r.InstallerRoles = many(64) })))
	for name, edit := range map[string]func(*gen.InstallReviewedSolutionRequest){
		"65 installer roles":       func(r *gen.InstallReviewedSolutionRequest) { r.InstallerRoles = many(65) },
		"a role ticked twice":      func(r *gen.InstallReviewedSolutionRequest) { r.InstallerRoles = []string{"viewer", "viewer"} },
		"an empty installer role":  func(r *gen.InstallReviewedSolutionRequest) { r.InstallerRoles = []string{""} },
		"a 129-character role":     func(r *gen.InstallReviewedSolutionRequest) { r.InstallerRoles = []string{strings.Repeat("r", 129)} },
		"a team that is not an id": func(r *gen.InstallReviewedSolutionRequest) { r.RoleTeams = map[string]string{"viewer": "Readers"} },
		"a team for an empty role": func(r *gen.InstallReviewedSolutionRequest) { r.RoleTeams = map[string]string{"": fixtureOrganization} },
		"65 teams":                 func(r *gen.InstallReviewedSolutionRequest) { r.RoleTeams = teamsFor(many(65)) },
		"a team for a 129-char role": func(r *gen.InstallReviewedSolutionRequest) {
			r.RoleTeams = teamsFor([]string{strings.Repeat("r", 129)})
		},
	} {
		err := Validate(request(edit))
		require.Equal(t, codes.InvalidArgument, status.Code(err), "%s is refused", name)
	}
}

func teamsFor(roles []string) map[string]string {
	out := map[string]string{}
	for _, role := range roles {
		out[role] = fixtureOrganization
	}
	return out
}
