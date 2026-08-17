package modulepackage

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	corecomposition "github.com/codefly-dev/core/composition"
)

const (
	ArchiveName              = "module.tar"
	ChecksumName             = "module.tar.sha256"
	MetadataName             = "artifact.json"
	ProvenanceName           = "provenance.json"
	SignatureName            = "provenance.sig"
	PackageRepository        = "https://github.com/codefly-dev/module-saas-starter.git"
	ReleaseSignatureIdentity = "https://github.com/codefly-dev/module-saas-starter/.github/workflows/ci.yml@refs/heads/main"
)

type ArtifactMetadata struct {
	Schema   string         `json:"schema"`
	Package  PackageSubject `json:"package"`
	Source   Source         `json:"source"`
	Artifact ArtifactFile   `json:"artifact"`
}

type PackageSubject struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

type Source struct {
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
}

type ArtifactFile struct {
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

type BuildOptions struct {
	RepositoryRoot string
	OutputDir      string
	Commit         string
}

func Build(options BuildOptions) (ArtifactMetadata, error) {
	repositoryRoot, err := filepath.Abs(options.RepositoryRoot)
	if err != nil {
		return ArtifactMetadata{}, fmt.Errorf("resolve repository root: %w", err)
	}
	commit, err := requireCleanCommit(repositoryRoot, options.Commit)
	if err != nil {
		return ArtifactMetadata{}, err
	}
	moduleRoot := filepath.Join(repositoryRoot, "module")
	manifest, err := ReadManifest(moduleRoot)
	if err != nil {
		return ArtifactMetadata{}, err
	}
	if err := validateGitTree(repositoryRoot, commit); err != nil {
		return ArtifactMetadata{}, err
	}
	outputDir, err := filepath.Abs(options.OutputDir)
	if err != nil {
		return ArtifactMetadata{}, fmt.Errorf("resolve release directory: %w", err)
	}
	if _, err := os.Lstat(outputDir); err == nil {
		return ArtifactMetadata{}, fmt.Errorf("refusing to replace existing release directory %s", outputDir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return ArtifactMetadata{}, fmt.Errorf("inspect release directory: %w", err)
	}
	parent := filepath.Dir(outputDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return ArtifactMetadata{}, fmt.Errorf("create release parent directory: %w", err)
	}
	lockPath := outputDir + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return ArtifactMetadata{}, fmt.Errorf("reserve release directory: %w", err)
	}
	defer func() { _ = os.Remove(lockPath) }()
	if err := lock.Close(); err != nil {
		return ArtifactMetadata{}, fmt.Errorf("close release reservation: %w", err)
	}
	if _, err := os.Lstat(outputDir); err == nil {
		return ArtifactMetadata{}, fmt.Errorf("refusing to replace existing release directory %s", outputDir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return ArtifactMetadata{}, fmt.Errorf("inspect reserved release directory: %w", err)
	}
	stagingDir, err := os.MkdirTemp(parent, ".module-release-*")
	if err != nil {
		return ArtifactMetadata{}, fmt.Errorf("create staged release directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stagingDir) }()

	temporary, err := os.CreateTemp(stagingDir, ".module-*.tar")
	if err != nil {
		return ArtifactMetadata{}, fmt.Errorf("create temporary archive: %w", err)
	}
	temporaryName := temporary.Name()
	if err := temporary.Close(); err != nil {
		return ArtifactMetadata{}, fmt.Errorf("close temporary archive: %w", err)
	}
	defer func() { _ = os.Remove(temporaryName) }()

	if err := writeCanonicalCommittedModule(repositoryRoot, commit, temporaryName, stagingDir); err != nil {
		return ArtifactMetadata{}, err
	}
	digest, size, err := fileDigest(temporaryName)
	if err != nil {
		return ArtifactMetadata{}, err
	}
	metadata := ArtifactMetadata{
		Schema:  "codefly/module-artifact/v2",
		Package: PackageSubject{ID: manifest.ID, Version: manifest.Version},
		Source:  Source{Repository: PackageRepository, Commit: commit},
		Artifact: ArtifactFile{
			Name:      ArchiveName,
			MediaType: corecomposition.ArtifactMediaType,
			Digest:    "sha256:" + digest,
			Size:      size,
		},
	}
	metadataBody, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return ArtifactMetadata{}, fmt.Errorf("encode artifact metadata: %w", err)
	}
	metadataBody = append(metadataBody, '\n')

	archivePath := filepath.Join(stagingDir, ArchiveName)
	if err := os.Rename(temporaryName, archivePath); err != nil {
		return ArtifactMetadata{}, fmt.Errorf("stage canonical archive: %w", err)
	}
	if err := os.WriteFile(filepath.Join(stagingDir, ChecksumName), []byte(digest+"  "+ArchiveName+"\n"), 0o644); err != nil {
		return ArtifactMetadata{}, fmt.Errorf("write archive checksum: %w", err)
	}
	if err := os.WriteFile(filepath.Join(stagingDir, MetadataName), metadataBody, 0o644); err != nil {
		return ArtifactMetadata{}, fmt.Errorf("write artifact metadata: %w", err)
	}
	if err := os.Rename(stagingDir, outputDir); err != nil {
		return ArtifactMetadata{}, fmt.Errorf("publish release directory: %w", err)
	}
	return metadata, nil
}

func writeCanonicalCommittedModule(repositoryRoot, commit, destination, stagingRoot string) error {
	moduleTree, err := gitOutput(repositoryRoot, "rev-parse", "--verify", commit+":module")
	if err != nil {
		return fmt.Errorf("resolve committed module tree: %w", err)
	}
	gitArchive, err := os.CreateTemp(stagingRoot, ".git-module-*.tar")
	if err != nil {
		return fmt.Errorf("create committed module archive: %w", err)
	}
	gitArchivePath := gitArchive.Name()
	if err := gitArchive.Close(); err != nil {
		return fmt.Errorf("close committed module archive: %w", err)
	}
	defer func() { _ = os.Remove(gitArchivePath) }()
	archive := exec.Command("git", "archive", "--format=tar", "--output", gitArchivePath, moduleTree)
	archive.Dir = repositoryRoot
	if output, err := archive.CombinedOutput(); err != nil {
		return fmt.Errorf("archive committed module tree: %w: %s", err, strings.TrimSpace(string(output)))
	}
	archiveBody, err := os.ReadFile(gitArchivePath)
	if err != nil {
		return fmt.Errorf("read committed module archive: %w", err)
	}
	committedTree, err := os.MkdirTemp(stagingRoot, ".committed-module-*")
	if err != nil {
		return fmt.Errorf("create committed module tree: %w", err)
	}
	defer func() { _ = os.RemoveAll(committedTree) }()
	if err := corecomposition.ExtractArchive(context.Background(), archiveBody, committedTree); err != nil {
		return fmt.Errorf("extract committed module tree: %w", err)
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open canonical module archive: %w", err)
	}
	writeErr := corecomposition.WriteCanonicalArchive(committedTree, output)
	closeErr := output.Close()
	if writeErr != nil {
		return fmt.Errorf("write canonical module archive: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close canonical module archive: %w", closeErr)
	}
	return nil
}

type SignOptions struct {
	ModuleRoot        string
	ReleaseDir        string
	Repository        string
	Ref               string
	Commit            string
	SignatureIdentity string
	PrivateKey        []byte
	ExpectedPublicKey []byte
}

// SignRelease writes the exact provenance and detached signature consumed by
// Core. The signature covers the persisted JSON bytes, including the trailing
// newline, so publication cannot accidentally sign a different representation.
func SignRelease(options SignOptions) (*corecomposition.Provenance, error) {
	manifest, err := ReadManifest(options.ModuleRoot)
	if err != nil {
		return nil, err
	}
	repository := options.Repository
	if repository == "" {
		repository = PackageRepository
	}
	identity := options.SignatureIdentity
	if identity == "" {
		identity = ReleaseSignatureIdentity
	}
	if options.Ref != "v"+manifest.Version {
		return nil, fmt.Errorf("provenance ref %q does not match package version %q", options.Ref, manifest.Version)
	}
	if len(options.Commit) != 40 || strings.Trim(options.Commit, "0123456789abcdef") != "" {
		return nil, fmt.Errorf("provenance commit must be a lowercase full SHA")
	}
	privateKey, err := decodePrivateKey(options.PrivateKey)
	if err != nil {
		return nil, err
	}
	expectedPublicKey, err := decodePublicKey(options.ExpectedPublicKey)
	if err != nil {
		return nil, err
	}
	derivedPublicKey := privateKey.Public().(ed25519.PublicKey)
	if !bytes.Equal(derivedPublicKey, expectedPublicKey) {
		return nil, fmt.Errorf("provenance private key does not match the configured Core trust key")
	}
	archivePath := filepath.Join(options.ReleaseDir, ArchiveName)
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		return nil, fmt.Errorf("read release archive: %w", err)
	}
	digest := sha256.Sum256(archive)
	metadataBody, err := os.ReadFile(filepath.Join(options.ReleaseDir, MetadataName))
	if err != nil {
		return nil, fmt.Errorf("read artifact metadata: %w", err)
	}
	metadataDecoder := json.NewDecoder(bytes.NewReader(metadataBody))
	metadataDecoder.DisallowUnknownFields()
	var metadata ArtifactMetadata
	if err := metadataDecoder.Decode(&metadata); err != nil {
		return nil, fmt.Errorf("decode artifact metadata: %w", err)
	}
	if err := requireJSONEnd(metadataDecoder); err != nil {
		return nil, fmt.Errorf("decode artifact metadata: %w", err)
	}
	wantedDigest := fmt.Sprintf("sha256:%x", digest)
	if metadata.Schema != "codefly/module-artifact/v2" ||
		metadata.Package.ID != manifest.ID || metadata.Package.Version != manifest.Version ||
		metadata.Source.Repository != repository || metadata.Source.Commit != options.Commit ||
		metadata.Artifact.Name != ArchiveName || metadata.Artifact.MediaType != corecomposition.ArtifactMediaType ||
		metadata.Artifact.Digest != wantedDigest || metadata.Artifact.Size != int64(len(archive)) {
		return nil, fmt.Errorf("artifact metadata does not describe the release being signed")
	}
	provenance := &corecomposition.Provenance{
		Schema:            corecomposition.ProvenanceSchema,
		Package:           manifest.ID,
		Version:           manifest.Version,
		Repository:        repository,
		Ref:               options.Ref,
		Commit:            options.Commit,
		ArtifactMediaType: corecomposition.ArtifactMediaType,
		ArtifactDigest:    wantedDigest,
		SignatureIdentity: identity,
	}
	body, err := json.MarshalIndent(provenance, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode module provenance: %w", err)
	}
	body = append(body, '\n')
	signature := ed25519.Sign(privateKey, body)
	if !ed25519.Verify(expectedPublicKey, body, signature) {
		return nil, fmt.Errorf("verify provenance signature against configured Core trust key")
	}
	if err := writeNewFile(filepath.Join(options.ReleaseDir, ProvenanceName), body); err != nil {
		return nil, err
	}
	encoded := append([]byte(base64.StdEncoding.EncodeToString(signature)), '\n')
	if err := writeNewFile(filepath.Join(options.ReleaseDir, SignatureName), encoded); err != nil {
		_ = os.Remove(filepath.Join(options.ReleaseDir, ProvenanceName))
		return nil, err
	}
	return provenance, nil
}

func requireJSONEnd(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func decodePrivateKey(encoded []byte) (ed25519.PrivateKey, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		return nil, fmt.Errorf("decode provenance private key: %w", err)
	}
	switch len(decoded) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(decoded), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(decoded), nil
	default:
		return nil, fmt.Errorf("decode provenance private key: got %d bytes, want %d-byte seed or %d-byte key", len(decoded), ed25519.SeedSize, ed25519.PrivateKeySize)
	}
}

func decodePublicKey(encoded []byte) (ed25519.PublicKey, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		return nil, fmt.Errorf("decode provenance public key: %w", err)
	}
	if len(decoded) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("decode provenance public key: got %d bytes, want %d", len(decoded), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(decoded), nil
}

func writeNewFile(path string, body []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create release asset %q: %w", filepath.Base(path), err)
	}
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write release asset %q: %w", filepath.Base(path), err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("close release asset %q: %w", filepath.Base(path), err)
	}
	return nil
}

func fileDigest(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("open archive: %w", err)
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		_ = file.Close()
		return "", 0, fmt.Errorf("hash archive: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", 0, fmt.Errorf("close archive: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func requireCleanCommit(repositoryRoot, commit string) (string, error) {
	if commit == "" {
		return "", fmt.Errorf("commit is required")
	}
	resolved, err := gitOutput(repositoryRoot, "rev-parse", "--verify", commit+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve release commit: %w", err)
	}
	head, err := gitOutput(repositoryRoot, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve HEAD: %w", err)
	}
	if resolved != head {
		return "", fmt.Errorf("release commit %s is not checked out at HEAD %s", resolved, head)
	}
	status, err := gitOutput(repositoryRoot, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return "", fmt.Errorf("inspect worktree: %w", err)
	}
	if status != "" {
		return "", fmt.Errorf("release requires a clean commit; worktree contains changes")
	}
	return resolved, nil
}

func validateGitTree(repositoryRoot, commit string) error {
	command := exec.Command("git", "ls-tree", "-rz", "--full-tree", commit, "--", "module")
	command.Dir = repositoryRoot
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("inspect release tree: %w", err)
	}
	if len(output) == 0 {
		return fmt.Errorf("release commit does not contain the module artifact root")
	}
	for _, record := range bytes.Split(output, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		metadata, path, found := strings.Cut(string(record), "\t")
		fields := strings.Fields(metadata)
		if !found || len(fields) != 3 {
			return fmt.Errorf("release tree contains an invalid entry")
		}
		if fields[0] != "100644" && fields[0] != "100755" {
			return fmt.Errorf("release tree entry %q has unsupported git mode %s", path, fields[0])
		}
	}
	return nil
}

func gitOutput(directory string, arguments ...string) (string, error) {
	command := exec.Command("git", arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func ValidateReleaseRef(manifest Manifest, tag, commit, remoteRefs string) error {
	wantedTag := "v" + manifest.Version
	if tag != wantedTag {
		return fmt.Errorf("release tag %q does not match package version %q", tag, manifest.Version)
	}
	if len(commit) != 40 || strings.Trim(commit, "0123456789abcdef") != "" {
		return fmt.Errorf("release commit must be a lowercase full SHA")
	}
	var tagObject, peeled string
	for _, line := range strings.Split(strings.TrimSpace(remoteRefs), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return fmt.Errorf("remote tag response contains an invalid line")
		}
		switch fields[1] {
		case "refs/tags/" + tag:
			tagObject = fields[0]
		case "refs/tags/" + tag + "^{}":
			peeled = fields[0]
		default:
			return fmt.Errorf("remote tag response contains an unexpected ref %q", fields[1])
		}
	}
	resolved := peeled
	if resolved == "" {
		resolved = tagObject
	}
	if resolved == "" {
		return fmt.Errorf("release tag %q does not exist on the remote", tag)
	}
	if resolved != commit {
		return fmt.Errorf("release tag %q resolves to %s, not release commit %s", tag, resolved, commit)
	}
	return nil
}

// ValidateReleaseAncestry prevents a tag workflow from presenting an
// unreviewed side-branch commit under the trusted main-branch signing identity.
func ValidateReleaseAncestry(repositoryRoot, commit, trustedRef string) error {
	if len(commit) != 40 || strings.Trim(commit, "0123456789abcdef") != "" {
		return fmt.Errorf("release commit must be a lowercase full SHA")
	}
	if trustedRef == "" || strings.HasPrefix(trustedRef, "-") {
		return fmt.Errorf("trusted release ref is invalid")
	}
	resolved, err := gitOutput(repositoryRoot, "rev-parse", "--verify", trustedRef+"^{commit}")
	if err != nil {
		return fmt.Errorf("resolve trusted release ref %q: %w", trustedRef, err)
	}
	command := exec.Command("git", "merge-base", "--is-ancestor", commit, resolved)
	command.Dir = repositoryRoot
	output, err := command.CombinedOutput()
	if err == nil {
		return nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return fmt.Errorf("release commit %s is not reachable from trusted ref %s", commit, trustedRef)
	}
	return fmt.Errorf("verify release ancestry: %w: %s", err, strings.TrimSpace(string(output)))
}

func ValidateImmutableReleaseSettings(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var settings struct {
		Enabled         bool `json:"enabled"`
		EnforcedByOwner bool `json:"enforced_by_owner"`
	}
	if err := decoder.Decode(&settings); err != nil {
		return fmt.Errorf("decode immutable release settings: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("decode immutable release settings: multiple JSON values are not allowed")
		}
		return fmt.Errorf("decode immutable release settings: %w", err)
	}
	if !settings.Enabled {
		return fmt.Errorf("GitHub immutable releases must be enabled before publishing")
	}
	return nil
}
