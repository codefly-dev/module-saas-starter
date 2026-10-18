package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"policy-log/mirror"

	"gopkg.in/yaml.v3"
)

// TestResolveSettingsNamesEveryMissingKeyAtOnce: a half-provisioned group must
// cost one restart to diagnose, not one per key, and it must fail rather than
// start a listener that cannot witness.
func TestResolveSettingsNamesEveryMissingKeyAtOnce(t *testing.T) {
	_, err := resolveSettings(lookup(func(string, string) (string, error) { return "", nil }))
	if err == nil {
		t.Fatal("resolveSettings accepted a group that delivered nothing")
	}
	for _, key := range []string{settingKeys.bucket, settingKeys.retentionDays} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("the refusal does not name the missing %s: %v", key, err)
		}
	}
	// No bucket and a token with no endpoint: both defects in one refusal.
	_, err = resolveSettings(lookup(func(_, key string) (string, error) {
		if key == settingKeys.warehouseToken {
			return "example-warehouse-token", nil
		}
		return "", nil
	}))
	if err == nil {
		t.Fatal("resolveSettings accepted a group with no bucket and half a mirror")
	}
	for _, key := range []string{settingKeys.bucket, settingKeys.warehouseEndpoint} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("the refusal does not name the missing %s: %v", key, err)
		}
	}
}

// TestABucketWithNoMirrorIsAStartableWitness: the owner's 2026-10-18 decision.
// The object store is the authority and the warehouse only mirrors it, so a
// group delivering the bucket and NEITHER mirror setting resolves, with no
// mirror — it is not read as a half-provisioned group.
func TestABucketWithNoMirrorIsAStartableWitness(t *testing.T) {
	resolved, err := resolveSettings(lookup(func(_, key string) (string, error) {
		switch key {
		case settingKeys.bucket:
			return "example-witness-bucket", nil
		case settingKeys.retentionDays:
			return "2557", nil
		}
		return "", errors.New("not delivered")
	}))
	if err != nil {
		t.Fatalf("a bucket with no mirror was refused: %v", err)
	}
	if resolved.bucket != "example-witness-bucket" || resolved.mirrored() {
		t.Fatalf("resolved %+v, want the bucket and no mirror", resolved)
	}
}

// TestNoMirrorIsStatedByNameAtBoot: a witness with no mirror wires mirror.None
// — a named value, never a nil a check could absorb — and says so in one line
// that names both settings it would need, so the pod log answers "why is the
// warehouse empty" without a code read.
func TestNoMirrorIsStatedByNameAtBoot(t *testing.T) {
	var said []string
	worker, err := startMirror(context.Background(), settings{bucket: "example-witness-bucket"},
		func(line string) { said = append(said, line) })
	if err != nil {
		t.Fatalf("startMirror with no mirror: %v", err)
	}
	if _, ok := worker.(mirror.None); !ok {
		t.Fatalf("startMirror wired %T, want mirror.None", worker)
	}
	if len(said) != 1 {
		t.Fatalf("startMirror said %d lines, want exactly one: %q", len(said), said)
	}
	for _, want := range []string{"no warehouse mirror is configured", "object store is the authority",
		settingKeys.warehouseEndpoint, settingKeys.warehouseToken} {
		if !strings.Contains(said[0], want) {
			t.Errorf("the boot line does not say %q: %s", want, said[0])
		}
	}
}

// TestAConfiguredMirrorIsBuiltAndStaysSilent: the other branch. A whole mirror
// starts the bounded worker and prints no no-mirror line; a mirror the writer
// refuses (not https) still refuses the boot, as before the decision.
func TestAConfiguredMirrorIsBuiltAndStaysSilent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	silent := func(line string) { t.Fatalf("a configured mirror printed %q", line) }
	worker, err := startMirror(ctx, settings{bucket: "b", warehouseEndpoint: "https://warehouse.example.com/ingest",
		warehouseToken: "example-warehouse-token"}, silent)
	if err != nil {
		t.Fatalf("startMirror with a whole mirror: %v", err)
	}
	if _, ok := worker.(*mirror.Worker); !ok {
		t.Fatalf("startMirror wired %T, want *mirror.Worker", worker)
	}
	if _, err := startMirror(ctx, settings{bucket: "b", warehouseEndpoint: "http://warehouse.example.com/ingest",
		warehouseToken: "example-warehouse-token"}, silent); err == nil {
		t.Fatal("startMirror accepted a mirror the writer refuses")
	}
}

// TestResolveSettingsRefusesEachMissingKeyIndividually proves no single setting
// has a default. A bucket with half a mirror, or a mirror without a bucket, is
// not a startable configuration.
func TestResolveSettingsRefusesEachMissingKeyIndividually(t *testing.T) {
	delivered := map[string]string{
		settingKeys.bucket:            "example-witness-bucket",
		settingKeys.retentionDays:     "2557",
		settingKeys.warehouseEndpoint: "https://warehouse.example.com/ingest",
		settingKeys.warehouseToken:    "example-warehouse-token",
	}
	for withheld := range delivered {
		_, err := resolveSettings(lookup(func(_, key string) (string, error) {
			if key == withheld {
				return "", nil
			}
			return delivered[key], nil
		}))
		if err == nil {
			t.Errorf("resolveSettings started with %s missing", withheld)
			continue
		}
		if !strings.Contains(err.Error(), withheld) {
			t.Errorf("the refusal for a missing %s does not name it: %v", withheld, err)
		}
	}
}

// TestResolveSettingsNeverFallsBackToTheProcessEnvironment is the one that
// keeps "no development bypass" true. An ambient bucket or warehouse credential
// would make this service witness somewhere nobody provisioned — and it would
// do so by starting successfully.
func TestResolveSettingsNeverFallsBackToTheProcessEnvironment(t *testing.T) {
	t.Setenv(settingKeys.bucket, "ambient-bucket")
	t.Setenv(settingKeys.warehouseEndpoint, "https://ambient.example.com/ingest")
	t.Setenv(settingKeys.warehouseToken, "ambient-token")
	// The group is absent, exactly as it is on a workstation.
	resolved, err := resolveSettings(lookup(func(string, string) (string, error) {
		return "", errors.New("no configuration found for policy-log")
	}))
	if err == nil {
		t.Fatalf("resolveSettings fell back to the process environment and resolved %+v", resolved)
	}
	for _, ambient := range []string{"ambient-bucket", "ambient.example.com", "ambient-token"} {
		if strings.Contains(err.Error(), ambient) {
			t.Errorf("the refusal echoed an ambient value %q", ambient)
		}
	}
}

// TestResolveSettingsNeverEchoesADeliveredValue: one of the three settings is a
// credential, and a startup refusal is the most widely read log line a service
// has. The refusal names keys, never values.
func TestResolveSettingsNeverEchoesADeliveredValue(t *testing.T) {
	const credential = "s3cr3t-warehouse-token"
	_, err := resolveSettings(lookup(func(_, key string) (string, error) {
		switch key {
		case settingKeys.warehouseToken:
			return credential, nil
		case settingKeys.bucket:
			return "example-witness-bucket", nil
		}
		return "", nil
	}))
	if err == nil {
		t.Fatal("resolveSettings accepted a group with no warehouse endpoint")
	}
	for _, value := range []string{credential, "example-witness-bucket"} {
		if strings.Contains(err.Error(), value) {
			t.Fatalf("the refusal echoed a delivered value %q: %v", value, err)
		}
	}
}

// TestResolveSettingsAcceptsAWholeGroup is the positive half: a fully delivered
// group resolves, and each value lands in its own field rather than being
// transposed.
func TestResolveSettingsAcceptsAWholeGroup(t *testing.T) {
	resolved, err := resolveSettings(lookup(func(group, key string) (string, error) {
		if group != policyLogConfiguration {
			t.Fatalf("resolveSettings read group %q, want %q", group, policyLogConfiguration)
		}
		switch key {
		case settingKeys.bucket:
			return "example-witness-bucket", nil
		case settingKeys.retentionDays:
			return "2557", nil
		case settingKeys.warehouseEndpoint:
			return "https://warehouse.example.com/ingest", nil
		case settingKeys.warehouseToken:
			return "example-warehouse-token", nil
		}
		t.Fatalf("resolveSettings read an undeclared key %q", key)
		return "", nil
	}))
	if err != nil {
		t.Fatalf("resolveSettings: %v", err)
	}
	if resolved.bucket != "example-witness-bucket" || resolved.retention != 2557*24*time.Hour ||
		resolved.warehouseEndpoint != "https://warehouse.example.com/ingest" ||
		resolved.warehouseToken != "example-warehouse-token" {
		t.Fatalf("resolveSettings transposed a value: %+v", resolved)
	}
}

type manifest struct {
	Name      string `yaml:"name"`
	Endpoints []struct {
		Name       string `yaml:"name"`
		API        string `yaml:"api"`
		Visibility string `yaml:"visibility"`
	} `yaml:"endpoints"`
	ServiceDependencies []struct {
		Name string `yaml:"name"`
	} `yaml:"service-dependencies"`
	Spec struct {
		ConnectEndpoint   bool     `yaml:"connect-endpoint"`
		ProtocolOutputDir []string `yaml:"protocol-output-dirs"`
	} `yaml:"spec"`
}

func readManifest(t *testing.T) manifest {
	t.Helper()
	body, err := os.ReadFile("../service.codefly.yaml")
	if err != nil {
		t.Fatalf("read the service manifest: %v", err)
	}
	var declared manifest
	if err := yaml.Unmarshal(body, &declared); err != nil {
		t.Fatalf("parse the service manifest: %v", err)
	}
	return declared
}

// TestTheWitnessListenerStaysPrivate is a gate, not a description. The witness
// must be reachable only by the host's declared client through
// workload-authenticated mesh policy: a `visibility` of module or public, a
// second endpoint, a Connect listener or a REST api would each put it in front
// of composed workloads or the public gateway, where a shared credential would
// be enough authority to append.
func TestTheWitnessListenerStaysPrivate(t *testing.T) {
	declared := readManifest(t)
	if len(declared.Endpoints) != 1 {
		t.Fatalf("the witness declares %d endpoints; it must declare exactly the one private listener", len(declared.Endpoints))
	}
	endpoint := declared.Endpoints[0]
	if endpoint.Name != grpcEndpointName {
		t.Errorf("the declared endpoint is %q but the binary looks up %q, so it would bind no port", endpoint.Name, grpcEndpointName)
	}
	if endpoint.Visibility != "" {
		t.Errorf("the witness endpoint declares visibility %q; it must declare none, which is private", endpoint.Visibility)
	}
	if endpoint.API != "" && endpoint.API != "grpc" {
		t.Errorf("the witness endpoint declares api %q; only grpc may be projected", endpoint.API)
	}
	if declared.Spec.ConnectEndpoint {
		t.Error("the witness declares a Connect endpoint, which is browser-reachable through the gateway")
	}
	for _, dependency := range declared.ServiceDependencies {
		t.Errorf("the witness declares a dependency on %q; it depends on nothing in the graph and must not be routed through one", dependency.Name)
	}
}

// TestEveryDeclaredProtocolOutputDirIsGenerated: a declared generated directory
// that no generator writes is a claim nobody can reproduce, and the inverse —
// generated bytes under a directory the manifest does not declare — is output
// Codefly's sync-drift phase does not know it owns.
func TestEveryDeclaredProtocolOutputDirIsGenerated(t *testing.T) {
	declared := readManifest(t)
	if len(declared.Spec.ProtocolOutputDir) == 0 {
		t.Fatal("the manifest declares no protocol output directory, so the agent has no protobuf sync input")
	}
	for _, dir := range declared.Spec.ProtocolOutputDir {
		entries, err := os.ReadDir("../" + dir)
		if err != nil {
			t.Errorf("declared protocol output dir %q does not exist: %v", dir, err)
			continue
		}
		if len(entries) == 0 {
			t.Errorf("declared protocol output dir %q is empty; nothing generates into it", dir)
		}
	}
}

func TestSigningIdentityComesOnlyFromDeliveredSecret(t *testing.T) {
	t.Setenv("POLICY_LOG_SIGNING_KEY", "ambient-secret-must-not-be-read")
	t.Setenv("POLICY_LOG_SIGNING_KEY_PATH", "/tmp/unused-key")
	_, err := resolveSigningKey(func(group, key string) (string, error) {
		if group != policyLogConfiguration || key != "POLICY_LOG_SIGNING_KEY" {
			t.Fatalf("unexpected secret selection: %s/%s", group, key)
		}
		return "", errors.New("provider error containing private value")
	})
	if err == nil || !strings.Contains(err.Error(), "POLICY_LOG_SIGNING_KEY") || strings.Contains(err.Error(), "private value") {
		t.Fatalf("missing secret refusal: %v", err)
	}
	want := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{19}, 32))
	der, err := x509.MarshalPKCS8PrivateKey(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolveSigningKey(func(string, string) (string, error) { return base64.StdEncoding.EncodeToString(der), nil })
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("delivered signing identity: %v", err)
	}
}

func TestInvalidDeliveredSigningSecretNamesItsSetting(t *testing.T) {
	for _, value := range []string{"", "malformed-private-value"} {
		key, err := resolveSigningKey(func(string, string) (string, error) { return value, nil })
		if err == nil || key != nil || !strings.Contains(err.Error(), "policy-log/POLICY_LOG_SIGNING_KEY") {
			t.Fatalf("unnamed refusal: %v", err)
		}
		if value != "" && strings.Contains(err.Error(), value) {
			t.Fatal("refusal exposed secret")
		}
	}
}

// lookup adapts a (value, error) answer to WorkspaceValueIfSet's three-way
// one: an error is "nothing carries this key" unless it is errUnreadable,
// which stands for a delivered value the carrier could not read.
func lookup(value func(group, key string) (string, error)) func(group, key string) (string, bool, error) {
	return func(group, key string) (string, bool, error) {
		got, err := value(group, key)
		if errors.Is(err, errUnreadable) {
			return "", false, err
		}
		if err != nil || got == "" {
			return "", false, nil
		}
		return got, true, nil
	}
}

var errUnreadable = errors.New("delivered but unreadable")

// TestAnUnreadableMirrorIsRefusedNotReadAsAbsent: both mirror settings
// delivered and neither readable (a broken file carrier) must refuse the boot
// by name. Read as absent, the witness would start claiming no mirror was
// configured — a provisioned projection gone dark with the wrong cause logged.
func TestAnUnreadableMirrorIsRefusedNotReadAsAbsent(t *testing.T) {
	_, err := resolveSettings(lookup(func(_, key string) (string, error) {
		switch key {
		case settingKeys.bucket:
			return "example-witness-bucket", nil
		case settingKeys.retentionDays:
			return "2557", nil
		}
		return "", errUnreadable
	}))
	if err == nil {
		t.Fatal("resolveSettings started a witness whose mirror settings could not be read")
	}
	for _, key := range []string{settingKeys.warehouseEndpoint, settingKeys.warehouseToken} {
		if !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "could not be read") {
			t.Errorf("the refusal does not name the unreadable %s: %v", key, err)
		}
	}
}

// TestTheEntryRetentionIsAWholeBoundedNumberOfDays: a Locked retention can
// never be shortened, so anything but a plain positive day count within the
// adapter's bound is refused by name rather than rounded or clamped.
func TestTheEntryRetentionIsAWholeBoundedNumberOfDays(t *testing.T) {
	for _, value := range []string{"0", "-1", "7y", "2557.5", " 2557", "02557", "4000", "213504", "9223372036854775807"} {
		_, err := resolveSettings(lookup(func(_, key string) (string, error) {
			switch key {
			case settingKeys.bucket:
				return "example-witness-bucket", nil
			case settingKeys.retentionDays:
				return value, nil
			}
			return "", nil
		}))
		if err == nil || !strings.Contains(err.Error(), settingKeys.retentionDays) {
			t.Errorf("retention %q was accepted or not named: %v", value, err)
		}
	}
}
