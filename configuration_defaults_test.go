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
// internal-auth, …). A composing solution must receive a local default for each
// so it boots the host without hand-authoring the groups; a missing one fails at
// runtime-init with "no configuration found for <group>".
//
// The defaults must ship under the module/ subtree, because that is the only
// tree lodestar base-syncs into a consumer's modules/saas. A default parked at
// the repo root reaches a path-composed consumer (composition walks up to this
// repo's workspace root) but never a lodestar-composed one, so it must live at
// module/configurations/local/*.env. The repo-root configurations/local/ entries
// are symlinks onto those files, so the in-repo dev workspace and path
// composition keep reading the same values.
//
// This test is the guard: add a workspace-configuration dependency to a service
// and you must ship its default under module/, or a lodestar-composed solution
// breaks at runtime-init.

const (
	moduleConfigLocalDir    = "module/configurations/local"
	workspaceConfigLocalDir = "configurations/local"
)

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

// shipsDefault reports whether dir holds a default for group in either
// <group>.env (non-secret) or <group>.secret.env (dev secret), reading through
// symlinks. Values may live in either file; either satisfies the dependency.
func shipsDefault(dir, group string) bool {
	for _, name := range []string{group + ".env", group + ".secret.env"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err == nil && declaresAtLeastOneVariable(data) {
			return true
		}
	}
	return false
}

func TestEveryDeclaredGroupShipsALocalDefault(t *testing.T) {
	for _, group := range declaredWorkspaceConfigGroups(t) {
		// The base-synced location is what a lodestar-composed consumer inherits.
		if !shipsDefault(moduleConfigLocalDir, group) {
			t.Errorf("declared config group %q has no default under the base-synced module subtree: add a %s/%s.env (or .secret.env) that assigns at least one variable, so a lodestar-composed solution inherits it instead of dying at runtime-init with \"no configuration found for %s\"",
				group, moduleConfigLocalDir, group, group)
		}
		// The repo-root entry (a symlink onto the module default) is what the
		// in-repo dev workspace and path composition read; a dangling or missing
		// one silently regresses local boot.
		if !shipsDefault(workspaceConfigLocalDir, group) {
			t.Errorf("declared config group %q does not resolve under %s: keep the repo-root entry (a symlink onto %s/%s.env) so the in-repo workspace and path composition read the same default",
				group, workspaceConfigLocalDir, moduleConfigLocalDir, group)
		}
	}
}
