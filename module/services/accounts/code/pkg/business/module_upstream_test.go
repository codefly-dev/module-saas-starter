package business

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// The origin a minting module reports is where the gateway will dial it, so it
// is admitted only as exactly an https host and port on a composition-local
// host — and normalised, so the registry never holds two spellings of one.
func TestAReportedOriginIsAnHTTPSCompositionLocalHostAndPort(t *testing.T) {
	for raw, want := range map[string]string{
		"https://backend.cell-documents.svc.cluster.local:8080":  "https://backend.cell-documents.svc.cluster.local:8080",
		"https://backend.cell-documents.svc:8080":                "https://backend.cell-documents.svc:8080",
		"https://BACKEND.cell-documents.svc.cluster.local:8080/": "https://backend.cell-documents.svc.cluster.local:8080",
		"https://backend:8080":                                   "https://backend:8080",
		"https://localhost:22631":                                "https://localhost:22631",
		"https://127.0.0.1:22631":                                "https://127.0.0.1:22631",
		"https://10.64.3.7:8080":                                 "https://10.64.3.7:8080",
		"https://[fd00::12]:8080":                                "https://[fd00::12]:8080",
	} {
		t.Run(raw, func(t *testing.T) {
			got, err := moduleUpstreamOrigin(raw)
			require.NoError(t, err)
			require.Equal(t, want, got)
		})
	}
}

func TestAReportedOriginThatIsNotOneIsRefused(t *testing.T) {
	for name, raw := range map[string]string{
		"plaintext":                 "http://backend.cell-documents.svc.cluster.local:8080",
		"no port":                   "https://backend.cell-documents.svc.cluster.local",
		"a path":                    "https://backend.cell-documents.svc.cluster.local:8080/api",
		"a query":                   "https://backend.cell-documents.svc.cluster.local:8080?x=1",
		"a bare query mark":         "https://backend.cell-documents.svc.cluster.local:8080?",
		"a fragment":                "https://backend.cell-documents.svc.cluster.local:8080#x",
		"userinfo":                  "https://ops@backend.cell-documents.svc.cluster.local:8080",
		"a public name":             "https://example.com:443",
		"a public address":          "https://8.8.8.8:443",
		"a name that only ends svc": "https://a.b.c.svc:8080",
		"another cluster's suffix":  "https://backend.cell-documents.svc.cluster.example:8080",
		"opaque":                    "https:backend:8080",
		"not a URL":                 "backend:8080",
		"relative":                  "//backend:8080",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := moduleUpstreamOrigin(raw)
			require.Empty(t, got)
			require.True(t, errors.Is(err, ErrModuleUpstreamRefused), "%q: %v", raw, err)
		})
	}
}

// Only a pure grant skips the policy-log witness, so this comparison is the
// security boundary of an envelope write: it must recognise "the same, plus
// builds and whole principals" and nothing else.
func TestAnEnvelopeWriteIsAPureGrantOnlyWhenNothingGrantedChanges(t *testing.T) {
	const before = `{"revision":3,"approved_builds":["sha256:a"],"principals":{"crm":{"bindings":[{"id":"u1","revision":9007199254740992}],"grant":{"tenant":"","queues":["q1"]}}}}`
	for _, tc := range []struct {
		name  string
		after string
		adds  bool
	}{
		{"the same document at the next revision", `{"revision":4,"approved_builds":["sha256:a"],"principals":{"crm":{"bindings":[{"id":"u1","revision":9007199254740992}],"grant":{"tenant":"","queues":["q1"]}}}}`, true},
		{"key order and whitespace differ", `{"principals":{"crm":{"grant":{"queues":["q1"],"tenant":""},"bindings":[{"revision":9007199254740992,"id":"u1"}]}}, "approved_builds":["sha256:a"], "revision":4}`, true},
		{"a build added", `{"revision":4,"approved_builds":["sha256:a","sha256:b"],"principals":{"crm":{"bindings":[{"id":"u1","revision":9007199254740992}],"grant":{"tenant":"","queues":["q1"]}}}}`, true},
		{"a whole principal added", `{"revision":4,"approved_builds":["sha256:a"],"principals":{"crm":{"bindings":[{"id":"u1","revision":9007199254740992}],"grant":{"tenant":"","queues":["q1"]}},"docs":{"grant":{"tenant":"t1"}}}}`, true},

		{"a build removed", `{"revision":4,"approved_builds":[],"principals":{"crm":{"bindings":[{"id":"u1","revision":9007199254740992}],"grant":{"tenant":"","queues":["q1"]}}}}`, false},
		{"a principal removed", `{"revision":4,"approved_builds":["sha256:a"],"principals":{}}`, false},
		{"the principals key removed", `{"revision":4,"approved_builds":["sha256:a"]}`, false},
		{"a queue added inside a granted principal", `{"revision":4,"approved_builds":["sha256:a"],"principals":{"crm":{"bindings":[{"id":"u1","revision":9007199254740992}],"grant":{"tenant":"","queues":["q1","q2"]}}}}`, false},
		{"the shared tenancy pinned to one organization", `{"revision":4,"approved_builds":["sha256:a"],"principals":{"crm":{"bindings":[{"id":"u1","revision":9007199254740992}],"grant":{"tenant":"t1","queues":["q1"]}}}}`, false},
		{"a restricting key that was absent appears", `{"revision":4,"approved_builds":["sha256:a"],"principals":{"crm":{"bindings":[{"id":"u1","revision":9007199254740992,"queue":"q1"}],"grant":{"tenant":"","queues":["q1"]}}}}`, false},
		{"a unit revision moves past what a float can tell apart", `{"revision":4,"approved_builds":["sha256:a"],"principals":{"crm":{"bindings":[{"id":"u1","revision":9007199254740993}],"grant":{"tenant":"","queues":["q1"]}}}}`, false},
		{"an unknown top-level key appears", `{"revision":4,"approved_builds":["sha256:a"],"deny":["crm"],"principals":{"crm":{"bindings":[{"id":"u1","revision":9007199254740992}],"grant":{"tenant":"","queues":["q1"]}}}}`, false},
		{"not a document", `[]`, false},
		{"not JSON", `{"revision":4`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.adds, moduleEnvelopeOnlyAdds([]byte(before), []byte(tc.after)))
		})
	}
	require.False(t, moduleEnvelopeOnlyAdds([]byte(`not json`), []byte(before)), "an envelope in force that cannot be read is never added to")
}
