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
	if !isSemverRange(">=1.0.0 <2.0.0") || isSemverRange("^1.0.0") {
		t.Fatal("bounded semantic version range validation failed")
	}
}

func TestBuildCreatesTheSameArchiveAndDigestTwice(t *testing.T) {
	repository := newPackageRepository(t)
	commit := git(t, repository, "rev-parse", "HEAD")
	first := t.TempDir()
	second := t.TempDir()

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
	if _, err := Build(BuildOptions{RepositoryRoot: repository, OutputDir: t.TempDir(), Commit: commit}); err == nil || !strings.Contains(err.Error(), "clean commit") {
		t.Fatalf("Build() error = %v, want clean commit rejection", err)
	}
	if err := os.Remove(filepath.Join(repository, "dirty.txt")); err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	if err := os.WriteFile(filepath.Join(output, ArchiveName), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(BuildOptions{RepositoryRoot: repository, OutputDir: output, Commit: commit}); err == nil || !strings.Contains(err.Error(), "refusing to replace") {
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
	if _, err := Build(BuildOptions{RepositoryRoot: repository, OutputDir: t.TempDir(), Commit: commit}); err == nil || !strings.Contains(err.Error(), "unsupported git mode 120000") {
		t.Fatalf("Build() error = %v, want symlink rejection", err)
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
    command: [go, version]
    outputs: [generated.txt]
conformance-suites:
  - id: conformance
    working-directory: .
    command: [go, test, ./...]
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
