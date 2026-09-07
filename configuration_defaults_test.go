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

// moduleDefaultFiles returns the default file names (e.g. "legal.env",
// "internal-auth.secret.env") that group actually ships under
// moduleConfigLocalDir. A group may ship a non-secret file, a secret file, or
// both; each present file must be mirrored by a repo-root symlink, and each
// secret file must hold only placeholder values.
func moduleDefaultFiles(group string) []string {
	var names []string
	for _, name := range []string{group + ".env", group + ".secret.env"} {
		data, err := os.ReadFile(filepath.Join(moduleConfigLocalDir, name))
		if err == nil && declaresAtLeastOneVariable(data) {
			names = append(names, name)
		}
	}
	return names
}

// repoRootMirrorsModule verifies the repo-root entry for name is a symlink that
// resolves to the module default of the same name. shipsDefault(workspace, …)
// alone is too weak: os.ReadFile follows the link, so it also passes a symlink
// aimed at the wrong group's file (mistargeted) or an independent real-file copy
// (which silently drifts from the module source of truth). module/ is the single
// source; the repo-root entry must be a pointer to it, not a divergent twin.
func repoRootMirrorsModule(t *testing.T, name string) {
	t.Helper()
	rootPath := filepath.Join(workspaceConfigLocalDir, name)
	info, err := os.Lstat(rootPath)
	if err != nil {
		t.Errorf("repo-root default %s is missing: it must be a symlink onto %s/%s so the in-repo workspace and path composition read the single module source",
			rootPath, moduleConfigLocalDir, name)
		return
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("repo-root default %s is a real file, not a symlink onto %s/%s: an independent copy drifts from the module source of truth",
			rootPath, moduleConfigLocalDir, name)
		return
	}
	rootResolved, err := filepath.EvalSymlinks(rootPath)
	if err != nil {
		t.Errorf("repo-root default %s does not resolve (dangling symlink): point it at %s/%s",
			rootPath, moduleConfigLocalDir, name)
		return
	}
	moduleResolved, err := filepath.EvalSymlinks(filepath.Join(moduleConfigLocalDir, name))
	if err != nil {
		t.Fatalf("resolve module default %s/%s: %v", moduleConfigLocalDir, name, err)
	}
	if rootResolved != moduleResolved {
		t.Errorf("repo-root default %s resolves to %s, not the matching module default %s/%s: a mistargeted symlink serves the wrong group's values",
			rootPath, rootResolved, moduleConfigLocalDir, name)
	}
}

// placeholderMarkers flag a value as obviously non-production. Every one of the
// secret defaults' current values carries at least one; requiring it is what
// keeps a real credential from ever being committed as a "default".
var placeholderMarkers = []string{
	"replace-me", "replaceme", "change-me", "changeme",
	"placeholder", "local-dev", "dev-only", "example", "dummy",
}

func looksLikePlaceholder(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range placeholderMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// assertSecretDefaultIsPlaceholder fails if a shipped secret default assigns any
// value that is not an obvious placeholder. The immutable module package
// archives the whole module/ tree (git archive commit:module), so a
// module/configurations/local/*.secret.env file IS published to every consumer.
// The base-integrity manifest deliberately excludes it from drift detection
// (secrets are runtime-owned), which means nothing else stops a real credential
// pasted into this "default" from riding along into the published package. This
// is that missing guard.
func assertSecretDefaultIsPlaceholder(t *testing.T, name string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(moduleConfigLocalDir, name))
	if err != nil {
		t.Fatalf("read secret default %s/%s: %v", moduleConfigLocalDir, name, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if value = strings.TrimSpace(value); value == "" {
			continue
		}
		if !looksLikePlaceholder(value) {
			t.Errorf("secret default %s/%s assigns %s a value that is not an obvious placeholder: committed local secret defaults ship in the immutable module package, so they must be non-production placeholders (containing one of %v), never a real credential",
				moduleConfigLocalDir, name, key, placeholderMarkers)
		}
	}
}

func TestEveryDeclaredGroupShipsALocalDefault(t *testing.T) {
	for _, group := range declaredWorkspaceConfigGroups(t) {
		// The base-synced location is what a lodestar-composed consumer inherits.
		if !shipsDefault(moduleConfigLocalDir, group) {
			t.Errorf("declared config group %q has no default under the base-synced module subtree: add a %s/%s.env (or .secret.env) that assigns at least one variable, so a lodestar-composed solution inherits it instead of dying at runtime-init with \"no configuration found for %s\"",
				group, moduleConfigLocalDir, group, group)
		}
		// Each module default this group ships must be mirrored by a repo-root
		// symlink (so in-repo dev and path composition read the same source), and
		// each secret default must hold only placeholder values (it is published
		// verbatim in the immutable module package).
		for _, name := range moduleDefaultFiles(group) {
			repoRootMirrorsModule(t, name)
			if strings.HasSuffix(name, ".secret.env") {
				assertSecretDefaultIsPlaceholder(t, name)
			}
		}
	}
}
