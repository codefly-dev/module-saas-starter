package meshtransport_test

import (
	"errors"
	"testing"

	"accounts/pkg/meshtransport"
)

type source map[string]string

func (s source) WorkspaceValue(group, key string) (string, error) {
	value, ok := s[group+"/"+key]
	if !ok {
		// The SDK reports an absent value as an error, not an empty string.
		return "", errors.New("no workspace configuration value")
	}
	return value, nil
}

// Only the exact value asserts the mesh. Unset, empty and false keep plaintext
// refused; anything else fails startup rather than being read as either answer,
// because a misspelled assertion must not pass for an absent one nor a present
// one. The value is trimmed first: configuration delivered as a file carries the
// newline the file ends with, and a correct assertion must not be refused over
// a byte nobody can see.
func TestProtectedIsFailClosedAndExact(t *testing.T) {
	const setting = meshtransport.Group + "/" + meshtransport.Key
	for raw, want := range map[string]bool{
		"true":      true,
		" true\n":   true,
		"false":     false,
		"":          false,
		"  \n":      false,
		"\tfalse  ": false,
	} {
		got, err := meshtransport.Protected(source{setting: raw})
		if err != nil {
			t.Errorf("%q: %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("%q asserted %v, want %v", raw, got, want)
		}
	}

	if got, err := meshtransport.Protected(source{}); got || err != nil {
		t.Errorf("an absent assertion = %v, %v; want false with no error", got, err)
	}

	for _, raw := range []string{"yes", "True", "1", "TRUE", "on", "mesh"} {
		got, err := meshtransport.Protected(source{setting: raw})
		if err == nil {
			t.Errorf("%q was read as %v rather than refused", raw, got)
			continue
		}
		if got {
			t.Errorf("%q refused but still reported protected", raw)
		}
	}
}

// A hostname cannot settle whether a mesh wraps the wire, so the rule admits
// only what a mesh can cover: a Service inside this cluster. The "svc" label
// may not be one of the first two, because svc.example.com and
// vault.svc.example.com are somebody else's hosts and are externally routable
// despite carrying the label.
func TestClusterServiceHost(t *testing.T) {
	for host, want := range map[string]bool{
		"vault.vault.svc":                     true,
		"vault.vault.svc:8200":                true,
		"vault.vault.svc.cluster.local":       true,
		"vault.vault.svc.cluster.local:8200":  true,
		"VAULT.VAULT.SVC.CLUSTER.LOCAL:8200":  true,
		"vault.vault.svc.cluster.local.:8200": true,
		"accounts.saas-starter.svc":           true,

		"svc.example.com":           false,
		"svc.example.com:8200":      false,
		"vault.svc.example.com":     false,
		"vault.svc":                 false,
		"vault":                     false,
		"vault.internal":            false,
		"vault.example.com":         false,
		"10.0.0.5":                  false,
		"10.0.0.5:8200":             false,
		"localhost":                 false,
		"..svc":                     false,
		"vault..svc.cluster.local":  false,
		"vault.vault.service.local": false,
	} {
		if got := meshtransport.ClusterServiceHost(host); got != want {
			t.Errorf("ClusterServiceHost(%q) = %v, want %v", host, got, want)
		}
	}
}

// Both conditions, never one. The assertion alone admits nothing, and the
// address alone admits nothing.
func TestAdmitsRequiresBoth(t *testing.T) {
	const inCluster = "http://vault.vault.svc.cluster.local:8200"
	if meshtransport.Admits(false, inCluster) {
		t.Error("an in-cluster address was admitted with no assertion")
	}
	if !meshtransport.Admits(true, inCluster) {
		t.Error("an asserted in-cluster address was refused")
	}
	for _, rawURL := range []string{
		"http://vault.example.com:8200",
		"http://10.0.0.5:8200",
		"http://vault:8200",
		"",
		"://broken",
	} {
		if meshtransport.Admits(true, rawURL) {
			t.Errorf("%q was admitted by an assertion that cannot cover it", rawURL)
		}
	}
}
