package main

import (
	"fmt"
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
// tree that base-syncs into a consumer's modules/saas. A default parked at
// the repo root reaches a path-composed consumer (composition walks up to this
// repo's workspace root) but never a base-synced one, so it must live at
// module/configurations/local/*.env. The repo-root configurations/local/ entries
// are symlinks onto those files, so the in-repo dev workspace and path
// composition keep reading the same values.
//
// This test is the guard: add a workspace-configuration dependency to a service
// and you must ship its default under module/, or a base-synced solution
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

// envValue extracts the assigned value from an env line's right-hand side,
// stripping surrounding quotes and any inline comment. Marker matching must
// judge the token alone: a real credential trailed by a placeholder note
// (sk_live_realtoken # replace-me, or the no-space sk_live_realtoken#replace-me)
// would otherwise borrow the note's marker and pass. An unquoted `#` begins the
// comment regardless of the preceding character — a guard must over-strip so a
// marker can never ride in behind a real token; a literal `#` belongs in a
// value only when quoted ("a#b"). A value that is only a comment is empty.
func envValue(raw string) string {
	v := strings.TrimLeft(raw, " \t")
	if v == "" {
		return ""
	}
	if q := v[0]; q == '"' || q == '\'' {
		if end := strings.IndexByte(v[1:], q); end >= 0 {
			return v[1 : 1+end]
		}
		// Unterminated quote: fall through and treat the remainder literally.
	}
	if i := strings.IndexByte(v, '#'); i >= 0 {
		return strings.TrimSpace(v[:i])
	}
	return strings.TrimSpace(v)
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
// keeps a real credential from ever being committed as a "default". Each marker
// must be a distinctive placeholder phrase: common English words like "example"
// or "dummy" are excluded because a production-shaped value
// (example-corp-prod-live-key) would borrow them as a loose substring and pass.
var placeholderMarkers = []string{
	"replace-me", "replaceme", "change-me", "changeme",
	"placeholder", "local-dev", "dev-only",
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

// secretDefaultProblems returns one human-readable problem per assignment in a
// secret default that is unfit to ship in the immutable module package: an
// empty value (a required secret default that boots nothing, so a
// base-synced consumer fails closed at runtime-init) or a value that is
// not an obvious placeholder (a real credential riding along). Each value is
// read through envValue, so a real token cannot borrow a placeholder marker
// from a trailing "# replace-me later" comment, and a comment-only right-hand
// side is treated as the empty value it is.
func secretDefaultProblems(data []byte) []string {
	var problems []string
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, rawValue, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		switch value := envValue(rawValue); {
		case value == "":
			problems = append(problems, fmt.Sprintf(
				"assigns %s an empty value: a shipped local secret default must provide a working placeholder so a base-synced consumer boots, not an empty value that fails closed at runtime-init",
				key))
		case !looksLikePlaceholder(value):
			problems = append(problems, fmt.Sprintf(
				"assigns %s a value that is not an obvious placeholder (must contain one of %v): committed local secret defaults ship verbatim in the immutable module package, so they must be non-production placeholders, never a real credential",
				key, placeholderMarkers))
		}
	}
	return problems
}

// assertSecretDefaultIsPlaceholder fails if a shipped secret default assigns any
// value that is empty or not an obvious placeholder. The immutable module
// package archives the whole module/ tree (git archive commit:module), so a
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
	for _, problem := range secretDefaultProblems(data) {
		t.Errorf("secret default %s/%s %s", moduleConfigLocalDir, name, problem)
	}
}

func TestEveryDeclaredGroupShipsALocalDefault(t *testing.T) {
	for _, group := range declaredWorkspaceConfigGroups(t) {
		// The base-synced location is what a base-synced consumer inherits.
		moduleDefaults := moduleDefaultFiles(group)
		if len(moduleDefaults) == 0 {
			t.Errorf("declared config group %q has no default under the base-synced module subtree: add a %s/%s.env (or .secret.env) that assigns at least one variable, so a base-synced solution inherits it instead of dying at runtime-init with \"no configuration found for %s\"",
				group, moduleConfigLocalDir, group, group)
		}
		// Each module default this group ships must be mirrored by a repo-root
		// symlink (so in-repo dev and path composition read the same source), and
		// each secret default must hold only placeholder values (it is published
		// verbatim in the immutable module package).
		for _, name := range moduleDefaults {
			repoRootMirrorsModule(t, name)
			if strings.HasSuffix(name, ".secret.env") {
				assertSecretDefaultIsPlaceholder(t, name)
			}
		}
	}
}

// TestEnvValueStripsInlineCommentsAndQuotes is the regression guard for the
// parsing hole behind the secret check: before envValue, the marker match ran
// over the whole right-hand side, so a comment could smuggle a placeholder
// marker onto a real token.
func TestEnvValueStripsInlineCommentsAndQuotes(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"sk_live_9f3aX7realtoken   # TODO replace-me before prod", "sk_live_9f3aX7realtoken"},
		{"sk_live_9f3aX7realtoken#replace-me", "sk_live_9f3aX7realtoken"},
		{"local-dev-only-replace-me", "local-dev-only-replace-me"},
		{`"local-dev"  # quoted with trailing note`, "local-dev"},
		{`"a#b"`, "a#b"},
		{"'change-me'", "change-me"},
		{"", ""},
		{"   ", ""},
		{"   # comment only, no value", ""},
	}
	for _, tc := range cases {
		if got := envValue(tc.raw); got != tc.want {
			t.Errorf("envValue(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// TestSecretDefaultProblems is the regression guard for the secret check itself:
// each malicious or broken secret default below must be reported, and the
// shipped placeholder defaults must pass clean. Every case here previously
// slipped through (inline-comment marker, common-word substring, empty value).
func TestSecretDefaultProblems(t *testing.T) {
	bad := []struct{ name, content, wantContains string }{
		{
			"inline comment hides a real token (Finding 1)",
			"CODEFLY_INTERNAL_TOKEN=sk_live_9f3aX7realtoken   # TODO replace-me before prod",
			"not an obvious placeholder",
		},
		{
			"no-space comment hides a real token (Finding 1, marker borrowed with no separating space)",
			"CODEFLY_INTERNAL_TOKEN=sk_live_9f3aX7realtoken#replace-me",
			"not an obvious placeholder",
		},
		{
			"production-shaped value borrows a common word (Finding 2)",
			"CODEFLY_INTERNAL_TOKEN=example-corp-prod-live-key-8f3a2b",
			"not an obvious placeholder",
		},
		{
			"empty required secret default (Finding 3)",
			"CODEFLY_INTERNAL_TOKEN=",
			"an empty value",
		},
		{
			"comment-only right-hand side is an empty value (Findings 1+3)",
			"CODEFLY_INTERNAL_TOKEN=   # fill me in later",
			"an empty value",
		},
	}
	for _, tc := range bad {
		got := secretDefaultProblems([]byte(tc.content))
		if len(got) == 0 {
			t.Errorf("%s: secretDefaultProblems(%q) reported no problem; it must be caught before it ships", tc.name, tc.content)
			continue
		}
		if !strings.Contains(got[0], tc.wantContains) {
			t.Errorf("%s: problem = %q, want it to mention %q", tc.name, got[0], tc.wantContains)
		}
	}

	good := "# leading comment\n" +
		"CODEFLY_INTERNAL_TOKEN=local-dev-only-replace-me\n" +
		"CODEFLY_GATEWAY_TOKEN=local-dev-gateway-only-replace-me\n"
	if got := secretDefaultProblems([]byte(good)); len(got) != 0 {
		t.Errorf("secretDefaultProblems(shipped placeholders) = %v, want none", got)
	}
}
