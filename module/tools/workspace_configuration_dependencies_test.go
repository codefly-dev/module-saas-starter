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

// This is also the shape that cost the next one: the frontend read
// `application`/APP_BASE_URL — the public origin it presents to accounts — and
// never declared `application`, so on a cell it presented its own
// cluster-internal address instead and every sign-in was refused. The gate then
// covered accounts alone, so nothing saw it. It now discovers every service's
// source tree, in each language a service here is written in, rather than
// listing the services someone remembered to add.

// readPatterns are the two spellings of a read in one source language: a read
// whose GROUP is a literal, and the narrower read whose group AND key are both
// literals.
type readPatterns struct {
	group *regexp.Regexp
	key   *regexp.Regexp
}

// goReads matches the Go accessors: the services' workspaceEnv helper and the
// SDK's WorkspaceValue / WorkspaceSecret / WorkspaceConfiguration, e.g.
// workspaceEnv("vault", "VAULT_ALLOW_INSECURE_HTTP") or
// codefly.For(ctx).WorkspaceValue("security", "ACCOUNTS_DATABASE_TRANSPORT").
var goReadAccessor = `(?:workspaceEnv|\.Workspace(?:Value|Secret|Configuration))\(\s*"([a-z0-9-]+)"`

var goReads = readPatterns{
	group: regexp.MustCompile(goReadAccessor),
	key:   regexp.MustCompile(goReadAccessor + `\s*,\s*"([A-Za-z0-9_]+)"\s*\)`),
}

// tsReads matches the TypeScript accessors: sdk-js's getWorkspaceConfiguration /
// getWorkspaceSecret / getWorkspaceValue, the injectable runtime reader's
// workspaceConfiguration / workspaceSecret, and the `read(group, key)`
// WorkspaceReader the public runtime configuration is resolved through.
var tsReadAccessor = `(?:getWorkspace(?:Configuration|Secret|Value)|workspace(?:Configuration|Secret|Value)|(?:^|[^.\w$])read)\(\s*"([a-z0-9-]+)"`

var tsReads = readPatterns{
	group: regexp.MustCompile(tsReadAccessor),
	key:   regexp.MustCompile(tsReadAccessor + `\s*,\s*"([A-Za-z0-9_]+)"\s*\)`),
}

// sourcePatterns picks the patterns for a production source file, or nil for a
// file that is not one (tests, stories, other languages).
func sourcePatterns(path string) *readPatterns {
	slashed := filepath.ToSlash(path)
	switch {
	case strings.HasSuffix(path, ".go"):
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		return &goReads
	case strings.HasSuffix(path, ".ts"), strings.HasSuffix(path, ".tsx"),
		strings.HasSuffix(path, ".mts"), strings.HasSuffix(path, ".mjs"):
		for _, marker := range []string{".test.", ".spec.", ".stories.", "/__tests__/", "/test/", "/tests/", "/e2e/"} {
			if strings.Contains(slashed, marker) {
				return nil
			}
		}
		if strings.HasSuffix(path, ".d.ts") {
			return nil
		}
		return &tsReads
	}
	return nil
}

// skippedSourceDirs are trees no service authors: installed dependencies and
// build output.
var skippedSourceDirs = map[string]bool{
	"node_modules": true, ".next": true, "dist": true, "build": true, "coverage": true, "out": true,
}

// servicesKnownToReadGroups are services whose literal reads this gate must find.
// If one of them yields none, an accessor was renamed and the gate is proving
// nothing for it. A service that reads its group through a named constant
// (policy-log, telemetry) is outside what a literal match can see, and declares
// its one group beside that constant.
var servicesKnownToReadGroups = []string{"accounts", "auth-gateway", "frontend"}

// workspaceReads is one service's literal reads: group -> key -> the files that
// read it. A read whose key is computed is recorded under the empty key.
type workspaceReads map[string]map[string][]string

func (reads workspaceReads) add(group, key, file string) {
	if reads[group] == nil {
		reads[group] = map[string][]string{}
	}
	reads[group][key] = append(reads[group][key], file)
}

func (reads workspaceReads) filesReading(group string) []string {
	seen := map[string]bool{}
	var files []string
	for _, sources := range reads[group] {
		for _, file := range sources {
			if !seen[file] {
				seen[file] = true
				files = append(files, file)
			}
		}
	}
	sort.Strings(files)
	return files
}

// servicesWithManifests (credential_group_scope_test.go) lists every service.

// workspaceReadsOf walks a service's whole directory and returns its literal
// group reads.
func workspaceReadsOf(t *testing.T, service string) workspaceReads {
	t.Helper()
	moduleDir := findModuleDir(t)
	reads := workspaceReads{}
	base := filepath.Join(moduleDir, "services", service)
	err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if skippedSourceDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		patterns := sourcePatterns(path)
		if patterns == nil {
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
		relative = filepath.ToSlash(relative)
		for _, match := range patterns.group.FindAllStringSubmatch(string(data), -1) {
			reads.add(match[1], "", relative)
		}
		for _, match := range patterns.key.FindAllStringSubmatch(string(data), -1) {
			reads.add(match[1], match[2], relative)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk services/%s: %v", service, err)
	}
	return reads
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

func TestEveryWorkspaceGroupReadIsDeclared(t *testing.T) {
	for _, service := range servicesKnownToReadGroups {
		if len(workspaceReadsOf(t, service)) == 0 {
			t.Fatalf("no literal workspace configuration reads found under services/%s; an accessor was renamed and this gate now proves nothing for it", service)
		}
	}
	for _, service := range servicesWithManifests(t) {
		declared := declaredWorkspaceGroups(t, service)
		read := workspaceReadsOf(t, service)
		names := make([]string, 0, len(read))
		for group := range read {
			names = append(names, group)
		}
		sort.Strings(names)
		for _, group := range names {
			if !declared[group] {
				t.Errorf("%s reads workspace configuration group %q in %s but does not declare it in services/%s/service.codefly.yaml; "+
					"an undeclared group is never injected, so the read returns nothing in every deployed cell",
					service, group, strings.Join(read.filesReading(group), ", "), service)
			}
		}
	}
}

// cliOwnedGroups are the workspace configuration groups Codefly itself writes
// for each run or environment, so a module ships no default for them — the CLI
// refuses an authored one — and their keys are the CLI's to name.
var cliOwnedGroups = map[string]string{
	// Its ROOT is the run's actual digest cache directory
	// (composition.ArtifactCacheDir), not a shipped path.
	"artifact-cache": "the native run's artifact cache",
	// HOSTS is generated from the environment's egress declaration.
	"egress": "the environment's declared egress hosts",
}

// A declared group with no shipped default leaves a composing workspace to
// discover the group's existence from a runtime failure. Every group this
// module's services declare ships a default here, secret groups excepted:
// those carry a .secret.env whose values a composer must supply.
func TestEveryDeclaredWorkspaceGroupShipsADefault(t *testing.T) {
	moduleDir := findModuleDir(t)
	for _, service := range servicesWithManifests(t) {
		for group := range declaredWorkspaceGroups(t, service) {
			// The CLI owns these values and refuses authored defaults.
			if owner, cliOwned := cliOwnedGroups[group]; cliOwned {
				for _, suffix := range []string{".env", ".secret.env", ".yaml"} {
					_, err := os.Stat(filepath.Join(moduleDir, "configurations", "local", group+suffix))
					if !os.IsNotExist(err) {
						t.Errorf("CLI-owned %s (%s) must have no authored default", group, owner)
					}
				}
				continue
			}
			plain := filepath.Join(moduleDir, "configurations", "local", group+".env")
			secret := filepath.Join(moduleDir, "configurations", "local", group+".secret.env")
			// A document group (read with WorkspaceConfigurationDocument) ships
			// its default as a YAML document.
			document := filepath.Join(moduleDir, "configurations", "local", group+".yaml")
			if _, err := os.Stat(plain); err == nil {
				continue
			}
			if _, err := os.Stat(document); err == nil {
				continue
			}
			if _, err := os.Stat(secret); err == nil {
				continue
			}
			t.Errorf("service %s declares workspace configuration group %q but configurations/local ships none of %s.env, %s.secret.env or %s.yaml; "+
				"a composing workspace then meets the group for the first time as a runtime failure",
				service, group, group, group, group)
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

// The key patterns above match a read whose group AND key are both literals,
// e.g. workspaceEnv("security", "TRUSTED_PROXY_CIDRS"). A read whose key is
// computed (workspaceEnv("datasource-keys", key)) names nothing to check, and is
// the caller's to keep declared.

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
	exempted := map[string]bool{}
	literalKeyReads := 0
	for _, service := range servicesWithManifests(t) {
		read := workspaceReadsOf(t, service)
		groups := make([]string, 0, len(read))
		for group := range read {
			groups = append(groups, group)
		}
		sort.Strings(groups)
		for _, group := range groups {
			// A CLI-owned group ships no default to declare its keys in.
			if _, cliOwned := cliOwnedGroups[group]; cliOwned {
				continue
			}
			declared := declaredGroupKeys(t, moduleDir, group)
			keys := make([]string, 0, len(read[group]))
			for key := range read[group] {
				if key != "" {
					keys = append(keys, key)
				}
			}
			sort.Strings(keys)
			for _, key := range keys {
				literalKeyReads++
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
	}
	if literalKeyReads == 0 {
		t.Fatalf("no literal workspace configuration (group, key) reads found in any service; an accessor was renamed and this gate now proves nothing")
	}

	// The list shrinks and never grows: an entry for a pair that no service
	// still reads undeclared is a licence nothing needs.
	for pair, why := range keysNoGroupCanDeclare {
		if !exempted[pair] {
			t.Errorf("keysNoGroupCanDeclare still exempts %s (%q), which no service reads undeclared any more; delete the line",
				pair, why)
		}
	}
}
