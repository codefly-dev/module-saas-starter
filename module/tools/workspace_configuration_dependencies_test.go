package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A service reads a workspace configuration group by name at runtime, and
// declares the groups it reads in its manifest. Only the declaration makes the
// platform inject the group, so a group that is read but not declared is not a
// tidiness problem: the read silently returns nothing in every deployed cell.
//
// That failure is invisible in exactly the places it matters most. Locally the
// value is usually absent anyway, or a stray process environment variable
// satisfies the fallback, so the service behaves. In a cell the operator sets
// the value, the platform never carries it, and the service keeps the default —
// so an operator's explicit opt-in silently does nothing and the service fails
// with an error that names something else entirely.
//
// This is the shape that cost us a deployment: accounts read the `vault` group
// to learn whether the Vault hop is protected out of band, never declared it,
// and refused to start in a cell whose operator had set the flag.

// workspaceEnvRead matches the accessor accounts uses to read a group, e.g.
// workspaceEnv("vault", "VAULT_ALLOW_INSECURE_HTTP").
var workspaceEnvRead = regexp.MustCompile(`workspaceEnv\(\s*"([a-z0-9-]+)"`)

// servicesWithWorkspaceReads maps a service to the source tree searched for its
// group reads. Add a service here when it starts reading groups by name.
var servicesWithWorkspaceReads = map[string]string{
	"accounts": "services/accounts/code",
}

func declaredWorkspaceGroups(t *testing.T, service string) map[string]bool {
	t.Helper()
	manifest := readModuleFile(t, "services/"+service+"/service.codefly.yaml")
	var parsed struct {
		WorkspaceConfigurationDependencies []string `yaml:"workspace-configuration-dependencies"`
	}
	if err := yaml.Unmarshal([]byte(manifest), &parsed); err != nil {
		t.Fatalf("parse services/%s/service.codefly.yaml: %v", service, err)
	}
	declared := make(map[string]bool, len(parsed.WorkspaceConfigurationDependencies))
	for _, group := range parsed.WorkspaceConfigurationDependencies {
		declared[group] = true
	}
	return declared
}

func groupsReadInTree(t *testing.T, root string) map[string][]string {
	t.Helper()
	read := map[string][]string{}
	base := filepath.Join(findModuleDir(t), filepath.FromSlash(root))
	err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range workspaceEnvRead.FindAllStringSubmatch(string(data), -1) {
			relative, relErr := filepath.Rel(findModuleDir(t), path)
			if relErr != nil {
				relative = path
			}
			read[match[1]] = append(read[match[1]], filepath.ToSlash(relative))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return read
}

func TestEveryWorkspaceGroupReadIsDeclared(t *testing.T) {
	for service, root := range servicesWithWorkspaceReads {
		declared := declaredWorkspaceGroups(t, service)
		read := groupsReadInTree(t, root)
		if len(read) == 0 {
			t.Fatalf("no workspaceEnv reads found under %s; the accessor was renamed and this gate now proves nothing", root)
		}
		names := make([]string, 0, len(read))
		for group := range read {
			names = append(names, group)
		}
		sort.Strings(names)
		for _, group := range names {
			if !declared[group] {
				sort.Strings(read[group])
				t.Errorf("%s reads workspace configuration group %q in %s but does not declare it in services/%s/service.codefly.yaml; "+
					"an undeclared group is never injected, so the read returns nothing in every deployed cell",
					service, group, strings.Join(read[group], ", "), service)
			}
		}
	}
}

// A declared group with no shipped default leaves a composing workspace to
// discover the group's existence from a runtime failure. Every group this
// module's services declare ships a default here, secret groups excepted:
// those carry a .secret.env whose values a composer must supply.
func TestEveryDeclaredWorkspaceGroupShipsADefault(t *testing.T) {
	moduleDir := findModuleDir(t)
	for service := range servicesWithWorkspaceReads {
		for group := range declaredWorkspaceGroups(t, service) {
			// CLI owns this native-run value and refuses authored defaults.
			// Unlike module configuration, its ROOT is the run's actual digest
			// cache directory (composition.ArtifactCacheDir), not a shipped path.
			if group == "artifact-cache" {
				for _, suffix := range []string{".env", ".secret.env"} {
					_, err := os.Stat(filepath.Join(moduleDir, "configurations", "local", group+suffix))
					if !os.IsNotExist(err) {
						t.Errorf("renderer-owned %s must have no authored default", group)
					}
				}
				continue
			}
			plain := filepath.Join(moduleDir, "configurations", "local", group+".env")
			secret := filepath.Join(moduleDir, "configurations", "local", group+".secret.env")
			if _, err := os.Stat(plain); err == nil {
				continue
			}
			if _, err := os.Stat(secret); err == nil {
				continue
			}
			t.Errorf("service %s declares workspace configuration group %q but configurations/local ships neither %s.env nor %s.secret.env; "+
				"a composing workspace then meets the group for the first time as a runtime failure",
				service, group, group, group)
		}
	}
}

// And every KEY it reads from a declared group must be declared in that group's
// shipped default.
//
// TestEveryWorkspaceGroupReadIsDeclared above covers the GROUP. The key is the
// same failure one level down, and the group-level gate passes straight over it:
// accounts has read `security`/TRUSTED_PROXY_CIDRS since per-IP rate limiting
// existed, `security` is declared, and `security.env` never declared the key —
// so the platform carried no value, every deployment attributed anonymous
// traffic to the proxy's own address, and an operator who set the key got no
// attribution and no error. The same held for ACCOUNTS_REVOCATION_FAIL_OPEN and
// RBAC_CENTRAL_ENFORCEMENT, both operator opt-ins that did nothing, and for
// `internal-auth`/JWT_PREVIOUS_PUBLIC_KEYS, which module/KEY_ROTATION.md
// instructs an operator to set during a signing-key rotation and which was
// dropped — turning the one mechanism that makes a rotation survivable into the
// thing that stops every outstanding credential verifying.
//
// A key a composition cannot supply is INERT, and inert reads as wired. That is
// the whole bug, and the names are mechanical, so this catches them.

// workspaceEnvKeyRead matches a read whose group AND key are both literals,
// e.g. workspaceEnv("security", "TRUSTED_PROXY_CIDRS"). A read whose key is
// computed (workspaceEnv("datasource-keys", key)) names nothing to check, and
// is the caller's to keep declared.
var workspaceEnvKeyRead = regexp.MustCompile(`workspaceEnv\(\s*"([a-z0-9-]+)"\s*,\s*"([A-Za-z0-9_]+)"\s*\)`)

// keysNoGroupCanDeclare are the (group, key) pairs this gate knowingly does not
// hold, each because declaring it today would change behaviour rather than
// enable it. THIS LIST SHRINKS AND NEVER GROWS.
//
// All three are optional rotation slots in a group that ships only a
// `.secret.env`, and configuration_defaults_test.go's assertSecretDefaultIsPlaceholder
// refuses an empty assignment in such a file — a shipped secret default must be
// a working placeholder, since a base-synced consumer boots on it. So the only
// way to declare these is a NON-empty value, and a non-empty value in a
// rotation slot is read as a real retiring credential: accounts would refuse to
// boot on `gateway-trust` (a previous credential with no
// CODEFLY_GATEWAY_TOKEN_PREVIOUS_EXPIRES_AT is a refusal by design), and
// `internal-auth` would accept a shipped placeholder as a second valid internal
// credential on every consumer that inherited it.
//
// The fix belongs to that empty-value rule, which conflates "a required secret
// has no default" with "an optional secret key is declared and unset" — the
// distinction a group has no way to express today. Until it can, these three
// stay inert, and an operator rotating either credential has to be told so by
// the runbook rather than by the group.
var keysNoGroupCanDeclare = map[string]string{
	"gateway-trust/CODEFLY_GATEWAY_TOKEN_PREVIOUS":            "an optional rotation slot in a secret-only group; a non-empty placeholder would be read as a real retiring gateway credential",
	"gateway-trust/CODEFLY_GATEWAY_TOKEN_PREVIOUS_EXPIRES_AT": "bounds the slot above, and declaring it alone leaves the pair half-declared",
	"internal-auth/CODEFLY_INTERNAL_TOKEN_PREVIOUS":           "an optional rotation slot in a secret-only group; a non-empty placeholder would be a second valid internal credential on every consumer",
}

// declaredGroupKeys returns the keys a group's shipped defaults assign, across
// its value file and its secret file — either one declares the key for the
// group the platform injects.
func declaredGroupKeys(t *testing.T, moduleDir, group string) map[string]bool {
	t.Helper()
	declared := map[string]bool{}
	for _, suffix := range []string{".env", ".secret.env"} {
		data, err := os.ReadFile(filepath.Join(moduleDir, "configurations", "local", group+suffix))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			if key, _, found := strings.Cut(trimmed, "="); found {
				declared[strings.TrimSpace(key)] = true
			}
		}
	}
	return declared
}

func TestEveryWorkspaceGroupKeyReadIsDeclared(t *testing.T) {
	moduleDir := findModuleDir(t)
	for service, root := range servicesWithWorkspaceReads {
		base := filepath.Join(moduleDir, filepath.FromSlash(root))
		// (group, key) -> the files that read it.
		read := map[string]map[string][]string{}
		err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			relative, relErr := filepath.Rel(moduleDir, path)
			if relErr != nil {
				relative = path
			}
			for _, match := range workspaceEnvKeyRead.FindAllStringSubmatch(string(data), -1) {
				group, key := match[1], match[2]
				if read[group] == nil {
					read[group] = map[string][]string{}
				}
				read[group][key] = append(read[group][key], filepath.ToSlash(relative))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
		if len(read) == 0 {
			t.Fatalf("no literal workspaceEnv(group, key) reads found under %s; the accessor was renamed and this gate now proves nothing", root)
		}

		exempted := map[string]bool{}
		groups := make([]string, 0, len(read))
		for group := range read {
			groups = append(groups, group)
		}
		sort.Strings(groups)
		for _, group := range groups {
			declared := declaredGroupKeys(t, moduleDir, group)
			keys := make([]string, 0, len(read[group]))
			for key := range read[group] {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				if declared[key] {
					continue
				}
				pair := group + "/" + key
				if why, allowed := keysNoGroupCanDeclare[pair]; allowed {
					exempted[pair] = true
					t.Logf("%s reads %s, which stays inert: %s", service, pair, why)
					continue
				}
				sources := read[group][key]
				sort.Strings(sources)
				t.Errorf("%s reads %s in %s, and configurations/local/%s.env (nor %s.secret.env) declares %s; "+
					"a composition can supply a value for a DECLARED key and never declare one, so this read returns "+
					"nothing in every deployed cell while reading as wired — an operator who sets it gets no effect and no error",
					service, pair, strings.Join(sources, ", "), group, group, key)
			}
		}

		// The list shrinks and never grows: an entry for a pair that is no
		// longer read, or that is now declared, is a licence nothing needs.
		for pair, why := range keysNoGroupCanDeclare {
			if exempted[pair] {
				continue
			}
			group, key, _ := strings.Cut(pair, "/")
			if read[group] != nil && read[group][key] != nil {
				continue
			}
			t.Errorf("keysNoGroupCanDeclare still exempts %s (%q), which %s no longer reads undeclared; delete the line",
				pair, why, service)
		}
	}
}
