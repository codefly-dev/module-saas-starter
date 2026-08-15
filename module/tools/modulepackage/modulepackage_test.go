package modulepackage

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStarterManifestDeclaresTheReleaseBoundary(t *testing.T) {
	moduleRoot := findModuleRoot(t)
	manifest, err := ReadManifest(moduleRoot)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ID != "codefly/saas-starter" || manifest.Version != "0.1.0" {
		t.Fatalf("unexpected package identity: %s@%s", manifest.ID, manifest.Version)
	}
	if len(manifest.ProvidedServices) != 9 {
		t.Fatalf("provided service count = %d, want 9", len(manifest.ProvidedServices))
	}
	wantedContracts := map[string]bool{
		"codefly/saas/frontend-plugin": false,
		"codefly/saas/settings":        false,
		"codefly/saas/permissions":     false,
		"codefly/saas/fixtures":        false,
		"codefly/saas/topology":        false,
	}
	for _, contract := range manifest.ContributionContracts {
		if _, known := wantedContracts[contract.ID]; known {
			wantedContracts[contract.ID] = true
		}
	}
	for contract, found := range wantedContracts {
		if !found {
			t.Errorf("package manifest does not declare %s", contract)
		}
	}
	for _, contract := range manifest.ContributionContracts {
		if contract.ID == "codefly/saas/frontend-plugin" && contract.Versions != ">=2.0.0 <3.0.0" {
			t.Fatalf("frontend contract range = %q, want contract major 2", contract.Versions)
		}
	}
}

func TestManifestRejectsUnknownFields(t *testing.T) {
	repository := newPackageRepository(t)
	manifestPath := filepath.Join(repository, "module", ManifestName)
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, []byte("unknown-field: true\n")...)
	if err := os.WriteFile(manifestPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(filepath.Join(repository, "module")); err == nil || !strings.Contains(err.Error(), "unknown-field") {
		t.Fatalf("ReadManifest() error = %v, want unknown field rejection", err)
	}
}

func TestSemanticVersionValidation(t *testing.T) {
	for _, version := range []string{"0.1.0", "1.2.3-alpha.1+build.7"} {
		if !isSemver(version) {
			t.Errorf("isSemver(%q) = false, want true", version)
		}
	}
	for _, version := range []string{"v1.2.3", "1.2", "01.2.3", "1.2.3-01", "1.2.3+"} {
		if isSemver(version) {
			t.Errorf("isSemver(%q) = true, want false", version)
		}
	}
	if !isSemverRange(">=1.0.0 <2.0.0") ||
		!isSemverRange(">=1.0.0-alpha.1 <1.0.0") ||
		isSemverRange("^1.0.0") ||
		isSemverRange(">=2.0.0 <1.0.0") ||
		isSemverRange(">=1.0.0 <1.0.0") {
		t.Fatal("bounded semantic version range validation failed")
	}
}

func TestManifestRejectsDuplicateTopologyEntriesThatMaskMissingDeclarations(t *testing.T) {
	for name, topology := range map[string]string{
		"service": `services:
  - name: frontend
    endpoints: [{name: http, api: http, visibility: public}]
  - name: frontend
    endpoints: [{name: http, api: http, visibility: public}]
`,
		"endpoint": `services:
  - name: frontend
    endpoints:
      - {name: http, api: http, visibility: public}
      - {name: http, api: http, visibility: public}
`,
	} {
		t.Run(name, func(t *testing.T) {
			repository := newPackageRepository(t)
			moduleRoot := filepath.Join(repository, "module")
			manifestPath := filepath.Join(moduleRoot, ManifestName)
			body, err := os.ReadFile(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			body = bytes.Replace(body, []byte("entry-points: {module: entry.txt}"), []byte("entry-points: {module: entry.txt, topology: topology.yaml}"), 1)
			if name == "service" {
				body = bytes.Replace(body, []byte("  - name: frontend\n    endpoints: [{name: http, protocol: http, visibility: public}]"), []byte("  - name: frontend\n    endpoints: [{name: http, protocol: http, visibility: public}]\n  - name: accounts\n    endpoints: [{name: http, protocol: http, visibility: public}]"), 1)
			} else {
				body = bytes.Replace(body, []byte("endpoints: [{name: http, protocol: http, visibility: public}]"), []byte("endpoints: [{name: http, protocol: http, visibility: public}, {name: api, protocol: http, visibility: public}]"), 1)
			}
			if err := os.WriteFile(manifestPath, body, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(moduleRoot, "topology.yaml"), []byte(topology), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadManifest(moduleRoot); err == nil || !strings.Contains(err.Error(), "duplicated") {
				t.Fatalf("ReadManifest() error = %v, want duplicate topology rejection", err)
			}
		})
	}
}

func TestBuildCreatesTheSameArchiveAndDigestTwice(t *testing.T) {
	repository := newPackageRepository(t)
	commit := git(t, repository, "rev-parse", "HEAD")
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")

	firstMetadata, err := Build(BuildOptions{RepositoryRoot: repository, OutputDir: first, Commit: commit})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	secondMetadata, err := Build(BuildOptions{RepositoryRoot: repository, OutputDir: second, Commit: commit})
	if err != nil {
		t.Fatal(err)
	}
	if firstMetadata != secondMetadata {
		t.Fatalf("artifact metadata changed between builds:\n%+v\n%+v", firstMetadata, secondMetadata)
	}
	firstArchive, err := os.ReadFile(filepath.Join(first, ArchiveName))
	if err != nil {
		t.Fatal(err)
	}
	secondArchive, err := os.ReadFile(filepath.Join(second, ArchiveName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstArchive, secondArchive) {
		t.Fatal("canonical archive bytes changed between builds")
	}
	if _, err := os.Stat(first + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful build left a release reservation: %v", err)
	}
	checksum, err := os.ReadFile(filepath.Join(first, ChecksumName))
	if err != nil {
		t.Fatal(err)
	}
	if string(checksum) != strings.TrimPrefix(firstMetadata.Artifact.Digest, "sha256:")+"  module.tar\n" {
		t.Fatalf("checksum = %q, want artifact digest", checksum)
	}

	reader := tar.NewReader(bytes.NewReader(firstArchive))
	foundManifest := false
	for {
		header, err := reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatal(err)
		}
		if strings.HasPrefix(header.Name, "/") || strings.Contains(header.Name, "../") {
			t.Fatalf("unsafe archive path %q", header.Name)
		}
		if header.Name == "module/"+ManifestName {
			foundManifest = true
		}
	}
	if !foundManifest {
		t.Fatalf("archive does not contain module/%s", ManifestName)
	}
}

func TestBuildRejectsDirtyWorktreeAndExistingAssets(t *testing.T) {
	repository := newPackageRepository(t)
	commit := git(t, repository, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repository, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(BuildOptions{RepositoryRoot: repository, OutputDir: filepath.Join(t.TempDir(), "release"), Commit: commit}); err == nil || !strings.Contains(err.Error(), "clean commit") {
		t.Fatalf("Build() error = %v, want clean commit rejection", err)
	}
	if err := os.Remove(filepath.Join(repository, "dirty.txt")); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "release")
	if err := os.Mkdir(output, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(BuildOptions{RepositoryRoot: repository, OutputDir: output, Commit: commit}); err == nil || !strings.Contains(err.Error(), "refusing to replace existing release directory") {
		t.Fatalf("Build() error = %v, want replacement rejection", err)
	}
}

func TestBuildRejectsSymlinksInThePackageTree(t *testing.T) {
	repository := newPackageRepository(t)
	link := filepath.Join(repository, "module", "link")
	if err := os.Symlink("entry.txt", link); err != nil {
		t.Fatal(err)
	}
	git(t, repository, "add", "module/link")
	git(t, repository, "commit", "-qm", "add symlink")
	commit := git(t, repository, "rev-parse", "HEAD")
	output := filepath.Join(t.TempDir(), "release")
	if _, err := Build(BuildOptions{RepositoryRoot: repository, OutputDir: output, Commit: commit}); err == nil || !strings.Contains(err.Error(), "unsupported git mode 120000") {
		t.Fatalf("Build() error = %v, want symlink rejection", err)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed build left a visible release directory: %v", err)
	}
}

func TestValidateReleaseRefRejectsMovedAndMismatchedTags(t *testing.T) {
	manifest := Manifest{Version: "0.1.0"}
	commit := strings.Repeat("a", 40)
	annotated := strings.Repeat("b", 40) + "\trefs/tags/v0.1.0\n" + commit + "\trefs/tags/v0.1.0^{}\n"
	if err := ValidateReleaseRef(manifest, "v0.1.0", commit, annotated); err != nil {
		t.Fatal(err)
	}
	for name, testCase := range map[string]struct {
		tag  string
		refs string
	}{
		"version": {tag: "v0.2.0", refs: annotated},
		"missing": {tag: "v0.1.0", refs: ""},
		"moved":   {tag: "v0.1.0", refs: strings.Repeat("c", 40) + "\trefs/tags/v0.1.0\n"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateReleaseRef(manifest, testCase.tag, commit, testCase.refs); err == nil {
				t.Fatal("ValidateReleaseRef() accepted an invalid release ref")
			}
		})
	}
}

func TestValidateImmutableReleaseSettings(t *testing.T) {
	if err := ValidateImmutableReleaseSettings([]byte(`{"enabled":true}`)); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"disabled": `{"enabled":false}`,
		"missing":  `{}`,
		"unknown":  `{"enabled":true,"mode":"mutable"}`,
		"trailing": `{"enabled":true} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateImmutableReleaseSettings([]byte(body)); err == nil {
				t.Fatal("ValidateImmutableReleaseSettings() accepted invalid settings")
			}
		})
	}
}

func TestReleaseWorkflowUsesAuthorizedPolicyReadAndRevalidatesBeforePublishing(t *testing.T) {
	workflow, err := os.ReadFile(filepath.Join(filepath.Dir(findModuleRoot(t)), ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(workflow)
	if !strings.Contains(body, "RELEASE_ADMIN_TOKEN: ${{ secrets.RELEASE_ADMIN_TOKEN }}") || !strings.Contains(body, `GH_TOKEN="${RELEASE_ADMIN_TOKEN}" gh api`) {
		t.Fatal("release policy check does not use the required Administration-read credential")
	}
	upload := strings.Index(body, `gh release upload "${GITHUB_REF_NAME}"`)
	revalidate := strings.Index(body, `remote-refs-before-publish`)
	publish := strings.Index(body, `gh release edit "${GITHUB_REF_NAME}" --draft=false`)
	if upload < 0 || revalidate < upload || publish < revalidate {
		t.Fatal("release workflow does not revalidate the remote tag after upload and before publication")
	}
}

func TestDeclaredCommandsExecuteAndGeneratorsMustProduceTheirOutputs(t *testing.T) {
	repository := newPackageRepository(t)
	moduleRoot := filepath.Join(repository, "module")
	manifest, err := ReadManifest(moduleRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := RunGenerators(moduleRoot, manifest); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(moduleRoot, "generated.txt")); err != nil || string(body) != "generated\n" {
		t.Fatalf("generator output = %q, %v", body, err)
	}
	if err := RunConformanceSuites(moduleRoot, manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(moduleRoot, "generated.txt")); err != nil {
		t.Fatal(err)
	}
	manifest.Generators[0].Command = []string{"go", "version"}
	if err := RunGenerators(moduleRoot, manifest); err == nil || !strings.Contains(err.Error(), "did not produce") {
		t.Fatalf("RunGenerators() error = %v, want missing output rejection", err)
	}
}

func newPackageRepository(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	moduleRoot := filepath.Join(repository, "module")
	if err := os.MkdirAll(moduleRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"entry.txt", "contract.txt", "migration.md"} {
		if err := os.WriteFile(filepath.Join(moduleRoot, path), []byte(path+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(moduleRoot, "generator.go"), []byte("package main\n\nimport \"os\"\n\nfunc main() { _ = os.WriteFile(\"generated.txt\", []byte(\"generated\\n\"), 0o644) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := `schema: codefly/module-package/v2
kind: module-package
id: example/package
version: 1.2.3
minimum-codefly-version: 0.3.0
repository: https://github.com/example/package.git
artifact:
  media-type: application/vnd.codefly.module.v2+tar
  roots: [.]
  entry-points: {module: entry.txt}
provided-services:
  - name: frontend
    endpoints: [{name: http, protocol: http, visibility: public}]
contribution-contracts:
  - id: example/frontend
    versions: ">=1.0.0 <2.0.0"
    entry-points: {schema: contract.txt}
generators:
  - id: generate
    working-directory: .
    command: [go, run, generator.go]
    outputs: [generated.txt]
conformance-suites:
  - id: conformance
    working-directory: .
    command: [go, version]
release:
  supported-from: ">=1.0.0 <2.0.0"
  migrations: [{from: ">=1.0.0 <1.2.3", guide: migration.md}]
  breaking-changes: [{version: 1.2.3, summary: package boundary}]
reserved-namespaces: [{kind: package, value: "example/"}]
`
	if err := os.WriteFile(filepath.Join(moduleRoot, ManifestName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repository, "init", "-q")
	git(t, repository, "config", "user.email", "test@example.com")
	git(t, repository, "config", "user.name", "Test")
	git(t, repository, "config", "commit.gpgsign", "false")
	git(t, repository, "add", "module")
	git(t, repository, "commit", "-qm", "fixture")
	return repository
}

func git(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func findModuleRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		candidate := filepath.Clean(filepath.Join(directory, "..", ".."))
		if _, err := os.Stat(filepath.Join(candidate, ManifestName)); err == nil {
			return candidate
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("module root not found")
		}
		directory = parent
	}
}
