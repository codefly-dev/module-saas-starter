// Command generate narrows the exported descriptor to ModuleCapabilitiesService's
// dependency closure before invoking Codefly's client generator.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func main() {
	root := flag.String("root", "../../..", "repository root")
	flag.Parse()
	if err := generate(*root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func generate(root string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(root, "module/contracts/api/accounts/connect/contract.binpb"))
	if err != nil {
		return err
	}
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(raw, &set); err != nil {
		return err
	}
	byName := map[string]*descriptorpb.FileDescriptorProto{}
	for _, f := range set.File {
		byName[f.GetName()] = f
	}
	needed := map[string]bool{}
	var visit func(string) error
	visit = func(name string) error {
		if needed[name] {
			return nil
		}
		f := byName[name]
		if f == nil {
			return fmt.Errorf("missing descriptor %s", name)
		}
		needed[name] = true
		for _, dep := range f.Dependency {
			if err := visit(dep); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit("saas/accounts/v1/module_capabilities.proto"); err != nil {
		return err
	}
	var narrowed descriptorpb.FileDescriptorSet
	for _, f := range set.File {
		if needed[f.GetName()] {
			// Imported files supply message types, not extra client services.
			f = proto.Clone(f).(*descriptorpb.FileDescriptorProto)
			if f.GetName() != "saas/accounts/v1/module_capabilities.proto" {
				f.Service = nil
			}
			narrowed.File = append(narrowed.File, f)
		}
	}
	raw, err = proto.MarshalOptions{Deterministic: true}.Marshal(&narrowed)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	catalogRaw, err := os.ReadFile(filepath.Join(root, "module/contracts/api/catalog.codefly.json"))
	if err != nil {
		return err
	}
	var catalog map[string]any
	if err := json.Unmarshal(catalogRaw, &catalog); err != nil {
		return err
	}
	var endpoint map[string]any
	for _, entry := range catalog["endpoints"].([]any) {
		e := entry.(map[string]any)
		if e["service"] == "accounts" && e["endpoint"] == "connect" {
			endpoint = e
			break
		}
	}
	if endpoint == nil {
		return fmt.Errorf("accounts contract missing")
	}
	var services []any
	for _, entry := range endpoint["services"].([]any) {
		if entry.(map[string]any)["name"] == "ModuleCapabilitiesService" {
			services = append(services, entry)
		}
	}
	endpoint["services"] = services
	endpoint["path"] = "contracts/api/contract.binpb"
	endpoint["digest"] = "sha256:" + hex.EncodeToString(digest[:])
	catalog["endpoints"] = []any{endpoint}
	catalogRaw, err = json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.MkdirTemp("", "source-read-contract-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(temp) }()
	if err := os.MkdirAll(filepath.Join(temp, "contracts/api"), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(temp, "contracts/api/contract.binpb"), raw, 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(temp, "contracts/api/catalog.codefly.json"), catalogRaw, 0644); err != nil {
		return err
	}
	output := filepath.Join(root, "libraries/source-read-sdk")
	// Only generated bindings are replaced; authored transport, tests and this
	// generator remain intact. Removed descriptor files cannot linger on reruns.
	if err := os.RemoveAll(filepath.Join(output, "go/gen")); err != nil {
		return err
	}
	cmd := exec.Command("codefly", "generate", "client", "--from", "contracts:"+temp, "--endpoint", "accounts/connect", "--name", "source-read-sdk", "--language", "go", "--services", "ModuleCapabilitiesService", "--go-module", "github.com/codefly-dev/module-saas-starter/libraries/source-read-sdk/go", "--output", output, "--force")
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	// Regenerate owned bindings with canonical shared Host descriptor imports.
	// The public SDK and an embedded owner must not register a second policy,
	// jobs or Work Context descriptor under the same protobuf full name.
	own := "github.com/codefly-dev/module-saas-starter/libraries/source-read-sdk/go/gen/"
	shared := "github.com/codefly-dev/saas-sdk-go/gen/"
	options := []string{"paths=source_relative",
		"Msaas/accounts/v1/module_capabilities.proto=" + own + "saas/accounts/v1;accountsv1",
		"Msaas/accounts/v1/module_registration.proto=" + own + "saas/accounts/v1;accountsv1",
		"Msaas/events/v1/events.proto=" + own + "saas/events/v1;eventsv1",
		"Msaas/accounts/v1/work_contexts.proto=" + shared + "saas/accounts/v1",
		"Msaas/jobs/v1/jobs.proto=" + shared + "saas/jobs/v1",
		"Msaas/policy/v1/options.proto=" + shared + "saas/policy/v1"}
	template, err := json.Marshal(map[string]any{"version": "v1", "plugins": []any{
		map[string]any{"name": "go", "out": filepath.Join(output, "go/gen"), "opt": options},
		map[string]any{"name": "connect-go", "out": filepath.Join(output, "go/gen"), "opt": options},
	}})
	if err != nil {
		return err
	}
	if err = os.RemoveAll(filepath.Join(output, "go/gen")); err != nil {
		return err
	}
	bindings := exec.Command("buf", "generate", filepath.Join(temp, "contracts/api/contract.binpb"), "--path", "saas/accounts/v1/module_capabilities.proto", "--path", "saas/accounts/v1/module_registration.proto", "--path", "saas/events/v1/events.proto", "--template", string(template))
	bindings.Stdout = os.Stdout
	bindings.Stderr = os.Stderr
	if err = bindings.Run(); err != nil {
		return err
	}
	// Preserve the facade's public result name without duplicating its descriptor.
	alias := []byte("package accountsv1\n\nimport shared \"github.com/codefly-dev/saas-sdk-go/gen/saas/accounts/v1\"\n\n// IssuedWorkContext is the canonical shared Host wire type.\ntype IssuedWorkContext = shared.IssuedWorkContext\n")
	if err = os.WriteFile(filepath.Join(output, "go/gen/saas/accounts/v1/shared.go"), alias, 0644); err != nil {
		return err
	}
	dependency := exec.Command("go", "mod", "edit", "-require=github.com/codefly-dev/saas-sdk-go@v0.0.3-0.20260903161219-eec695334b45")
	dependency.Dir = filepath.Join(output, "go")
	dependency.Stdout = os.Stdout
	dependency.Stderr = os.Stderr
	if err = dependency.Run(); err != nil {
		return err
	}
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = filepath.Join(output, "go")
	tidy.Stdout = os.Stdout
	tidy.Stderr = os.Stderr
	if err = tidy.Run(); err != nil {
		return err
	}
	facade := filepath.Join(output, "go/accounts_facade.pb.go")
	raw, err = os.ReadFile(facade)
	if err != nil {
		return err
	}
	old := "return &Client{gw: gw, opts: opts}"
	if strings.Count(string(raw), old) != 1 {
		return fmt.Errorf("facade constructor changed; cannot enforce internal gRPC transport")
	}
	raw = []byte(strings.Replace(string(raw), old, "return &Client{gw: gw, opts: append(opts, connect.WithGRPC())}", 1))
	return os.WriteFile(facade, raw, 0644)
}
