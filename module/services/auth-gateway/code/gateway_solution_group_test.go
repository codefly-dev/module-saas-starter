package main

import (
	"encoding/json"
	"testing"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
)

func TestSolutionManifestGroupsOnlyDeclaredSolutions(t *testing.T) {
	for _, kind := range []accountsv1.SolutionDeclaredKind{
		accountsv1.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_SOLUTION,
		accountsv1.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_MODULE,
	} {
		record := &accountsv1.SolutionRegistration{
			SolutionId: "example", Declared: &accountsv1.SolutionDeclaredBinding{Kind: kind},
			Frontend: &accountsv1.SolutionFrontendBinding{Manifest: fakeFrontendBlock("example")},
			Group:    &accountsv1.SolutionGroup{Id: "operations", Title: "Operations", Order: 2},
		}
		wire, ok := solutionManifest(record, false)
		require.True(t, ok)
		var projected map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(wire), &projected))
		if kind == accountsv1.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_SOLUTION {
			require.JSONEq(t, `{"id":"operations","title":"Operations","order":2}`, string(projected["group"]))
		} else {
			require.NotContains(t, projected, "group")
		}
		record.Frontend = nil
		_, ok = solutionManifest(record, false)
		require.False(t, ok, "group membership cannot manufacture a frontend")
	}
}
