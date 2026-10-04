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

// The projected token's path is a contract between two files nothing links: the
// client reads it, the group documents it. A cell that mounts the token
// somewhere else gets a refusal naming a path the group never mentioned, so the
// two spellings are compared here.
//
// Observed 2026-10-03: nothing upstream of this repository relates a
// configuration group's documentation to the constant a service reads. The check
// stays regardless of that — a host must not depend on a check it does not run.
func TestProjectedTokenPathAgreesBetweenTheClientAndTheGroup(t *testing.T) {
	t.Parallel()
	const tokenPath = "/var/run/secrets/vault/token"
	for _, file := range []string{
		"module/services/accounts/code/pkg/vaultconnection/kubernetes.go",
		vaultGroupDefaults,
	} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), tokenPath) {
			t.Errorf("%s does not name the projected token path %s", file, tokenPath)
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

// goGRPCMountFloor is the first go-grpc agent release able to render a volume
// this module declares. Observed 2026-10-03 at the pinned 0.1.50: the agent's
// Deployment template hardcodes `automountServiceAccountToken: false` and a
// single /tmp emptyDir, and its manifest spec carries no mount field at all —
// no config-mounts (the nextjs agent has one, go-grpc does not) and no projected
// ServiceAccount token. So the hosted accounts Deployment cannot yet carry the
// Vault CA mount or the audience-`vault` projected token, which is owed in
// codefly-dev/service-go-grpc, not here.
//
// Until a release lands, this floor is unknown and the pairing below is what
// guards the seam: declaring the mounts against an agent that drops them would
// render a pod with no CA and no token while the manifest says otherwise, and
// accounts would refuse to boot with a message pointing at the wrong file.
const goGRPCMountFloor = ""

// The tripwire: the moment someone declares the Vault mounts on accounts, this
// fails unless the agent pin has moved to a release that renders them.
func TestVaultMountsAreNotDeclaredAgainstAnAgentThatDropsThem(t *testing.T) {
	t.Parallel()
	manifest := readAgentPinnedManifest(t, accountsGoGRPCPin)
	if len(manifest.Spec.ConfigMounts) == 0 {
		return
	}
	if goGRPCMountFloor == "" {
		t.Fatalf(
			"%s declares spec.config-mounts, but no go-grpc release is known to render them: the pinned %s hardcodes "+
				"automountServiceAccountToken: false and a single /tmp emptyDir. Set goGRPCMountFloor in this file to the "+
				"release that renders mounts and move the agent pin to it, or the Deployment silently drops the Vault CA "+
				"mount and the projected token while this manifest claims both",
			accountsGoGRPCPin, manifest.Agent.Version)
	}
	pinned, floor := parseAgentVersion(t, manifest.Agent.Version), parseAgentVersion(t, goGRPCMountFloor)
	if pinned[0] < floor[0] ||
		(pinned[0] == floor[0] && pinned[1] < floor[1]) ||
		(pinned[0] == floor[0] && pinned[1] == floor[1] && pinned[2] < floor[2]) {
		t.Fatalf("%s declares spec.config-mounts against go-grpc %s, below the %s that renders them",
			accountsGoGRPCPin, manifest.Agent.Version, goGRPCMountFloor)
	}
}
