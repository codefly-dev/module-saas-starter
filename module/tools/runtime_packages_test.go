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

// A service's runtime image carries its binary, the CA bundle, and nothing else.
// A service that runs an OS binary therefore works in dev — where the developer's
// own machine supplies it — and fails on a cell, where the image does not. The
// go-grpc agent's `spec.runtime-packages` is the one place that dependency can be
// declared so the built image carries it.
//
// This gate exists because the declaration and the code that needs it live in
// different files, and nothing connected them: accounts' datasource fetch ran
// `git` from the moment it was written, the agent pin was moved to a build that
// supports runtime-packages *for that reason*, and the declaration itself was
// still never added. Every repository sync on the cell refused with "git is not
// installed in its image", correctly and unretryably, while the local suite
// passed. Holding the manifest to the code closes that gap in the direction that
// actually failed: the code is the fact, the manifest must catch up to it.

// lookPathCall finds a binary this service resolves on PATH before running it.
// exec.LookPath is how a Go service asks "is this binary in my image?", so its
// literal argument is the runtime dependency, named at the call site.
var lookPathCall = regexp.MustCompile(`exec\.LookPath\("([a-z0-9][a-z0-9+._-]{0,63})"\)`)

// alpinePackageForBinary maps a binary onto the Alpine package that provides it,
// for the cases where the two names differ. A binary absent from this map is
// assumed to be its own package, which is true for git and for most of the
// single-binary packages a service would shell out to. Add an entry rather than
// letting the gate demand a package that does not exist.
var alpinePackageForBinary = map[string]string{}

// serviceManifestRuntimePackages is the slice of a service manifest this gate
// reads: the agent that builds the image, and the packages the image installs.
type serviceManifestRuntimePackages struct {
	Name  string `yaml:"name"`
	Agent struct {
		Name string `yaml:"name"`
	} `yaml:"agent"`
	Spec struct {
		RuntimePackages []string `yaml:"runtime-packages"`
	} `yaml:"spec"`
}

func TestServicesDeclareEveryBinaryTheyRunAsARuntimePackage(t *testing.T) {
	moduleDir := findModuleDir(t)
	servicesDir := filepath.Join(moduleDir, "services")
	entries, err := os.ReadDir(servicesDir)
	if err != nil {
		t.Fatalf("read services directory: %v", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		serviceDir := filepath.Join(servicesDir, entry.Name())
		manifestPath := filepath.Join(serviceDir, "service.codefly.yaml")
		raw, readErr := os.ReadFile(manifestPath)
		if readErr != nil {
			continue // not a service directory
		}
		var manifest serviceManifestRuntimePackages
		if unmarshalErr := yaml.Unmarshal(raw, &manifest); unmarshalErr != nil {
			t.Fatalf("%s: %v", manifestPath, unmarshalErr)
		}

		binaries := binariesRunByService(t, serviceDir)
		if len(binaries) == 0 {
			continue
		}

		// Only the go-grpc agent renders a runtime stage this module can add
		// packages to. A service on another agent that shells out is a real
		// problem, but not one runtime-packages can express — say so rather
		// than demanding a field the agent does not read.
		if manifest.Agent.Name != "go-grpc" {
			t.Errorf("service %s runs %v at runtime but is built by the %q agent, which has no runtime-packages; the image cannot be made to carry them",
				manifest.Name, binaries, manifest.Agent.Name)
			continue
		}

		declared := make(map[string]bool, len(manifest.Spec.RuntimePackages))
		for _, pkg := range manifest.Spec.RuntimePackages {
			declared[pkg] = true
		}
		for _, binary := range binaries {
			pkg := binary
			if mapped, ok := alpinePackageForBinary[binary]; ok {
				pkg = mapped
			}
			if !declared[pkg] {
				t.Errorf("service %s runs %q at runtime (exec.LookPath) but %s does not declare %q in spec.runtime-packages; "+
					"the image carries only the CA bundle, so this works in dev and fails on a cell",
					manifest.Name, binary, mustRel(t, moduleDir, manifestPath), pkg)
			}
		}
	}
}

// binariesRunByService returns every binary the service's non-test Go code
// resolves on PATH, sorted and deduplicated. Test files are excluded: a test
// binary runs on a developer machine or a CI runner, never in the service image,
// so a test's own git dependency says nothing about what the image must carry.
func binariesRunByService(t *testing.T, serviceDir string) []string {
	t.Helper()
	found := map[string]bool{}
	walkErr := filepath.WalkDir(serviceDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "node_modules", ".git", "vendor", "gen":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, match := range lookPathCall.FindAllStringSubmatch(string(content), -1) {
			found[match[1]] = true
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk %s: %v", serviceDir, walkErr)
	}
	binaries := make([]string, 0, len(found))
	for binary := range found {
		binaries = append(binaries, binary)
	}
	sort.Strings(binaries)
	return binaries
}

func mustRel(t *testing.T, base, path string) string {
	t.Helper()
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return path
	}
	return rel
}
