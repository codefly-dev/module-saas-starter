package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

// reachingBinding is an applied record under alias, holding principal and
// asking to redeem bindings.
func reachingBinding(t *testing.T, alias, principal string, bindings ...solutionhost.AuthorityBinding) *accountsv1.SolutionHostBindingState {
	t.Helper()
	b := moduleManifestBinding(t, endpointOf("documents", "rest", "rest"))
	m, ok := appliedManifest(b)
	require.True(t, ok)
	m.Alias, m.Binding = alias, "acme.test."+alias
	m.Authority = &solutionhost.Authority{Principal: principal, Bindings: bindings}
	body, err := solutionhost.Marshal(m)
	require.NoError(t, err)
	b.BindingId = m.Binding
	b.Applied.SolutionId, b.Applied.Generation.Document = alias, string(body)
	b.Registration.SolutionId, b.Registration.Declared.BindingId = alias, m.Binding
	return b
}

// A record's reach is the modules it invokes on a person's behalf, named by
// their aliases: never its own principal, never a headless-only binding, and
// never an audience no applied record answers to.
func TestARecordReachesTheModulesItDeclaresItInvokes(t *testing.T) {
	invoke := map[string][]string{"invoke": {"record:read"}}
	page := reachingBinding(t, "people", "people-runtime",
		solutionhost.AuthorityBinding{ID: "rows", Audience: "rows-runtime", Operations: invoke},
		solutionhost.AuthorityBinding{ID: "rows-again", Audience: "rows-runtime", Operations: invoke},
		solutionhost.AuthorityBinding{ID: "self", Audience: "people-runtime", Operations: invoke},
		solutionhost.AuthorityBinding{ID: "sweep", Audience: "documents-runtime", Operations: map[string][]string{"headless": {"document:read"}}},
		solutionhost.AuthorityBinding{ID: "ghost", Audience: "not-applied", Operations: invoke},
		solutionhost.AuthorityBinding{ID: "notes", Audience: "notes-runtime", Operations: invoke},
	)
	bindings := []*accountsv1.SolutionHostBindingState{
		page,
		reachingBinding(t, "records", "rows-runtime"),
		reachingBinding(t, "documents", "documents-runtime"),
		// Two aliases carrying one principal: neither is the module reached.
		reachingBinding(t, "notes-b", "notes-runtime"),
		reachingBinding(t, "notes-a", "notes-runtime"),
	}
	s := (&appliedBindingsRegistry{bindings: bindings})
	snapshot, err := s.List(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"people": {"records"}}, snapshot.reaches)
	require.Equal(t, map[string][]string{"notes-runtime": {"notes-a", "notes-b"}}, snapshot.sharedPrincipals)
}

// The frontend reads a record's reach from the registry snapshot route, so the
// route — not only the join behind it — has to carry it.
func TestTheRegistrySnapshotRouteCarriesARecordsReach(t *testing.T) {
	invoke := map[string][]string{"invoke": {"record:read"}}
	gw, _, _, _ := newGatewayHarness(t)
	gw.solutions.client = &appliedBindingsRegistry{bindings: []*accountsv1.SolutionHostBindingState{
		reachingBinding(t, "people", "people-runtime", solutionhost.AuthorityBinding{ID: "rows", Audience: "rows-runtime", Operations: invoke}),
		reachingBinding(t, "records", "rows-runtime"),
	}}
	require.NoError(t, gw.solutions.refresh(context.Background()))
	req := httptest.NewRequest(http.MethodGet, "/solutions/_registry", nil)
	req.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var snapshot solutionRegistryProjection
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &snapshot))
	reaches := map[string][]string{}
	for _, record := range snapshot.Solutions {
		reaches[record.ID] = record.Reaches
	}
	require.Equal(t, map[string][]string{"people": {"records"}, "records": nil}, reaches)
}
