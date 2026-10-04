package main

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The host's secrets store holds the Ed25519 signing key every edge verifies
// against and the Transit key that seals every API key, connector credential,
// MFA secret and WebAuthn credential. On 2026-10-03 a node replacement
// rescheduled a deployed Vault that had been rendered in `-dev` mode: it came
// back empty, accounts crash-looped on `vault http 404`, and the Transit key was
// gone for good because it is non-exportable. These assertions hold the two
// halves of the fix this repository owns — which Vault agent may serve a
// deployed render, and what accounts is told about the Vault it binds.

const (
	vaultServiceManifest   = "module/services/vault/service.codefly.yaml"
	accountsServiceMainfst = "module/services/accounts/service.codefly.yaml"
	vaultGroupDefaults     = "module/configurations/local/vault.env"
	accountsGoGRPCPin      = "module/services/accounts/service.codefly.yaml"
)

// vaultDurableRenderFloor is the first vault agent release whose deployed render
// is durable: `vault server` with integrated raft storage on a retained
// PersistentVolumeClaim, auto-unsealed by the seal the environment supplies,
// with `vault server -dev` reachable only from the ephemeral local-apply
// profile. Read from the v0.0.37 tag's
// templates/deployment/kustomize/base/stateful-set.yaml.tmpl (which branches on
// .Deployment.Parameters.Durable) and provision/server.sh (which exits 78
// naming VAULT_SEAL_TYPE when no auto-unseal seal is configured, and refuses
// shamir) on 2026-10-03.
//
// Below this floor the agent renders `-dev` unconditionally for every profile,
// so a downgrade of this pin silently puts a product's secrets back in memory.
// That is the whole failure, and nothing upstream of this repository stops a pin
// from moving down, so the floor is asserted here.
//
// What this does NOT assert, and nothing in this repository can: that the image
// refuses a dev command in a deployed runtime context. It does not. Running the
// pinned image with `server -dev` and deployed-context variables comes up on
// `storage_type: inmem`; the exit-78 refusal guards only the durable launch
// path. So the guarantee here is exactly "the restricted renderer chooses
// raft", and a dev command reaching a deployed runtime by another route would
// still start. That guard belongs to the Vault launch owner. Pinning a floor is
// the whole of what a consumer of that agent can enforce — which is the standing
// reason this check survives whatever the agent later gains.
var vaultDurableRenderFloor = [3]int{0, 0, 37}

type agentPinnedManifest struct {
	Agent struct {
		Name    string `yaml:"name"`
		Version string `yaml:"version"`
	} `yaml:"agent"`
	WorkspaceConfigurationDependencies []string `yaml:"workspace-configuration-dependencies"`
	Spec                               struct {
		ServiceAccount *struct {
			Name string `yaml:"name"`
		} `yaml:"service-account"`
		ConfigMounts []map[string]any `yaml:"config-mounts"`
	} `yaml:"spec"`
}

func readAgentPinnedManifest(t *testing.T, path string) agentPinnedManifest {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest agentPinnedManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func parseAgentVersion(t *testing.T, version string) [3]int {
	t.Helper()
	// A prerelease ("0.1.48-dev.abc1234") compares on its release triple: a
	// prerelease of the floor release carries the floor's templates.
	release, _, _ := strings.Cut(version, "-")
	parts := strings.Split(release, ".")
	if len(parts) != 3 {
		t.Fatalf("agent version %q is not a three-part version", version)
	}
	var parsed [3]int
	for index, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil {
			t.Fatalf("agent version %q component %q is not a number", version, part)
		}
		parsed[index] = value
	}
	return parsed
}

// A deployed render of this module's own Vault must be the durable one. The
// in-memory `vault server -dev` shape keeps the signing key and the Transit key
// in RAM on a single auto-unsealed node, so any reschedule is total,
// unrecoverable loss of everything Transit sealed.
func TestVaultAgentPinCannotRenderAnInMemoryVaultOnACell(t *testing.T) {
	t.Parallel()
	manifest := readAgentPinnedManifest(t, vaultServiceManifest)
	if manifest.Agent.Name != "vault" {
		t.Fatalf("%s names agent %q, not vault", vaultServiceManifest, manifest.Agent.Name)
	}
	pinned := parseAgentVersion(t, manifest.Agent.Version)
	if pinned[0] < vaultDurableRenderFloor[0] ||
		(pinned[0] == vaultDurableRenderFloor[0] && pinned[1] < vaultDurableRenderFloor[1]) ||
		(pinned[0] == vaultDurableRenderFloor[0] && pinned[1] == vaultDurableRenderFloor[1] && pinned[2] < vaultDurableRenderFloor[2]) {
		t.Fatalf(
			"%s pins vault agent %s, below %d.%d.%d: that agent renders `vault server -dev` for every deploy profile, "+
				"so a deployed product's signing key and Transit key live in memory and a reschedule loses them permanently",
			vaultServiceManifest, manifest.Agent.Version,
			vaultDurableRenderFloor[0], vaultDurableRenderFloor[1], vaultDurableRenderFloor[2])
	}
}

// accounts reads the Vault's address, CA, auth method and role from the `vault`
// configuration group, and logs in as its own ServiceAccount. Both declarations
// are what make that possible: without the group the keys never reach the
// process, and without a ServiceAccount of its own there is no identity for a
// Vault Kubernetes role to bind.
func TestAccountsDeclaresWhatTheVaultBindingNeeds(t *testing.T) {
	t.Parallel()
	manifest := readAgentPinnedManifest(t, accountsServiceMainfst)
	found := false
	for _, group := range manifest.WorkspaceConfigurationDependencies {
		if group == "vault" {
			found = true
		}
	}
	if !found {
		t.Errorf("%s does not depend on the `vault` configuration group, so VAULT_ADDR, VAULT_CA_FILE, VAULT_AUTH_METHOD and VAULT_K8S_ROLE never reach the process", accountsServiceMainfst)
	}
	if manifest.Spec.ServiceAccount == nil || manifest.Spec.ServiceAccount.Name == "" {
		t.Errorf("%s declares no spec.service-account: Vault Kubernetes auth binds a role to accounts' own ServiceAccount, and the namespace default is shared with every other workload", accountsServiceMainfst)
	}
}

// Both in-pod paths are a contract between two files nothing links: the client
// reads them, the group documents them, and the service manifest's mounts are
// what create them. A render that put either somewhere else would produce a
// refusal naming a path the group never mentioned, so the spellings are
// compared here.
//
// Observed 2026-10-03: nothing upstream of this repository relates a
// configuration group's documentation to the constant a service reads. The check
// stays regardless of that — a host must not depend on a check it does not run.
func TestProjectedTokenPathAgreesBetweenTheClientAndTheGroup(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/var/run/secrets/vault/token", "/etc/vault/ca/ca.crt"} {
		for _, file := range []string{
			"module/services/accounts/code/pkg/vaultconnection/kubernetes.go",
			vaultGroupDefaults,
		} {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), path) {
				t.Errorf("%s does not name the in-pod path %s", file, path)
			}
		}
	}
	// The pod's default token is audience-bound to the API server. Replaying it
	// against Vault is the shortcut this binding exists to avoid, so neither
	// file may name it.
	data, err := os.ReadFile(vaultGroupDefaults)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "/var/run/secrets/kubernetes.io/serviceaccount") {
		t.Errorf("%s points at the pod's default API-server token, which must not be replayable against Vault", vaultGroupDefaults)
	}
}

// Every key accounts reads from the `vault` group must be declared in the
// group's own defaults. A key a service reads but the group never declares is
// inert: an environment can supply a value for a declared key, never declare
// one, so the feature silently does nothing on every deployment.
func TestEveryVaultGroupKeyAccountsReadsIsDeclared(t *testing.T) {
	t.Parallel()
	defaults, err := os.ReadFile(vaultGroupDefaults)
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for line := range strings.SplitSeq(string(defaults), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if key, _, found := strings.Cut(line, "="); found {
			declared[strings.TrimSpace(key)] = true
		}
	}
	for _, key := range []string{
		"VAULT_ADDR",
		"VAULT_CA_FILE",
		"VAULT_AUTH_METHOD",
		"VAULT_K8S_ROLE",
		"VAULT_K8S_MOUNT",
		"VAULT_K8S_TOKEN_PATH",
		"VAULT_ALLOW_INSECURE_HTTP",
		"VAULT_KEY_CUSTODY",
	} {
		if !declared[key] {
			t.Errorf("%s does not declare %s, which accounts reads: a key the group never declares cannot be supplied by an environment, so it is inert everywhere", vaultGroupDefaults, key)
		}
	}
	// The group's repo-root entry is a symlink onto this file, so the two never
	// drift; a copy would let a consumer's base sync carry stale keys.
	info, err := os.Lstat("configurations/local/vault.env")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("configurations/local/vault.env is not a symlink onto %s", vaultGroupDefaults)
	}
}

// goGRPCMountFloor is the first go-grpc agent release that renders the mounts
// accounts declares. It is empty while codefly-dev/service-go-grpc#156 is
// unreleased: 0.1.50, the version pinned today, hardcodes
// `automountServiceAccountToken: false` and a single /tmp emptyDir, and its
// manifest spec carries no mount field at all.
//
// Setting it is one half of a two-part change; the other is moving the agent pin
// in accounts' manifest to that release. The conformance test below names both.
const goGRPCMountFloor = ""

// The two halves of VAULT_CA_FILE, and where the projected token lands. The
// client defaults to these paths and the manifest's mounts are what create them,
// so a render that moved either would hand the operator a refusal naming a path
// nothing else mentions.
const (
	vaultCAMountPath = "/etc/vault/ca"
	vaultCAKey       = "ca.crt"
	vaultCAConfigMap = "vault-ca"
	vaultTokenPath   = "/var/run/secrets/vault/token"
)

// TestHostedAccountsDeploymentCarriesTheVaultBinding is the conformance test for
// requirement D1's render half, and **it cannot pass until
// codefly-dev/service-go-grpc#156 releases**. That is deliberate, and PR #1008
// stays open on it.
//
// The test it replaced returned successfully when both mounts were absent,
// which made it worse than nothing: it read as coverage of the exact shape the
// October 2026 incident needed and asserted that shape only once somebody had
// already declared it. A hosted accounts with no CA and no projected token
// cannot authenticate to the Vault the composition names, so a green suite over
// that state is a false report.
//
// What it asserts is what this repository can own: the declaration the renderer
// is a pure function of, plus a pin at a release that renders it. **Still owed
// at the pin bump:** an assertion over the rendered Deployment bytes — the CA
// volume and its mode, the projected `serviceAccountToken` with
// `audience: vault`, the mount paths and the ServiceAccount. That cannot be
// written here yet because the renderer that would produce them does not exist;
// when the pin moves, add it beside this.
//
// The audience and the token path are matched by value rather than by field
// name, so whatever #156 calls the spec key, this test keeps its meaning.
func TestHostedAccountsDeploymentCarriesTheVaultBinding(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(accountsServiceMainfst)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Spec map[string]any `yaml:"spec"`
	}
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}

	owed := func(what string) {
		t.Errorf("%s does not declare %s.\n"+
			"This is requirement D1's render half and it waits on codefly-dev/service-go-grpc#156: the pinned go-grpc %s "+
			"hardcodes automountServiceAccountToken: false and one /tmp emptyDir, and its manifest spec has no mount field. "+
			"When #156 releases: declare it, set goGRPCMountFloor in this file, move the agent pin to that release, and add "+
			"the rendered-Deployment assertion this test still owes.",
			accountsServiceMainfst, what, readAgentPinnedManifest(t, accountsGoGRPCPin).Agent.Version)
	}

	// The CA the Vault's certificate chains to, at the path the client defaults
	// VAULT_CA_FILE to. Optional, so a local render with no such ConfigMap starts.
	if mount := caMount(document.Spec); mount == nil {
		owed("a config-mount for the " + vaultCAConfigMap + " ConfigMap")
	} else {
		if got, _ := mount["mount-path"].(string); got != vaultCAMountPath {
			t.Errorf("the %q mount-path is %q, but the client defaults VAULT_CA_FILE to %s/%s",
				vaultCAConfigMap, got, vaultCAMountPath, vaultCAKey)
		}
		if optional, _ := mount["optional"].(bool); !optional {
			t.Errorf("the %q mount is not optional: a local render has no such ConfigMap and the pod would never start", vaultCAConfigMap)
		}
	}

	// The projected ServiceAccount token. Matched on its audience, because that
	// is the whole point: the pod's default token is bound to the API server and
	// must not be replayable against Vault.
	if !declaresValue(document.Spec, "audience", "vault") {
		owed("a projected ServiceAccount token with audience `vault`")
	}
	if !strings.Contains(string(data), vaultTokenPath) {
		owed("the projected token at " + vaultTokenPath + ", which is where the client reads it")
	}

	if goGRPCMountFloor == "" {
		t.Errorf("goGRPCMountFloor is unset: no go-grpc release is known to render these mounts, so the declaration above is "+
			"staged and the hosted Deployment carries neither. Set it to the release that carries codefly-dev/service-go-grpc#156 "+
			"and move %s's agent pin to it in the same change.", accountsGoGRPCPin)
		return
	}
	manifest := readAgentPinnedManifest(t, accountsGoGRPCPin)
	pinned, floor := parseAgentVersion(t, manifest.Agent.Version), parseAgentVersion(t, goGRPCMountFloor)
	if pinned[0] < floor[0] ||
		(pinned[0] == floor[0] && pinned[1] < floor[1]) ||
		(pinned[0] == floor[0] && pinned[1] == floor[1] && pinned[2] < floor[2]) {
		t.Fatalf(
			"%s declares the Vault mounts against go-grpc %s, below the %s that renders them: the Deployment would carry "+
				"no CA and no projected token while this manifest claims both, and accounts would refuse to boot naming the wrong file",
			accountsGoGRPCPin, manifest.Agent.Version, goGRPCMountFloor)
	}
}

func caMount(spec map[string]any) map[string]any {
	mounts, _ := spec["config-mounts"].([]any)
	for _, entry := range mounts {
		mount, _ := entry.(map[string]any)
		if name, _ := mount["config-map"].(string); name == vaultCAConfigMap {
			return mount
		}
	}
	return nil
}

// declaresValue reports whether any mapping anywhere under the spec binds key to
// value. It walks rather than naming a path because #156's spec key spelling is
// not settled, and a test that hardcoded a guess would be asserting the guess.
func declaresValue(node any, key, value string) bool {
	switch typed := node.(type) {
	case map[string]any:
		if found, _ := typed[key].(string); found == value {
			return true
		}
		for _, child := range typed {
			if declaresValue(child, key, value) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if declaresValue(child, key, value) {
				return true
			}
		}
	}
	return false
}
