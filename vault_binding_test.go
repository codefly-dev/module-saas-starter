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
// gone for good because it is non-exportable.
//
// These assertions hold what this repository owns about the binding that
// replaced it: which Vault agent may serve a deployed render, and what accounts
// is told about the Vault it reaches. Under the owner's design that binding is
// values plus one secret and nothing else — no TLS of its own, no CA, no
// mounted file anywhere — so what there is to assert here is the configuration
// surface, and the transport rule is held in accounts' own suites.

const (
	vaultServiceManifest    = "module/services/vault/service.codefly.yaml"
	accountsServiceManifest = "module/services/accounts/service.codefly.yaml"
	vaultGroupDefaults      = "module/configurations/local/vault.env"
	vaultSecretDefaults     = "module/configurations/local/vault.secret.env"
	meshGroupDefaults       = "module/configurations/local/internal-transport.env"
)

// vaultDurableRenderFloor is the first vault agent release whose deployed render
// is durable: `vault server` with integrated raft storage on a retained
// PersistentVolumeClaim, auto-unsealed by the seal the environment supplies,
// with `vault server -dev` reachable only from the ephemeral local-apply
// profile. Read from the v0.0.37 tag's
// templates/deployment/kustomize/base/stateful-set.yaml.tmpl (which branches on
// .Deployment.Parameters.Durable) and provision/server.sh on 2026-10-03.
//
// Below this floor the agent renders `-dev` unconditionally for every profile,
// so a downgrade of this pin silently puts a product's secrets back in memory.
// That is the whole failure, and nothing upstream of this repository stops a pin
// from moving down, so the floor is asserted here.
//
// What this does NOT assert, and nothing in this repository can: that the image
// refuses a dev command in a deployed runtime context. It does not. Running the
// pinned image with `server -dev` and deployed-context variables comes up on
// `storage_type: inmem`; the agent's exit-78 refusal guards only the durable
// launch path and is a missing-seal refusal. So the guarantee here is exactly
// "the restricted renderer chooses raft", and a dev command reaching a deployed
// runtime by another route would still start. That guard belongs to the Vault
// launch owner. Pinning a floor is the whole of what a consumer of that agent
// can enforce — which is the standing reason this check survives whatever the
// agent later gains.
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

// accounts reads the Vault's address, auth method and AppRole mount from the
// `vault` group, the credential from the `vault` secret group, and the mesh
// assertion from `internal-transport`. All three declarations are what make the
// binding reachable: without a group, those keys never arrive at the process.
func TestAccountsDeclaresWhatTheVaultBindingNeeds(t *testing.T) {
	t.Parallel()
	manifest := readAgentPinnedManifest(t, accountsServiceManifest)
	declared := map[string]bool{}
	for _, group := range manifest.WorkspaceConfigurationDependencies {
		declared[group] = true
	}
	for group, why := range map[string]string{
		"vault":              "VAULT_ADDR, VAULT_AUTH_METHOD, VAULT_APPROLE_MOUNT and the AppRole credential",
		"internal-transport": "the mesh assertion that admits the cell Vault's plaintext in-cluster address",
	} {
		if !declared[group] {
			t.Errorf("%s does not depend on the `%s` configuration group, so %s never reach the process", accountsServiceManifest, group, why)
		}
	}
}

// Nothing in the binding may require a file. In-cluster transport security is
// the mesh's, so there is no certificate to anchor and no token to project, and
// Codefly delivers values and secrets rather than mounts — a render that mounted
// something would be reintroducing a dependency the design removed.
func TestTheVaultBindingMountsNothing(t *testing.T) {
	t.Parallel()
	manifest := readAgentPinnedManifest(t, accountsServiceManifest)
	if len(manifest.Spec.ConfigMounts) != 0 {
		t.Errorf("%s declares config-mounts %#v: the Vault binding is values and one secret, and nothing in it is a file",
			accountsServiceManifest, manifest.Spec.ConfigMounts)
	}
	for _, file := range []string{vaultGroupDefaults, vaultSecretDefaults} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, withdrawn := range []string{
			"VAULT_CA_FILE=",
			"VAULT_K8S_ROLE=",
			"VAULT_K8S_MOUNT=",
			"VAULT_K8S_TOKEN_PATH=",
			"VAULT_ALLOW_INSECURE_HTTP=",
		} {
			if strings.Contains(string(data), withdrawn) {
				t.Errorf("%s still declares %s: it belongs to the withdrawn file-and-TLS binding", file, withdrawn)
			}
		}
	}
}

// Every key accounts reads must be declared in the group that carries it. A key
// a service reads but no group declares is inert: a composition can supply a
// value for a declared key, never declare one, so the feature silently does
// nothing on every deployment while reading as wired.
func TestEveryVaultGroupKeyAccountsReadsIsDeclared(t *testing.T) {
	t.Parallel()
	assignments := func(file string) map[string]bool {
		t.Helper()
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		declared := map[string]bool{}
		for line := range strings.SplitSeq(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if key, _, found := strings.Cut(line, "="); found {
				declared[strings.TrimSpace(key)] = true
			}
		}
		return declared
	}
	for file, keys := range map[string][]string{
		vaultGroupDefaults:  {"VAULT_ADDR", "VAULT_AUTH_METHOD", "VAULT_APPROLE_MOUNT", "VAULT_KEY_CUSTODY"},
		vaultSecretDefaults: {"VAULT_APPROLE_ROLE_ID", "VAULT_APPROLE_SECRET_ID"},
		meshGroupDefaults:   {"mesh-protected"},
	} {
		declared := assignments(file)
		for _, key := range keys {
			if !declared[key] {
				t.Errorf("%s does not declare %s, which accounts reads: a key no group declares cannot be supplied by a composition, so it is inert everywhere", file, key)
			}
		}
	}

	// Each group's repo-root entry is a symlink onto the module's copy, so the
	// two never drift; a copy would let a consumer's base sync carry stale keys.
	for _, root := range []string{
		"configurations/local/vault.env",
		"configurations/local/vault.secret.env",
		"configurations/local/internal-transport.env",
	} {
		info, err := os.Lstat(root)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s is not a symlink onto the module's copy", root)
		}
	}
}

// The credential is a secret, so it lives in the SECRET group and nowhere else:
// a role id or secret id assigned in the non-secret group would ship in the
// immutable module package and be readable from a ConfigMap on every cell.
func TestTheAppRoleCredentialLivesOnlyInTheSecretGroup(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(vaultGroupDefaults)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"VAULT_APPROLE_ROLE_ID", "VAULT_APPROLE_SECRET_ID"} {
		if strings.Contains(string(data), key+"=") {
			t.Errorf("%s assigns %s: the credential belongs to %s, which the configuration plane delivers as a secret", vaultGroupDefaults, key, vaultSecretDefaults)
		}
	}
}
