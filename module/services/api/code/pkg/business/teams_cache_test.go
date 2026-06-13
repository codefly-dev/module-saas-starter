package business_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"api/pkg/business"
	"api/pkg/gen"
	"api/pkg/infra"
)

// TestService_AddTeamMember_UsesCachedOrgID — pins the
// requireTeamAdmin → AddTeamMember dedup. Without the cache, the
// flow is:
//
//   1. requireTeamAdmin: WithBypass to resolve team→org   (#bypass+=1)
//   2. requireTeamAdmin: WithOrgTx to ListTeamMembers
//   3. Service.AddTeamMember: WithBypass again (resolveTeamOrg)  (#bypass+=1)
//   4. Service.AddTeamMember: WithOrgTx to do the insert
//
// With the cache stamped via WithCachedTeamOrgID, step 3 is elided —
// resolveTeamOrg sees the ctx-cached orgID and skips the bypass call.
//
// The test invokes AddTeamMember once with the cache pre-stamped,
// once without, and asserts the WithBypass counter delta differs.
//
// This is integration-y: real DB + real Service. Slow-ish (~1s).
func TestService_AddTeamMember_UsesCachedOrgID(t *testing.T) {
	clearData(t)
	ctx := testCtx

	owner, orgID := mustUserAndOrg(t, ctx,
		"alice-cache@rls-test.com", "alice-cache-rls", "Acme Cache A")
	other, _ := mustUserAndOrg(t, ctx,
		"bob-cache@rls-test.com", "bob-cache-rls", "Acme Cache B")

	team, err := testService.CreateTeam(ctx, owner, &gen.CreateTeamRequest{
		OrgId: orgID, Name: "engineering",
	})
	require.NoError(t, err)

	// Helper to count "tenant_tx.go" invocations across teams.go
	// — we sum every counter whose key references teams.go's
	// resolveTeamOrg line. Coarse but enough to detect the +1 vs +0
	// behavior we care about.
	countTeamsBypasses := func() int64 {
		var n int64
		for site, v := range infra.BypassCounters() {
			if containsTeamsGo(site) {
				n += v
			}
		}
		return n
	}

	// First call: NO cache. Service.resolveTeamOrg should fire
	// WithBypass once (one bypass for THIS team_id resolve).
	before := countTeamsBypasses()
	require.NoError(t, testService.AddTeamMember(ctx, owner, &gen.AddTeamMemberRequest{
		TeamId: team.Team.Id,
		UserId: other,
		Role:   gen.TeamRole_TEAM_ROLE_MEMBER,
	}))
	withoutCacheDelta := countTeamsBypasses() - before

	// Remove the member so the second call has work to do (and
	// won't fail on duplicate-key — schema does ON CONFLICT but
	// keep the test deterministic).
	require.NoError(t, testService.RemoveTeamMember(ctx, owner, &gen.RemoveTeamMemberRequest{
		TeamId: team.Team.Id, UserId: other,
	}))

	// Second call: with cached orgID. resolveTeamOrg should
	// short-circuit; no new WithBypass from teams.go.
	before2 := countTeamsBypasses()
	cachedCtx := business.WithCachedTeamOrgID(ctx, team.Team.Id, orgID)
	require.NoError(t, testService.AddTeamMember(cachedCtx, owner, &gen.AddTeamMemberRequest{
		TeamId: team.Team.Id,
		UserId: other,
		Role:   gen.TeamRole_TEAM_ROLE_MEMBER,
	}))
	withCacheDelta := countTeamsBypasses() - before2

	// The cached path should fire STRICTLY FEWER bypasses than the
	// uncached path — not zero (the test setup may invoke other
	// bypasses transitively), but at least one less from the
	// resolveTeamOrg path.
	require.Greater(t, withoutCacheDelta, withCacheDelta,
		"cached AddTeamMember should fire fewer WithBypass calls than uncached "+
			"(uncached delta=%d, cached delta=%d)", withoutCacheDelta, withCacheDelta)
}

// containsTeamsGo — naive substring check. The recordBypass site
// key looks like "module/services/api/code/pkg/business/teams.go:N";
// resolveTeamOrg lives in teams.go.
func containsTeamsGo(site string) bool {
	const needle = "business/teams.go"
	for i := 0; i+len(needle) <= len(site); i++ {
		if site[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
