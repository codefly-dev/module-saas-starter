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
