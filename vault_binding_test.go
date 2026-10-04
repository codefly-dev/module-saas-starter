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
// The declaration below it is therefore STAGED, not live, and PR #1008 names
// #156 as the one thing it waits on. Set this to the release that carries the
// mounts and move the agent pin to it in the same change; the assertions here
// turn into a real pin-versus-declaration gate the moment it is non-empty.
const goGRPCMountFloor = ""

// vaultCAMountPath and vaultCAKey are the two halves of VAULT_CA_FILE. The
// client defaults to their join, and the manifest's mount is what creates it, so
// a render that moved either would hand the operator a refusal naming a path
// nothing else mentions.
const (
	vaultCAMountPath = "/etc/vault/ca"
	vaultCAKey       = "ca.crt"
	vaultCAConfigMap = "vault-ca"
)

// The declared mount must compose to exactly the path the client defaults to.
// This is the half of the seam that is testable before #156 ships: the agent
// cannot render the volume yet, but what this repository asks for can already be
// held against what it reads.
func TestDeclaredVaultCAMountComposesThePathTheClientReads(t *testing.T) {
	t.Parallel()
	manifest := readAgentPinnedManifest(t, accountsServiceMainfst)
	if len(manifest.Spec.ConfigMounts) == 0 {
		t.Skipf("%s declares no config-mounts yet; this arms with the declaration owed on codefly-dev/service-go-grpc#156", accountsServiceMainfst)
	}
	var mount map[string]any
	for _, candidate := range manifest.Spec.ConfigMounts {
		if name, _ := candidate["config-map"].(string); name == vaultCAConfigMap {
			mount = candidate
		}
	}
	if mount == nil {
		t.Fatalf("%s declares config-mounts but none for the %q ConfigMap", accountsServiceMainfst, vaultCAConfigMap)
	}
	if got, _ := mount["mount-path"].(string); got != vaultCAMountPath {
		t.Errorf("the %q mount-path is %q, but the client defaults VAULT_CA_FILE to %s/%s", vaultCAConfigMap, got, vaultCAMountPath, vaultCAKey)
	}
	// Optional, or a local k3d render — where no cell publishes this ConfigMap —
	// would block the pod on a volume it will never get.
	if optional, _ := mount["optional"].(bool); !optional {
		t.Errorf("the %q mount is not optional: a local render has no such ConfigMap and the pod would never start", vaultCAConfigMap)
	}
}

// The tripwire: once a release renders the mounts, the pin must be at or above
// it. While the floor is unset the declaration is staged against #156 and this
// says so rather than failing, because a red branch for a dependency that has
// not shipped teaches nobody anything and hides the reds that matter.
func TestVaultMountsAreNotDeclaredAgainstAnAgentThatDropsThem(t *testing.T) {
	t.Parallel()
	manifest := readAgentPinnedManifest(t, accountsGoGRPCPin)
	if len(manifest.Spec.ConfigMounts) == 0 {
		return
	}
	if goGRPCMountFloor == "" {
		t.Logf(
			"%s declares spec.config-mounts against go-grpc %s, which drops them: STAGED on codefly-dev/service-go-grpc#156. "+
				"When it releases, set goGRPCMountFloor in this file and move the agent pin to it in the same change.",
			accountsGoGRPCPin, manifest.Agent.Version)
		return
	}
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
