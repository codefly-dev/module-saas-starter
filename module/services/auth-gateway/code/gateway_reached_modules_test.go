package main

import (
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
		solutionhost.AuthorityBinding{ID: "rows", Audience: "record-store", Operations: invoke},
		solutionhost.AuthorityBinding{ID: "rows-again", Audience: "record-store", Operations: invoke},
		solutionhost.AuthorityBinding{ID: "self", Audience: "people-runtime", Operations: invoke},
		solutionhost.AuthorityBinding{ID: "sweep", Audience: "documents-runtime", Operations: map[string][]string{"headless": {"document:read"}}},
		solutionhost.AuthorityBinding{ID: "ghost", Audience: "not-applied", Operations: invoke},
	)
	records := reachingBinding(t, "records", "record-store")
	documents := reachingBinding(t, "documents", "documents-runtime")
	var registrations []*accountsv1.SolutionRegistration
	for _, b := range []*accountsv1.SolutionHostBindingState{page, records, documents} {
		registrations = append(registrations, b.Registration)
	}
	s := snapshotFromAppliedBindings(&accountsv1.ListSolutionRegistrationsResponse{Registrations: registrations},
		&accountsv1.ListSolutionHostBindingsResponse{Bindings: []*accountsv1.SolutionHostBindingState{page, records, documents}})
	require.Equal(t, map[string][]string{"people": {"records"}}, s.reaches)
}
