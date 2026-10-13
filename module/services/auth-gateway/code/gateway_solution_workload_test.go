package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestAppliedSolutionWorkloadIdentity(t *testing.T) {
	var document *solutionhost.SolutionHostBinding
	for _, fixture := range solutionhost.FixturesOf(solutionhost.DocumentTypePresence) {
		parsed, err := solutionhost.Parse(fixture.Document)
		if err == nil && len(parsed.Workloads) > 0 {
			document = parsed
			break
		}
	}
	require.NotNil(t, document, "Core must ship a readable workload presence fixture")
	body, err := json.Marshal(document)
	require.NoError(t, err)
	binding := &accountsv1.SolutionHostBindingState{
		BindingId:    document.Binding,
		Applied:      &accountsv1.SolutionHostBindingAppliedGeneration{SolutionId: "example", Generation: &accountsv1.SolutionHostBindingGeneration{Generation: document.Generation, Document: string(body)}},
		Registration: &accountsv1.SolutionRegistration{SolutionId: "example", Declared: &accountsv1.SolutionDeclaredBinding{BindingId: document.Binding, Generation: document.Generation}},
	}
	require.Equal(t, document.Workloads[0].Identity.SPIFFEID, appliedWorkloadSPIFFEID(binding))
	for _, test := range []struct {
		name   string
		mutate func(*accountsv1.SolutionHostBindingState)
	}{
		{"another applied generation", func(b *accountsv1.SolutionHostBindingState) { b.Registration.Declared.Generation++ }},
		{"another binding", func(b *accountsv1.SolutionHostBindingState) { b.BindingId += "-other" }},
		{"another registry alias", func(b *accountsv1.SolutionHostBindingState) { b.Applied.SolutionId = "other" }},
		{"tombstone", func(b *accountsv1.SolutionHostBindingState) { b.Applied.Removed = true }},
		{"desired only", func(b *accountsv1.SolutionHostBindingState) { b.Desired = b.Applied.Generation; b.Applied = nil }},
		{"malformed document", func(b *accountsv1.SolutionHostBindingState) { b.Applied.Generation.Document = "{}" }},
		{"no workload", func(b *accountsv1.SolutionHostBindingState) {
			copy := *document
			copy.Workloads = nil
			body, err := json.Marshal(copy)
			require.NoError(t, err)
			b.Applied.Generation.Document = string(body)
		}},
		{"ambiguous workloads", func(b *accountsv1.SolutionHostBindingState) {
			copy := *document
			second := copy.Workloads[0]
			second.Name += "-other"
			second.Identity.SPIFFEID = "spiffe://acme.test/ns/acme-prod/sa/other"
			copy.Workloads = append(append([]solutionhost.Workload{}, copy.Workloads...), second)
			body, err := json.Marshal(copy)
			require.NoError(t, err)
			b.Applied.Generation.Document = string(body)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			b := proto.Clone(binding).(*accountsv1.SolutionHostBindingState)
			test.mutate(b)
			require.Empty(t, appliedWorkloadSPIFFEID(b))
		})
	}
}

// Exercises the actual reverse-proxy path and guarded network dial. Unlike the
// in-memory TLS tests this test requires permission to bind loopback.
func TestSolutionProxyWorkloadIdentity(t *testing.T) {
	gateway := &Gateway{}
	reached := 0
	server := newSolutionTestServer(t, gateway, "example", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached++; _, _ = w.Write([]byte("solution")) }))
	for _, test := range []struct {
		id     string
		status int
	}{
		{"spiffe://acme.test/ns/acme-prod/sa/example", http.StatusOK},
		{"spiffe://acme.test/ns/acme-prod/sa/other", http.StatusBadGateway},
		{"", http.StatusBadGateway},
	} {
		recorder := httptest.NewRecorder()
		gateway.proxyTo(recorder, httptest.NewRequest(http.MethodGet, "/assets/entry.js", nil), MustURL(server.URL), &RouteEntry{Service: "solution:example", WorkloadSPIFFEID: test.id})
		require.Equal(t, test.status, recorder.Code)
	}
	require.Equal(t, 1, reached)
}
