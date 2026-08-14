package modulepackage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	ArchiveName  = "module.tar"
	ChecksumName = "module.tar.sha256"
	MetadataName = "artifact.json"
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
	if err := os.MkdirAll(options.OutputDir, 0o755); err != nil {
		return ArtifactMetadata{}, fmt.Errorf("create release directory: %w", err)
	}
	if err := validateGitTree(repositoryRoot, commit); err != nil {
		return ArtifactMetadata{}, err
	}
	for _, name := range []string{ArchiveName, ChecksumName, MetadataName} {
		if _, err := os.Lstat(filepath.Join(options.OutputDir, name)); err == nil {
			return ArtifactMetadata{}, fmt.Errorf("refusing to replace existing release asset %s", name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return ArtifactMetadata{}, fmt.Errorf("inspect release asset %s: %w", name, err)
		}
	}

	temporary, err := os.CreateTemp(options.OutputDir, ".module-*.tar")
	if err != nil {
		return ArtifactMetadata{}, fmt.Errorf("create temporary archive: %w", err)
	}
	temporaryName := temporary.Name()
	if err := temporary.Close(); err != nil {
		return ArtifactMetadata{}, fmt.Errorf("close temporary archive: %w", err)
	}
	defer os.Remove(temporaryName)

	archive := exec.Command("git", "archive", "--format=tar", "--output", temporaryName, commit, "--", "module")
	archive.Dir = repositoryRoot
	if output, err := archive.CombinedOutput(); err != nil {
		return ArtifactMetadata{}, fmt.Errorf("create canonical archive: %w: %s", err, strings.TrimSpace(string(output)))
	}
	digest, size, err := fileDigest(temporaryName)
	if err != nil {
		return ArtifactMetadata{}, err
	}
	metadata := ArtifactMetadata{
		Schema:  "codefly/module-artifact/v2",
		Package: PackageSubject{ID: manifest.ID, Version: manifest.Version},
		Source:  Source{Repository: manifest.Repository, Commit: commit},
		Artifact: ArtifactFile{
			Name:      ArchiveName,
			MediaType: manifest.Artifact.MediaType,
			Digest:    "sha256:" + digest,
			Size:      size,
		},
	}
	metadataBody, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return ArtifactMetadata{}, fmt.Errorf("encode artifact metadata: %w", err)
	}
	metadataBody = append(metadataBody, '\n')

	archivePath := filepath.Join(options.OutputDir, ArchiveName)
	if err := os.Link(temporaryName, archivePath); err != nil {
		return ArtifactMetadata{}, fmt.Errorf("publish canonical archive: %w", err)
	}
	if err := os.Remove(temporaryName); err != nil {
		return ArtifactMetadata{}, fmt.Errorf("remove temporary archive: %w", err)
	}
	if err := os.WriteFile(filepath.Join(options.OutputDir, ChecksumName), []byte(digest+"  "+ArchiveName+"\n"), 0o644); err != nil {
		return ArtifactMetadata{}, fmt.Errorf("write archive checksum: %w", err)
	}
	if err := os.WriteFile(filepath.Join(options.OutputDir, MetadataName), metadataBody, 0o644); err != nil {
		return ArtifactMetadata{}, fmt.Errorf("write artifact metadata: %w", err)
	}
	return metadata, nil
}

func fileDigest(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("open archive: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, fmt.Errorf("hash archive: %w", err)
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
