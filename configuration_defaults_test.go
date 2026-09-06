package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Services declare workspace-configuration-dependencies (legal, identity,
// internal-auth, …). Codefly's composition provisions a composed module's own
// workspace configurations into the consuming workspace: a solution that
// references this module by path (`codefly add module --source <repo>/module`)
// resolves the module's workspace root — this repository root — and reads its
// configurations/local/* to satisfy those dependencies, so the solution boots
// the host without hand-authoring the groups.
//
// That only holds if this repository actually ships a local default for every
// declared group. This test is the guard: add a workspace-configuration
// dependency to a service and you must ship its default here, or a composing
// solution breaks at runtime-init with "no configuration found for <group>".

const workspaceConfigLocalDir = "configurations/local"

func declaredWorkspaceConfigGroups(t *testing.T) []string {
	t.Helper()
	manifests, err := filepath.Glob("module/services/*/service.codefly.yaml")
	if err != nil {
		t.Fatalf("glob service manifests: %v", err)
	}
	if len(manifests) == 0 {
		t.Fatal("no service manifests found")
	}
	seen := map[string]struct{}{}
	for _, manifestPath := range manifests {
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatalf("read %s: %v", manifestPath, err)
		}
		var manifest struct {
			WorkspaceConfigurationDependencies []string `yaml:"workspace-configuration-dependencies"`
		}
		if err := yaml.Unmarshal(data, &manifest); err != nil {
			t.Fatalf("parse %s: %v", manifestPath, err)
		}
		for _, group := range manifest.WorkspaceConfigurationDependencies {
			seen[group] = struct{}{}
		}
	}
	groups := make([]string, 0, len(seen))
	for group := range seen {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	return groups
}

// declaresAtLeastOneVariable reports whether an env file assigns at least one
// variable (a `KEY=` line). A file of only comments would register a named
// group with no bindings, which does not satisfy a dependent service.
func declaresAtLeastOneVariable(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if key, _, ok := strings.Cut(trimmed, "="); ok && strings.TrimSpace(key) != "" {
			return true
		}
	}
	return false
}

func TestEveryDeclaredGroupShipsALocalDefault(t *testing.T) {
	for _, group := range declaredWorkspaceConfigGroups(t) {
		shipped := false
		// A group's values may live in <group>.env (non-secret) and/or
		// <group>.secret.env (dev secret); either satisfies the dependency.
		for _, name := range []string{group + ".env", group + ".secret.env"} {
			data, err := os.ReadFile(filepath.Join(workspaceConfigLocalDir, name))
			if err == nil && declaresAtLeastOneVariable(data) {
				shipped = true
			}
		}
		if !shipped {
			t.Errorf("declared config group %q has no local default: add a %s/%s.env (or .secret.env) that assigns at least one variable, so a composing solution provisions it via composition instead of hand-authoring it",
				group, workspaceConfigLocalDir, group)
		}
	}
}
