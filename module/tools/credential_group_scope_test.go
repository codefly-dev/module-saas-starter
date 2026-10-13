package tools

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A service is delivered every group it declares, whether it reads it or not.
// So a credential's blast radius is the set of services that declare the group
// it travels in — which is a fact about the MANIFESTS, not about who calls what.
//
// The frontend is a public-facing process. It declares `identity` because it
// renders the login page from that group's public facts: the issuer, the client
// id, the display name, the scope. A deployment that puts the identity provider's
// own credentials in that same group therefore hands them to the frontend too,
// which reads neither — and a public process holding the provider's administration
// credential widens what a frontend compromise is worth, for nothing.
//
// The answer is which GROUP a credential lives in, so that is what this holds.
// Every group listed here carries credentials, and this says which services may
// declare it. A group added to a service's manifest and not here passes; a
// credential group reaching a service not listed fails, named.
var credentialGroupHolders = map[string][]string{
	// The provider credentials that authenticate this host TO the identity
	// provider. accounts performs the authorization-code exchange and runs the
	// optional administration adapter; nothing else reads either.
	"identity-provider": {"accounts"},
	// The credential accounts believes forwarded identity on. Only the identity
	// plane — the service that stamps it and the service that trusts it.
	"gateway-trust": {"accounts", "auth-gateway"},
	// The GitHub App's signing key and webhook secret: deployment custody, read
	// only by accounts.
	"github-app": {"accounts"},
	// The cell's Vault credentials.
	"vault": {"accounts"},
}

func TestACredentialGroupIsDeclaredOnlyByServicesThatReadIt(t *testing.T) {
	declaredBy := map[string][]string{}
	for _, service := range servicesWithManifests(t) {
		for group := range declaredWorkspaceGroups(t, service) {
			declaredBy[group] = append(declaredBy[group], service)
		}
	}

	for group, allowed := range credentialGroupHolders {
		holders := declaredBy[group]
		sort.Strings(holders)
		permitted := map[string]bool{}
		for _, service := range allowed {
			permitted[service] = true
		}
		for _, service := range holders {
			if !permitted[service] {
				t.Errorf("service %q declares the credential group %q, which carries credentials it does not read; "+
					"a declared group is delivered whether it is read or not, so this widens what a compromise of %s is worth. "+
					"Permitted holders: %s",
					service, group, service, strings.Join(allowed, ", "))
			}
		}
		if len(holders) == 0 {
			t.Errorf("no service declares the credential group %q; either it is dead and should be deleted, "+
				"or a service reads it undeclared and gets nothing in every deployed cell", group)
		}
	}
}

// servicesWithManifests lists every service of this module, so the gate above
// cannot be satisfied by a service it forgot to look at.
func servicesWithManifests(t *testing.T) []string {
	t.Helper()
	base := filepath.Join(findModuleDir(t), "services")
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("read %s: %v", base, err)
	}
	var services []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, statErr := os.Stat(filepath.Join(base, entry.Name(), "service.codefly.yaml")); statErr != nil {
			continue
		}
		services = append(services, entry.Name())
	}
	if len(services) == 0 {
		t.Fatal("no service manifests found; this gate now proves nothing")
	}
	sort.Strings(services)
	return services
}

// The gateway-provenance credential makes accounts believe a forwarded identity,
// so a holder of it can assert any user to accounts. The gate above keeps the
// GROUP to the identity plane; this keeps the KEY to that group.
//
// The defect it closes is the other way a credential spreads: not a service
// declaring a group it does not read, but a key appearing in a second group that
// many services do read. A `CODEFLY_GATEWAY_TOKEN` in `internal-auth` reaches
// every service that declares the internal token, which is nearly all of them, and
// the name alone makes it look like the same credential the gateway stamps — so a
// composition can be built where it is, and accounts then believes forwarded
// identity from anything that holds it.
func TestTheGatewayCredentialKeyAppearsInNoOtherGroup(t *testing.T) {
	moduleDir := findModuleDir(t)
	base := filepath.Join(moduleDir, "configurations", "local")
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("read %s: %v", base, err)
	}

	found := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		group := strings.TrimSuffix(strings.TrimSuffix(entry.Name(), ".env"), ".secret")
		data, readErr := os.ReadFile(filepath.Join(base, entry.Name()))
		if readErr != nil {
			t.Fatalf("read %s: %v", entry.Name(), readErr)
		}
		for _, line := range strings.Split(string(data), "\n") {
			key, _, assigned := strings.Cut(strings.TrimSpace(line), "=")
			if !assigned || strings.HasPrefix(key, "#") {
				continue
			}
			if !strings.Contains(key, "GATEWAY_TOKEN") {
				continue
			}
			found++
			if group != "gateway-trust" {
				t.Errorf("the group %q declares %q: the gateway-provenance credential belongs to `gateway-trust`, "+
					"which only the identity plane declares. In any other group it reaches every service that "+
					"declares that group, and accounts then believes forwarded identity from each of them",
					group, key)
			}
		}
	}
	if found == 0 {
		t.Fatal("no GATEWAY_TOKEN key found in any shipped default; the key was renamed and this gate now proves nothing")
	}
}
