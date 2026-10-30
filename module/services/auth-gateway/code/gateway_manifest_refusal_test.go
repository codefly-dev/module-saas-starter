package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	modulecontract "github.com/codefly-dev/core/contracts/module"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

// appliedBindingsRegistry serves registrations through the production join
// (snapshotFromAppliedBindings), so the cache sees exactly what it would from
// accounts for these applied bindings.
type appliedBindingsRegistry struct {
	bindings []*accountsv1.SolutionHostBindingState
}

func (r appliedBindingsRegistry) List(context.Context, *accountsv1.ListSolutionRegistrationsRequest) (*solutionRegistrySnapshot, error) {
	resp := &accountsv1.ListSolutionRegistrationsResponse{RegistryRevision: 1}
	for _, binding := range r.bindings {
		resp.Registrations = append(resp.Registrations, binding.GetRegistration())
	}
	return snapshotFromAppliedBindings(resp, &accountsv1.ListSolutionHostBindingsResponse{Bindings: r.bindings}), nil
}

// A delivered manifest this gateway's parser refuses used to vanish: no log
// line, no route, and a registration that still read as declared. It is now
// logged once with its alias, generation and reason, and the operator snapshot
// carries the refusal on the record.
func TestADroppedAppliedManifestIsReportedNotSilent(t *testing.T) {
	binding := principalBinding(t, "example-runtime", 3)
	principal := regexp.MustCompile(`(?m)^(\s*)principal: example-runtime$`)
	require.Regexp(t, principal, binding.Applied.Generation.Document)
	binding.Applied.Generation.Document = principal.ReplaceAllString(binding.Applied.Generation.Document,
		"${1}principal: example-runtime\n${1}field_from_a_newer_core: x")

	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	gw, _, _, _ := newGatewayHarness(t)
	gw.solutions.client = appliedBindingsRegistry{bindings: []*accountsv1.SolutionHostBindingState{binding}}
	require.NoError(t, gw.solutions.refresh(context.Background()))
	require.NoError(t, gw.solutions.refresh(context.Background()))

	require.Equal(t, 1, strings.Count(logs.String(), "applied manifest dropped"), "logged once, not every reconcile: %s", logs.String())
	require.Contains(t, logs.String(), `WARN: solution registry: applied manifest dropped: alias="documents" generation=3`)
	require.Contains(t, logs.String(), "field_from_a_newer_core")

	routing, resolution := gw.solutions.resolveRouting(context.Background(), "documents")
	require.Nil(t, routing)
	require.Equal(t, solutionNotActive, resolution, "the record is still declared; it has no route")

	req := httptest.NewRequest(http.MethodGet, "/solutions/_registry", nil)
	req.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var snapshot solutionRegistryProjection
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &snapshot))
	require.Len(t, snapshot.Solutions, 1)
	require.Equal(t, "documents", snapshot.Solutions[0].ID)
	require.Contains(t, snapshot.Solutions[0].Refusal, "generation 3: this gateway's parser refuses the applied manifest")
	require.Contains(t, snapshot.Solutions[0].Refusal, "field_from_a_newer_core")
}

// The installation declarations a solution's manifest now carries are fields
// this gateway's parser knows. Pinning a Core that does not know them drops
// every such manifest, which is the failure the test above makes visible.
func TestTheGatewayParserReadsInstallationDeclarations(t *testing.T) {
	binding := principalBinding(t, "example-runtime", 1)
	manifest, refusal := readAppliedManifest(binding)
	require.NoError(t, refusal)
	manifest.Authority.InstallationRole = "none"
	manifest.Authority.EdgeDependencies = []modulecontract.EdgeDependency{{Module: "documents", Presence: "required"}}
	body, err := solutionhost.Marshal(manifest)
	require.NoError(t, err)
	require.Contains(t, string(body), "installation_role: none")
	require.Contains(t, string(body), "edge_dependencies:")
	binding.Applied.Generation.Document = string(body)

	got, refusal := readAppliedManifest(binding)
	require.NoError(t, refusal)
	require.Equal(t, "none", got.Authority.InstallationRole)
	require.Equal(t, []modulecontract.EdgeDependency{{Module: "documents", Presence: "required"}}, got.Authority.EdgeDependencies)
	require.Empty(t, principalSnapshot(binding, binding.Registration).refusals)
}
