package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The marker list exists in three places, and they must agree.
//
// `configuration_defaults_test.go` at the repository root REQUIRES every shipped
// secret default to carry one, so a real credential can never be committed as a
// default. accounts and auth-gateway REFUSE a value carrying one outside local
// development, because a value carrying one is a value everybody knows: these
// files ride verbatim in the immutable module package, out of a public repository.
//
// The three copies are separate Go modules with no shared local package, so the
// agreement is checked at the text level. A marker added only to the shipping gate
// leaves a published value a cell would accept; one added only to a service
// refuses a default that side still ships, which is a boot failure in local
// development. Neither is visible in the diff that causes it.
//
// Same for the length floor: the two services must not disagree about how strong a
// perimeter credential has to be, or the gateway and accounts admit different
// populations of value.
func TestShippedPlaceholderMarkersMatchTheShippingGate(t *testing.T) {
	moduleDir := findModuleDir(t)
	repoRoot := filepath.Dir(moduleDir)

	sources := map[string]string{
		"the shipping gate (configuration_defaults_test.go)": filepath.Join(repoRoot, "configuration_defaults_test.go"),
		"accounts":     filepath.Join(moduleDir, "services", "accounts", "code", "shipped_placeholder.go"),
		"auth-gateway": filepath.Join(moduleDir, "services", "auth-gateway", "code", "shipped_placeholder.go"),
	}

	var reference []string
	var referenceName string
	for name, path := range sources {
		markers := placeholderMarkersIn(t, name, path)
		if reference == nil {
			reference, referenceName = markers, name
			continue
		}
		if strings.Join(markers, ",") != strings.Join(reference, ",") {
			t.Errorf("placeholder markers differ: %s has %v, %s has %v — "+
				"a marker on only one side either leaves a published value a cell accepts, "+
				"or refuses a default that side still ships",
				referenceName, reference, name, markers)
		}
	}
	if len(reference) == 0 {
		t.Fatal("no placeholder markers were found in any source")
	}
}

func TestPerimeterCredentialFloorsAgree(t *testing.T) {
	moduleDir := findModuleDir(t)
	floors := map[string]string{}
	for _, service := range []string{"accounts", "auth-gateway"} {
		path := filepath.Join(moduleDir, "services", service, "code", "shipped_placeholder.go")
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		match := regexp.MustCompile(`minimumPerimeterCredentialLength = (\d+)`).FindSubmatch(source)
		if match == nil {
			t.Fatalf("%s declares no minimumPerimeterCredentialLength", path)
		}
		floors[service] = string(match[1])
	}
	if floors["accounts"] != floors["auth-gateway"] {
		t.Errorf("perimeter credential floors differ: accounts %s, auth-gateway %s — "+
			"the two would admit different populations of value",
			floors["accounts"], floors["auth-gateway"])
	}
}

// placeholderMarkersIn reads the sorted marker literals out of a
// `...Markers = []string{...}` declaration, whatever the variable is called on
// each side.
func placeholderMarkersIn(t *testing.T, name, path string) []string {
	t.Helper()
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s (%s): %v", name, path, err)
	}
	block := regexp.MustCompile(`(?s)laceholderMarkers = \[\]string\{(.*?)\}`).FindSubmatch(source)
	if block == nil {
		t.Fatalf("%s (%s): no placeholder marker declaration found", name, path)
	}
	var markers []string
	for _, literal := range regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(block[1], -1) {
		markers = append(markers, string(literal[1]))
	}
	if len(markers) == 0 {
		t.Fatalf("%s (%s): the marker declaration is empty", name, path)
	}
	sort.Strings(markers)
	return markers
}
