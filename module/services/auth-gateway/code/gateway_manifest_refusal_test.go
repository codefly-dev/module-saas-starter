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
	// bindings is replaced between refreshes, never during one.
	bindings []*accountsv1.SolutionHostBindingState
}

func (r *appliedBindingsRegistry) List(context.Context, *accountsv1.ListSolutionRegistrationsRequest) (*solutionRegistrySnapshot, error) {
	resp := &accountsv1.ListSolutionRegistrationsResponse{RegistryRevision: 1}
	for _, binding := range r.bindings {
		resp.Registrations = append(resp.Registrations, binding.GetRegistration())
	}
	return snapshotFromAppliedBindings(resp, &accountsv1.ListSolutionHostBindingsResponse{Bindings: r.bindings}), nil
}

// refusalHarness is a gateway whose registry serves the given applied binding
// through the production join, logging into a buffer the test reads.
func refusalHarness(t *testing.T, binding *accountsv1.SolutionHostBindingState) (*Gateway, *appliedBindingsRegistry, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	gw, _, _, _ := newGatewayHarness(t)
	registry := &appliedBindingsRegistry{bindings: []*accountsv1.SolutionHostBindingState{binding}}
	gw.solutions.client = registry
	return gw, registry, &logs
}

// projectedRefusal is the record's `refusal` in the operator snapshot.
func projectedRefusal(t *testing.T, gw *Gateway) string {
	t.Helper()
	require.NoError(t, gw.solutions.refresh(context.Background()))
	req := httptest.NewRequest(http.MethodGet, "/solutions/_registry", nil)
	req.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var snapshot solutionRegistryProjection
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &snapshot))
	require.Len(t, snapshot.Solutions, 1)
	require.Equal(t, "documents", snapshot.Solutions[0].ID)
	return snapshot.Solutions[0].Refusal
}

// withDocumentLine rewrites one line of the binding's applied document.
func withDocumentLine(t *testing.T, binding *accountsv1.SolutionHostBindingState, line, replacement string) {
	t.Helper()
	pattern := regexp.MustCompile(`(?m)^(\s*)` + regexp.QuoteMeta(line) + `$`)
	require.Regexp(t, pattern, binding.Applied.Generation.Document)
	binding.Applied.Generation.Document = pattern.ReplaceAllString(binding.Applied.Generation.Document, "${1}"+replacement)
}

// A delivered manifest this gateway's parser refuses used to vanish: no log
// line, no route, and a registration that still read as declared. It is now
// logged once with its alias, generation and full reason, and the operator
// snapshot carries the refusal's class on the record — never the reason, which
// can quote an address the snapshot must not expose.
func TestADroppedAppliedManifestIsReportedNotSilent(t *testing.T) {
	binding := principalBinding(t, "example-runtime", 3)
	withDocumentLine(t, binding, "principal: example-runtime", "principal: example-runtime\n${1}field_from_a_newer_core: x")
	gw, _, logs := refusalHarness(t, binding)

	refusal := projectedRefusal(t, gw)
	require.NoError(t, gw.solutions.refresh(context.Background()))
	require.Equal(t, 1, strings.Count(logs.String(), "applied manifest dropped"), "logged once, not every reconcile: %s", logs.String())
	require.Contains(t, logs.String(), `WARN: solution registry: applied manifest dropped: alias="documents" generation=3`)
	require.Contains(t, logs.String(), "field_from_a_newer_core")

	routing, resolution := gw.solutions.resolveRouting(context.Background(), "documents")
	require.Nil(t, routing)
	require.Equal(t, solutionNotActive, resolution, "the record is still declared; it has no route")
	require.Equal(t, "generation 3: "+errManifestUnparsable.Error(), refusal)
}

// Core's parser quotes a malformed endpoint address in its error. The log
// keeps it for the operator; the snapshot, which exposes no address, does not.
func TestARefusalProjectsNoAddress(t *testing.T) {
	binding := principalBinding(t, "example-runtime", 3)
	withDocumentLine(t, binding, "address: http://documents:8080", "address: http://documents:8080/%zz")
	gw, _, logs := refusalHarness(t, binding)

	refusal := projectedRefusal(t, gw)
	require.Contains(t, logs.String(), "documents:8080", "the full reason is logged")
	require.Equal(t, "generation 3: "+errManifestUnparsable.Error(), refusal)
	require.NotContains(t, refusal, "documents:8080")
}

// A refusal is a fact about one applied generation. A later readable one
// clears it, and a changed reason is logged again rather than deduplicated away.
func TestARefusalFollowsTheAppliedGeneration(t *testing.T) {
	unreadable := principalBinding(t, "example-runtime", 3)
	withDocumentLine(t, unreadable, "principal: example-runtime", "principal: example-runtime\n${1}field_from_a_newer_core: x")
	gw, registry, logs := refusalHarness(t, unreadable)
	require.Equal(t, "generation 3: "+errManifestUnparsable.Error(), projectedRefusal(t, gw))

	// A different reason for the same alias: the document names generation 4
	// while the registration and the applied generation say 3.
	mismatched := principalBinding(t, "example-runtime", 4)
	mismatched.Applied.Generation.Generation = 3
	mismatched.Registration.Declared.Generation = 3
	registry.bindings[0] = mismatched
	require.Equal(t, "generation 3: "+errManifestMismatch.Error(), projectedRefusal(t, gw))
	require.Equal(t, 2, strings.Count(logs.String(), "applied manifest dropped"), "a changed reason is logged again: %s", logs.String())

	registry.bindings[0] = principalBinding(t, "example-runtime", 4)
	require.Empty(t, projectedRefusal(t, gw), "a readable generation clears the refusal")
	require.Equal(t, 2, strings.Count(logs.String(), "applied manifest dropped"))
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
