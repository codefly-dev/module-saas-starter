package business

import (
	"encoding/json"
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

// Only a narrowing needs the policy-log witness. The comparison is therefore
// the security boundary of an envelope write: whatever it does not recognise
// as "at least everything that was granted" has to read as a narrowing.
func TestAnEnvelopeChangeIsAWideningOnlyWhenNothingGrantedIsLost(t *testing.T) {
	value := func(t *testing.T, raw string) any {
		t.Helper()
		var decoded any
		require.NoError(t, json.Unmarshal([]byte(raw), &decoded))
		return decoded
	}
	for _, tc := range []struct {
		name          string
		before, after string
		widens        bool
	}{
		{"unchanged", `{"a":[{"id":"x","actions":["read"]}]}`, `{"a":[{"id":"x","actions":["read"]}]}`, true},
		{"a key added", `{"a":true}`, `{"a":true,"b":["x"]}`, true},
		{"an element added", `{"a":["x"]}`, `{"a":["x","y"]}`, true},
		{"an element added to an identified object", `[{"id":"x","actions":["read"]}]`, `[{"id":"x","actions":["read","write"]}]`, true},
		{"an identified object added", `[{"id":"x"}]`, `[{"id":"x"},{"id":"y"}]`, true},
		{"a list reordered", `["x","y"]`, `["y","x"]`, true},
		{"false becomes true", `{"members":false}`, `{"members":true}`, true},
		{"an empty string is set", `{"tenant":""}`, `{"tenant":"t1"}`, true},
		{"an empty key dropped", `{"a":[],"b":true}`, `{"b":true}`, true},

		{"a key removed", `{"a":true,"b":["x"]}`, `{"a":true}`, false},
		{"an element removed", `{"a":["x","y"]}`, `{"a":["x"]}`, false},
		{"an action removed from an identified object", `[{"id":"x","actions":["read","write"]}]`, `[{"id":"x","actions":["read"]}]`, false},
		{"an identified object removed", `[{"id":"x"},{"id":"y"}]`, `[{"id":"x"}]`, false},
		{"an identified object renamed", `[{"id":"x","actions":["read"]}]`, `[{"id":"y","actions":["read"]}]`, false},
		{"true becomes false", `{"members":true}`, `{"members":false}`, false},
		{"a string changed", `{"tenant":"t1"}`, `{"tenant":"t2"}`, false},
		{"a string cleared", `{"tenant":"t1"}`, `{"tenant":""}`, false},
		{"a number changed", `{"limit":5}`, `{"limit":6}`, false},
		{"a list replaced by an object", `{"a":["x"]}`, `{"a":{"x":true}}`, false},
		{"an object replaced by a scalar", `{"a":{"x":true}}`, `{"a":true}`, false},
		{"a value replaced by null", `{"a":["x"]}`, `{"a":null}`, false},
		{"an unidentified object changed", `[{"kind":"k","actions":["read","write"]}]`, `[{"kind":"k","actions":["read"]}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.widens, envelopeValueWidens(value(t, tc.before), value(t, tc.after)))
		})
	}
}
