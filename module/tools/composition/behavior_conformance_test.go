package composition

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	updatev0 "github.com/codefly-dev/core/generated/go/codefly/update/v0"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	extAuthzSource   = "services/auth-gateway/code/ext_authz.go"
	minterSource     = "services/accounts/code/pkg/auth/ed25519/minter.go"
	hostRuntime      = "services/frontend/code/src/solutions/host-runtime.ts"
	federationConfig = "configurations/local/federation.env"
)

func contractByID(t *testing.T, contracts *updatev0.BehavioralContracts, id string) *structpb.Struct {
	t.Helper()
	for _, contract := range contracts.Contracts {
		if contract.Id == id {
			return contract.Contract
		}
	}
	t.Fatalf("behavioral contract %q is not declared", id)
	return nil
}

// at walks a declared contract by field path, so a conformance check names the
// exact assertion it is holding the implementation to.
func at(t *testing.T, content *structpb.Struct, path ...string) *structpb.Value {
	t.Helper()
	var value *structpb.Value = structpb.NewStructValue(content)
	for i, field := range path {
		object := value.GetStructValue()
		if object == nil {
			t.Fatalf("declared path %s is not an object", strings.Join(path[:i], "."))
		}
		next, ok := object.Fields[field]
		if !ok {
			t.Fatalf("declared contract has no %s", strings.Join(path[:i+1], "."))
		}
		value = next
	}
	return value
}

func declaredStrings(t *testing.T, value *structpb.Value) []string {
	t.Helper()
	list := value.GetListValue()
	if list == nil {
		t.Fatal("declared value is not a list")
	}
	values := make([]string, 0, len(list.Values))
	for _, entry := range list.Values {
		values = append(values, entry.GetStringValue())
	}
	return values
}

// declaredToken returns a contract's token declaration, whether the contract
// states the exchange directly or as a numbered handshake.
func declaredToken(t *testing.T, content *structpb.Struct) map[string]*structpb.Value {
	t.Helper()
	if token, ok := content.Fields["token"]; ok {
		return token.GetStructValue().Fields
	}
	for _, step := range content.Fields["handshake"].GetListValue().GetValues() {
		if token, ok := step.GetStructValue().GetFields()["token"]; ok {
			return token.GetStructValue().Fields
		}
	}
	t.Fatal("contract declares no minted token")
	return nil
}

func readSource(t *testing.T, moduleRoot, relative string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(moduleRoot, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatalf("read %s: %v", relative, err)
	}
	return string(data)
}

// quotedBlock returns the quoted strings of the literal block opened by start,
// up to its closing brace.
func quotedBlock(t *testing.T, source, start string) []string {
	t.Helper()
	index := strings.Index(source, start)
	if index < 0 {
		t.Fatalf("source does not declare %s", start)
	}
	rest := source[index+len(start):]
	end := strings.Index(rest, "}")
	if end < 0 {
		t.Fatalf("declaration %s is not terminated", start)
	}
	return regexp.MustCompile(`"([^"]+)"`).FindAllString(rest[:end], -1)
}

func unquote(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strings.Trim(value, `"`))
	}
	slices.Sort(out)
	return out
}

// TestGatewayIdentityHeadersMatchTheDeclaredContract is the check that makes the
// declaration load-bearing: an upstream reads identity only from these headers,
// so adding, renaming or dropping one changes a contract every consumer depends
// on and must move the published digest with it.
func TestGatewayIdentityHeadersMatchTheDeclaredContract(t *testing.T) {
	moduleRoot := findModuleRoot(t)
	content := contractByID(t, loadBehavioralContracts(t, moduleRoot), "authorization.gateway.identity-headers")
	source := readSource(t, moduleRoot, extAuthzSource)

	declared := declaredStrings(t, at(t, content, "stampedHeaders"))
	slices.Sort(declared)
	stamped := unquote(quotedBlock(t, source, "canonicalUpstreamAuthHeaders = []string{"))
	if !slices.Equal(declared, stamped) {
		t.Errorf("declared stamped identity headers do not match %s\n  declared: %v\n  stamped:  %v", extAuthzSource, declared, stamped)
	}

	kinds := declaredStrings(t, at(t, content, "credentialKinds"))
	slices.Sort(kinds)
	var implemented []string
	for _, constant := range []string{"credentialKindSession", "credentialKindAPIKey"} {
		match := regexp.MustCompile(constant + `\s*=\s*"([^"]+)"`).FindStringSubmatch(source)
		if match == nil {
			t.Fatalf("%s does not declare %s", extAuthzSource, constant)
		}
		implemented = append(implemented, match[1])
	}
	slices.Sort(implemented)
	if !slices.Equal(kinds, implemented) {
		t.Errorf("declared credential kinds %v do not match %s %v", kinds, extAuthzSource, implemented)
	}
}

// TestSolutionRuntimeCompatibilityMatchesTheHost holds the federation contract
// to the single place the register route and the Module-Federation host both
// read, so the declared majors and shared scope cannot drift from the host.
func TestSolutionRuntimeCompatibilityMatchesTheHost(t *testing.T) {
	moduleRoot := findModuleRoot(t)
	content := contractByID(t, loadBehavioralContracts(t, moduleRoot), "federation.solution.runtime-compatibility")
	source := readSource(t, moduleRoot, hostRuntime)

	for _, name := range []string{"SOLUTION_MANIFEST_SCHEMA_MAJOR", "SOLUTION_HOST_CONTRACT_MAJOR"} {
		match := regexp.MustCompile(`export const ` + name + ` = (\d+);`).FindStringSubmatch(source)
		if match == nil {
			t.Fatalf("%s does not export %s", hostRuntime, name)
		}
		declared := fmt.Sprintf("%d", int(at(t, content, "hostMajors", name).GetNumberValue()))
		if declared != match[1] {
			t.Errorf("declared %s = %s, host serves %s", name, declared, match[1])
		}
	}

	declared := declaredStrings(t, at(t, content, "sharedScope", "keys"))
	slices.Sort(declared)
	index := strings.Index(source, "HOST_SHARED_VERSIONS: Readonly<Record<string, string>> = {")
	if index < 0 {
		t.Fatalf("%s does not export HOST_SHARED_VERSIONS", hostRuntime)
	}
	body := source[index+strings.Index(source[index:], "{")+1:]
	body = body[:strings.Index(body, "};")]
	var published []string
	for _, line := range regexp.MustCompile(`(?m)^\s*(?:"([^"]+)"|([A-Za-z_$][\w$]*))\s*:`).FindAllStringSubmatch(body, -1) {
		if name := line[1] + line[2]; name != "" {
			published = append(published, name)
		}
	}
	slices.Sort(published)
	if !slices.Equal(declared, published) {
		t.Errorf("declared Module-Federation shared scope does not match %s\n  declared:  %v\n  published: %v", hostRuntime, declared, published)
	}
}

// TestRegistrationCredentialsMatchTheMinter ties each declared credential to the
// audience, subject and lifetime accounts actually mints.
func TestRegistrationCredentialsMatchTheMinter(t *testing.T) {
	moduleRoot := findModuleRoot(t)
	contracts := loadBehavioralContracts(t, moduleRoot)
	minter := readSource(t, moduleRoot, minterSource)

	for _, credential := range []struct {
		contract  string
		constant  string
		subject   string
		principal string
	}{
		{"registration.solution.credential", "SolutionRegistrationAudience", "solution:<id>", `"solution:" + solutionID`},
		{"registration.module.rest-federation", "ModuleRegistrationAudience", "module:<prefix>", `"module:" + prefix`},
	} {
		content := contractByID(t, contracts, credential.contract)
		fields := declaredToken(t, content)

		match := regexp.MustCompile(`const ` + credential.constant + ` = "([^"]+)"`).FindStringSubmatch(minter)
		if match == nil {
			t.Fatalf("%s does not declare %s", minterSource, credential.constant)
		}
		if audience := fields["audience"].GetStringValue(); audience != match[1] {
			t.Errorf("contract %q declares audience %q, minter issues %q", credential.contract, audience, match[1])
		}
		if subject := fields["subject"].GetStringValue(); subject != credential.subject {
			t.Errorf("contract %q declares subject %q, minter builds %s", credential.contract, subject, credential.principal)
		}
		if !strings.Contains(minter, credential.principal) {
			t.Errorf("%s no longer builds the subject %s that contract %q declares", minterSource, credential.principal, credential.contract)
		}
		if ttl := fields["ttlSeconds"].GetNumberValue(); ttl != 300 {
			t.Errorf("contract %q declares ttlSeconds %v", credential.contract, ttl)
		}
	}

	if !strings.Contains(minter, "registrationTTL = 5 * time.Minute") {
		t.Errorf("%s no longer mints registration credentials with the declared 300-second lifetime", minterSource)
	}
}

// TestDeclaredRegistrationSurfacesExist holds every declared route, header and
// configuration key to the source that serves it. A renamed surface is the
// silent break this declaration exists to publish.
func TestDeclaredRegistrationSurfacesExist(t *testing.T) {
	moduleRoot := findModuleRoot(t)
	gateway := readSource(t, moduleRoot, "services/auth-gateway/code/gateway_solutions.go") +
		readSource(t, moduleRoot, "services/auth-gateway/code/gateway_modules.go") +
		readSource(t, moduleRoot, "services/auth-gateway/code/gateway_solution_credential.go")
	federation := readSource(t, moduleRoot, federationConfig)

	for _, surface := range []struct{ source, literal string }{
		{gateway, `solutionPrefix = "/solutions/"`},
		{gateway, `"_registration-token"`},
		{gateway, `"_register"`},
		{gateway, `"_registry"`},
		{gateway, "/modules/_registration-token"},
		{gateway, "/modules/_register"},
		{gateway, "/modules/_work-context"},
		{gateway, "X-Codefly-Internal-Token"},
		{gateway, "X-Codefly-Solution-Secret"},
		{gateway, "X-Codefly-Solution-Registration"},
		{gateway, "X-Codefly-Module-Secret"},
		{gateway, "X-Codefly-Module-Registration"},
		{federation, "SOLUTION_REGISTRATION_SECRETS"},
		{federation, "MODULE_REGISTRATION_SECRETS"},
		{federation, "MODULE_IDENTITY_SECRETS"},
	} {
		if !strings.Contains(surface.source, surface.literal) {
			t.Errorf("declared surface %q is no longer served", surface.literal)
		}
	}

	if _, err := os.Stat(filepath.Join(moduleRoot, "services/frontend/code/src/app/api/solutions/register/route.ts")); err != nil {
		t.Errorf("declared frontend registration route is absent: %v", err)
	}
	// The numbered publisher-binding migration was folded into the baseline, so
	// the durable evidence is the invariant it established, not its file name.
	baseline := readSource(t, moduleRoot, "services/store/migrations/1_baseline.up.sql")
	for _, invariant := range []string{"publisher text NOT NULL", "solution_registrations_publisher_check"} {
		if !strings.Contains(baseline, invariant) {
			t.Errorf("declared publisher binding is no longer carried by the store baseline: %q absent", invariant)
		}
	}
}
