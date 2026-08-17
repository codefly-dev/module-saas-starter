package modulepackage

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	corecomposition "github.com/codefly-dev/core/composition"
)

func TestStarterManifestIsTheCoreV2Contract(t *testing.T) {
	moduleRoot := findModuleRoot(t)
	manifest, err := ReadManifest(moduleRoot)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ID != "codefly/saas-starter" || manifest.Version != "0.1.0" {
		t.Fatalf("unexpected package identity: %s@%s", manifest.ID, manifest.Version)
	}
	if len(manifest.Services) != 11 {
		t.Fatalf("provided service count = %d, want 11", len(manifest.Services))
	}
	for _, contract := range []string{"composition", "frontendPlugin", "settings", "permissions", "fixtures"} {
		if manifest.Contracts[contract] == "" {
			t.Errorf("package manifest does not declare Core contract %q", contract)
		}
	}
	if len(manifest.Claims) == 0 {
		t.Fatal("package manifest does not declare base collision claims")
	}
	claimedPackages := map[string]bool{}
	for _, claim := range manifest.Claims {
		if claim.Kind == corecomposition.CollisionPackage {
			claimedPackages[claim.Key] = true
		}
	}
	var frontend struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(mustRead(t, filepath.Join(moduleRoot, "services/frontend/code/package.json")), &frontend); err != nil {
		t.Fatal(err)
	}
	for dependency := range frontend.Dependencies {
		if !claimedPackages[dependency] {
			t.Errorf("base frontend dependency %q has no package collision claim", dependency)
		}
	}
	for dependency := range frontend.DevDependencies {
		if !claimedPackages[dependency] {
			t.Errorf("base frontend development dependency %q has no package collision claim", dependency)
		}
	}
	claimedPermissions := map[string]bool{}
	for _, claim := range manifest.Claims {
		if claim.Kind == corecomposition.CollisionPermission {
			claimedPermissions[claim.Key] = true
		}
	}
	vocabulary := mustRead(t, filepath.Join(moduleRoot, "services/accounts/code/pkg/business/service_vocabulary.go"))
	for _, match := range regexp.MustCompile(`Permission:\s*"([^"]+)"`).FindAllSubmatch(vocabulary, -1) {
		permission := string(match[1])
		if !claimedPermissions[permission] {
			t.Errorf("base Accounts permission %q has no permission collision claim", permission)
		}
	}
}

func TestBuildProducesACoreCanonicalRootArchive(t *testing.T) {
	repository := newPackageRepository(t)
	commit := git(t, repository, "rev-parse", "HEAD")
	releaseDir := filepath.Join(t.TempDir(), "release")
	metadata, err := Build(BuildOptions{RepositoryRoot: repository, OutputDir: releaseDir, Commit: commit})
	if err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile(filepath.Join(releaseDir, ArchiveName))
	if err != nil {
		t.Fatal(err)
	}
	extracted := t.TempDir()
	if err := corecomposition.ExtractArchive(context.Background(), archive, extracted); err != nil {
		t.Fatal(err)
	}
	manifest, err := corecomposition.LoadPackageManifest(extracted)
	if err != nil {
		t.Fatalf("Core could not load the archive root: %v", err)
	}
	if manifest.ID != metadata.Package.ID {
		t.Fatalf("archive package = %q, metadata package = %q", manifest.ID, metadata.Package.ID)
	}
	canonical, digest, err := corecomposition.CanonicalArchive(extracted)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(archive, canonical) {
		t.Fatal("release archive is not byte-for-byte Core canonical")
	}
	if digest != metadata.Artifact.Digest {
		t.Fatalf("canonical digest = %q, metadata digest = %q", digest, metadata.Artifact.Digest)
	}
}

func TestSignedReleaseVerifiesAndMaterializesThroughCore(t *testing.T) {
	repository := newPackageRepository(t)
	commit := git(t, repository, "rev-parse", "HEAD")
	releaseDir := filepath.Join(t.TempDir(), "release")
	if _, err := Build(BuildOptions{RepositoryRoot: repository, OutputDir: releaseDir, Commit: commit}); err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := ReadManifest(filepath.Join(repository, "module"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SignRelease(SignOptions{
		ModuleRoot:        filepath.Join(repository, "module"),
		ReleaseDir:        releaseDir,
		Ref:               "v" + manifest.Version,
		Commit:            commit,
		SignatureIdentity: "test-signer",
		PrivateKey:        []byte(base64.StdEncoding.EncodeToString(privateKey)),
		ExpectedPublicKey: []byte(base64.StdEncoding.EncodeToString(publicKey)),
	}); err != nil {
		t.Fatal(err)
	}
	artifact := mustRead(t, filepath.Join(releaseDir, ArchiveName))
	provenance := mustRead(t, filepath.Join(releaseDir, ProvenanceName))
	signature := mustRead(t, filepath.Join(releaseDir, SignatureName))
	verified, err := corecomposition.VerifyRelease(&corecomposition.Release{
		Repository: PackageRepository,
		Ref:        "v" + manifest.Version,
		Commit:     commit,
		Artifact:   artifact,
		Provenance: provenance,
		Signature:  signature,
	}, manifest.ID, manifest.Version, corecomposition.TrustPolicy{
		Repositories: map[string]string{manifest.ID: PackageRepository},
		Signers:      map[string]ed25519.PublicKey{"test-signer": publicKey},
	})
	if err != nil {
		t.Fatalf("Core rejected signed release: %v", err)
	}
	cacheRoot := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.Walk(cacheRoot, func(path string, info os.FileInfo, err error) error {
			if err == nil {
				_ = os.Chmod(path, info.Mode()|0o700)
			}
			return nil
		})
	})
	materializer := corecomposition.NewMaterializer(cacheRoot)
	if _, err := materializer.Materialize(context.Background(), verified); err != nil {
		t.Fatalf("Core rejected canonical materialization: %v", err)
	}
}

func TestSignReleaseRejectsArtifactMetadataDrift(t *testing.T) {
	repository := newPackageRepository(t)
	commit := git(t, repository, "rev-parse", "HEAD")
	releaseDir := filepath.Join(t.TempDir(), "release")
	if _, err := Build(BuildOptions{RepositoryRoot: repository, OutputDir: releaseDir, Commit: commit}); err != nil {
		t.Fatal(err)
	}
	metadataPath := filepath.Join(releaseDir, MetadataName)
	body := bytes.Replace(mustRead(t, metadataPath), []byte(commit), []byte(strings.Repeat("b", 40)), 1)
	if err := os.WriteFile(metadataPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = SignRelease(SignOptions{
		ModuleRoot: filepath.Join(repository, "module"), ReleaseDir: releaseDir,
		Ref: "v1.2.3", Commit: commit,
		PrivateKey: []byte(base64.StdEncoding.EncodeToString(privateKey)), ExpectedPublicKey: []byte(base64.StdEncoding.EncodeToString(publicKey)),
	})
	if err == nil || !strings.Contains(err.Error(), "does not describe") {
		t.Fatalf("SignRelease() error = %v, want metadata drift rejection", err)
	}
}

func TestSignReleaseRejectsKeyOutsideCoreTrustPolicy(t *testing.T) {
	repository := newPackageRepository(t)
	commit := git(t, repository, "rev-parse", "HEAD")
	releaseDir := filepath.Join(t.TempDir(), "release")
	if _, err := Build(BuildOptions{RepositoryRoot: repository, OutputDir: releaseDir, Commit: commit}); err != nil {
		t.Fatal(err)
	}
	_, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	otherPublicKey, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = SignRelease(SignOptions{
		ModuleRoot: filepath.Join(repository, "module"), ReleaseDir: releaseDir,
		Ref: "v1.2.3", Commit: commit,
		PrivateKey: []byte(base64.StdEncoding.EncodeToString(privateKey)), ExpectedPublicKey: []byte(base64.StdEncoding.EncodeToString(otherPublicKey)),
	})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("SignRelease() error = %v, want Core trust-key mismatch rejection", err)
	}
}

func TestBuildRejectsDirtyWorktreeExistingAssetsAndSymlinks(t *testing.T) {
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
	if _, err := Build(BuildOptions{RepositoryRoot: repository, OutputDir: output, Commit: commit}); err == nil || !strings.Contains(err.Error(), "refusing to replace") {
		t.Fatalf("Build() error = %v, want replacement rejection", err)
	}
	link := filepath.Join(repository, "module", "link")
	if err := os.Symlink("artifact/entry.txt", link); err != nil {
		t.Fatal(err)
	}
	git(t, repository, "add", "module/link")
	git(t, repository, "commit", "-qm", "add symlink")
	commit = git(t, repository, "rev-parse", "HEAD")
	if _, err := Build(BuildOptions{RepositoryRoot: repository, OutputDir: filepath.Join(t.TempDir(), "symlink"), Commit: commit}); err == nil || !strings.Contains(err.Error(), "unsupported git mode 120000") {
		t.Fatalf("Build() error = %v, want symlink rejection", err)
	}
}

func TestValidateImmutableReleaseSettingsAcceptsDocumentedResponse(t *testing.T) {
	if err := ValidateImmutableReleaseSettings([]byte(`{"enabled":true,"enforced_by_owner":false}`)); err != nil {
		t.Fatalf("documented GitHub response was rejected: %v", err)
	}
	if err := ValidateImmutableReleaseSettings([]byte(`{"enabled":true,"future_field":"allowed"}`)); err != nil {
		t.Fatalf("forward-compatible GitHub response was rejected: %v", err)
	}
	for _, body := range []string{`{"enabled":false}`, `{}`, `{"enabled":true} {}`} {
		if err := ValidateImmutableReleaseSettings([]byte(body)); err == nil {
			t.Fatalf("ValidateImmutableReleaseSettings(%s) accepted invalid policy", body)
		}
	}
}

func TestValidateReleaseRefRejectsMovedAndMismatchedTags(t *testing.T) {
	manifest := Manifest{Version: "0.1.0"}
	commit := strings.Repeat("a", 40)
	annotated := strings.Repeat("b", 40) + "\trefs/tags/v0.1.0\n" + commit + "\trefs/tags/v0.1.0^{}\n"
	if err := ValidateReleaseRef(manifest, "v0.1.0", commit, annotated); err != nil {
		t.Fatal(err)
	}
	for _, refs := range []string{"", strings.Repeat("c", 40) + "\trefs/tags/v0.1.0\n"} {
		if err := ValidateReleaseRef(manifest, "v0.1.0", commit, refs); err == nil {
			t.Fatal("ValidateReleaseRef() accepted a missing or moved tag")
		}
	}
}

func TestValidateReleaseAncestryRejectsUnmergedCommit(t *testing.T) {
	repository := newPackageRepository(t)
	mainCommit := git(t, repository, "rev-parse", "HEAD")
	git(t, repository, "branch", "-M", "main")
	if err := ValidateReleaseAncestry(repository, mainCommit, "main"); err != nil {
		t.Fatalf("main commit was rejected: %v", err)
	}
	git(t, repository, "checkout", "-qb", "side")
	if err := os.WriteFile(filepath.Join(repository, "side.txt"), []byte("unreviewed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repository, "add", "side.txt")
	git(t, repository, "commit", "-qm", "side")
	sideCommit := git(t, repository, "rev-parse", "HEAD")
	if err := ValidateReleaseAncestry(repository, sideCommit, "main"); err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("ValidateReleaseAncestry() error = %v, want unmerged-commit rejection", err)
	}
}

func TestDeclaredCommandsExecute(t *testing.T) {
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
}

func newPackageRepository(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	moduleRoot := filepath.Join(repository, "module")
	if err := os.MkdirAll(filepath.Join(moduleRoot, "artifact"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(moduleRoot, "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleRoot, "artifact", "entry.txt"), []byte("entry\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleRoot, "tools", "generator.go"), []byte("package main\n\nimport \"os\"\n\nfunc main() { _ = os.WriteFile(\"../generated.txt\", []byte(\"generated\\n\"), 0o644) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := `schema: codefly/module-package/v2
kind: module-package
id: example/package
version: 1.2.3
minimum-codefly-version: ">=0.3.0 <1.0.0"
artifact-roots: [artifact]
contracts: {composition: ">=2.0.0 <3.0.0"}
generators:
  - name: generate
    working-directory: tools
    command: [go, run, generator.go]
reserved-namespaces: [example]
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

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
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
