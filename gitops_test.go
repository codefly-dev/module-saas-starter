package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const fixtureRevision = "0123456789abcdef0123456789abcdef01234567"

func TestGenerateGitOpsGoldenShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		workspace string
		module    string
		services  []string
		golden    string
	}{
		{
			name:      "warden",
			workspace: "warden-control",
			module:    "identity",
			services:  []string{"accounts", "auth-sidecar", "cache", "frontend", "object-storage", "store", "vault"},
			golden:    "testdata/warden-inventory.golden.json",
		},
		{
			name:      "mind",
			workspace: "mind-control",
			module:    "users",
			services:  []string{"store", "vault", "accounts", "cache", "frontend", "forge-edge", "object-storage"},
			golden:    "testdata/mind-inventory.golden.json",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root, moduleDir := writeGitOpsFixture(t, test.workspace, test.module, test.services)
			workspace, err := loadWorkspaceManifest(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := generateGitOps(context.Background(), moduleDir, workspace); err != nil {
				t.Fatal(err)
			}

			got, err := os.ReadFile(filepath.Join(moduleDir, filepath.FromSlash(gitOpsRelativeDir), "inventory.json"))
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(test.golden)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Fatalf("inventory golden mismatch\nwant:\n%s\ngot:\n%s", want, got)
			}

			assertEnvironmentApplications(t, moduleDir, "local", test.services)
			awsServices := make([]string, 0, len(test.services))
			for _, service := range test.services {
				if _, managed := awsManagedServices[service]; !managed {
					awsServices = append(awsServices, service)
				}
			}
			assertEnvironmentApplications(t, moduleDir, "aws", awsServices)
		})
	}
}

func TestGenerateGitOpsRejectsHostileContracts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(t *testing.T, root, moduleDir string, workspace *workspaceManifest)
		want   string
	}{
		{
			name: "missing gitops contract",
			mutate: func(_ *testing.T, _, _ string, workspace *workspaceManifest) {
				workspace.Gitops = nil
			},
			want: "must declare gitops",
		},
		{
			name: "placeholder repository",
			mutate: func(_ *testing.T, _, _ string, workspace *workspaceManifest) {
				workspace.Gitops.RepoURL = "REPLACE_ME"
			},
			want: "exact repository URL",
		},
		{
			name: "repository credentials",
			mutate: func(_ *testing.T, _, _ string, workspace *workspaceManifest) {
				workspace.Gitops.RepoURL = "https://token@github.com/acme/platform.git"
			},
			want: "must not contain credentials",
		},
		{
			name: "repository query credentials",
			mutate: func(_ *testing.T, _, _ string, workspace *workspaceManifest) {
				workspace.Gitops.RepoURL = "https://github.com/acme/platform.git?token=secret"
			},
			want: "exact HTTPS or SSH repository URL",
		},
		{
			name: "SCP repository query credentials",
			mutate: func(_ *testing.T, _, _ string, workspace *workspaceManifest) {
				workspace.Gitops.RepoURL = "git@github.com:acme/platform.git?token=secret"
			},
			want: "exact HTTPS or SSH repository URL",
		},
		{
			name: "insecure repository transport",
			mutate: func(_ *testing.T, _, _ string, workspace *workspaceManifest) {
				workspace.Gitops.RepoURL = "http://github.com/acme/platform.git"
			},
			want: "exact HTTPS or SSH repository URL",
		},
		{
			name: "escaping owned path",
			mutate: func(_ *testing.T, _, _ string, workspace *workspaceManifest) {
				workspace.Gitops.Path = "../other"
			},
			want: "canonical relative repository path",
		},
		{
			name: "mutable production revision",
			mutate: func(_ *testing.T, _, _ string, workspace *workspaceManifest) {
				workspace.Gitops.Branch = "main"
				workspace.Environments = workspace.Environments[1:]
			},
			want: "full commit SHA or a signed tag",
		},
		{
			name: "missing namespace",
			mutate: func(_ *testing.T, _, _ string, workspace *workspaceManifest) {
				workspace.Environments[1].Namespace = ""
			},
			want: "Kubernetes DNS label",
		},
		{
			name: "unsupported remote cluster",
			mutate: func(_ *testing.T, _, _ string, workspace *workspaceManifest) {
				workspace.Environments[1].Cluster.Kind = "gke"
			},
			want: "is not supported",
		},
		{
			name: "missing service path",
			mutate: func(t *testing.T, _, moduleDir string, _ *workspaceManifest) {
				t.Helper()
				if err := os.Remove(filepath.Join(moduleDir, "services", "frontend")); err != nil {
					t.Fatal(err)
				}
			},
			want: "declared service \"frontend\" path",
		},
		{
			name: "extra service path",
			mutate: func(t *testing.T, _, moduleDir string, _ *workspaceManifest) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(moduleDir, "services", "phantom"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: "has no module service declaration",
		},
		{
			name: "duplicate service",
			mutate: func(t *testing.T, _, moduleDir string, _ *workspaceManifest) {
				t.Helper()
				writeTestFile(t, filepath.Join(moduleDir, moduleYamlPath), `kind: module
name: identity
services:
  - name: accounts
  - name: accounts
`)
			},
			want: "more than once",
		},
		{
			name: "duplicate service path",
			mutate: func(t *testing.T, _, moduleDir string, _ *workspaceManifest) {
				t.Helper()
				writeTestFile(t, filepath.Join(moduleDir, moduleYamlPath), `kind: module
name: identity
services:
  - name: accounts
    path: accounts
  - name: frontend
    path: accounts
`)
			},
			want: "declare the same path",
		},
		{
			name: "symlinked service path",
			mutate: func(t *testing.T, _, moduleDir string, _ *workspaceManifest) {
				t.Helper()
				if err := os.Remove(filepath.Join(moduleDir, "services", "accounts")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), filepath.Join(moduleDir, "services", "accounts")); err != nil {
					t.Fatal(err)
				}
			},
			want: "is not a directory",
		},
		{
			name: "service path below symlink",
			mutate: func(t *testing.T, _, moduleDir string, _ *workspaceManifest) {
				t.Helper()
				external := t.TempDir()
				if err := os.Mkdir(filepath.Join(external, "accounts"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.RemoveAll(filepath.Join(moduleDir, "services")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(moduleDir, "services"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(external, filepath.Join(moduleDir, "services", "external")); err != nil {
					t.Fatal(err)
				}
				writeTestFile(t, filepath.Join(moduleDir, moduleYamlPath), `kind: module
name: identity
services:
  - name: accounts
    path: external/accounts
`)
			},
			want: "is not a directory",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root, moduleDir := writeGitOpsFixture(t, "hostile-control", "identity", []string{"accounts", "frontend"})
			workspace, err := loadWorkspaceManifest(root)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, root, moduleDir, workspace)
			err = generateGitOps(context.Background(), moduleDir, workspace)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("generateGitOps() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestGenerateGitOpsReplacesStaleSecretBearingTree(t *testing.T) {
	t.Parallel()
	root, moduleDir := writeGitOpsFixture(t, "safe-control", "identity", []string{"accounts"})
	staleRoot := filepath.Join(moduleDir, filepath.FromSlash(gitOpsRelativeDir))
	writeTestFile(t, filepath.Join(staleRoot, "overlays", "aws", "secret.yaml"), `apiVersion: v1
kind: Secret
metadata:
  name: leaked
stringData:
  password: hostile-canary
`)
	workspace, err := loadWorkspaceManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := generateGitOps(context.Background(), moduleDir, workspace); err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(staleRoot, func(file string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), "hostile-canary") || strings.Contains(string(data), "kind: Secret") {
			t.Fatalf("secret material survived in %s", file)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReplaceGeneratedTreeRestoresPreviousTreeOnInstallFailure(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	root := filepath.Join(parent, "kustomize")
	writeTestFile(t, filepath.Join(root, "inventory.json"), "previous\n")

	err := replaceGeneratedTree(filepath.Join(parent, "missing-stage"), root)
	if err == nil || !strings.Contains(err.Error(), "install generated GitOps directory") {
		t.Fatalf("replaceGeneratedTree() error = %v", err)
	}
	data, readErr := os.ReadFile(filepath.Join(root, "inventory.json"))
	if readErr != nil {
		t.Fatalf("previous tree was not restored: %v", readErr)
	}
	if string(data) != "previous\n" {
		t.Fatalf("restored inventory = %q", data)
	}
}

func TestLocalRevisionBindsCheckedOutHarnessSnapshot(t *testing.T) {
	t.Parallel()
	root, moduleDir := writeGitOpsFixture(t, "local-control", "identity", []string{"accounts"})
	runGit(t, root, "init", "-b", "main")
	runGit(t, root, "config", "user.name", "Codefly Test")
	runGit(t, root, "config", "user.email", "test@codefly.dev")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "snapshot")
	head := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))

	workspace, err := loadWorkspaceManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	workspace.Gitops.Branch = "main"
	workspace.Environments = workspace.Environments[:1]
	if err := generateGitOps(context.Background(), moduleDir, workspace); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(moduleDir, filepath.FromSlash(gitOpsRelativeDir), "inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	var inventory gitOpsInventory
	if err := json.Unmarshal(data, &inventory); err != nil {
		t.Fatal(err)
	}
	if inventory.Environments[0].Revision != head {
		t.Fatalf("local revision = %s, want harness snapshot %s", inventory.Environments[0].Revision, head)
	}
}

func TestProductionSignedTagResolvesImmutableCommit(t *testing.T) {
	t.Parallel()
	root, moduleDir := writeGitOpsFixture(t, "production-control", "identity", []string{"accounts"})
	runGit(t, root, "init", "-b", "main")
	runGit(t, root, "config", "user.name", "Codefly Test")
	runGit(t, root, "config", "user.email", "test@codefly.dev")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "release")
	head := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))

	signingKey := filepath.Join(root, "test-signing-key")
	command := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", signingKey)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generate signing key: %v\n%s", err, output)
	}
	publicKey, err := os.ReadFile(signingKey + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	allowedSigners := filepath.Join(root, "allowed-signers")
	writeTestFile(t, allowedSigners, "test@codefly.dev "+string(publicKey))
	runGit(t, root, "config", "gpg.format", "ssh")
	runGit(t, root, "config", "gpg.ssh.program", "ssh-keygen")
	runGit(t, root, "config", "user.signingkey", signingKey)
	runGit(t, root, "config", "gpg.ssh.allowedSignersFile", allowedSigners)
	runGit(t, root, "tag", "-s", "v1.0.0", "-m", "signed release")

	workspace, err := loadWorkspaceManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	workspace.Gitops.Branch = "refs/tags/v1.0.0"
	workspace.Environments = workspace.Environments[1:]
	if err := generateGitOps(context.Background(), moduleDir, workspace); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(moduleDir, filepath.FromSlash(gitOpsRelativeDir), "inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	var inventory gitOpsInventory
	if err := json.Unmarshal(data, &inventory); err != nil {
		t.Fatal(err)
	}
	if inventory.Environments[0].Revision != head {
		t.Fatalf("production revision = %s, want signed tag commit %s", inventory.Environments[0].Revision, head)
	}
}

func TestCreateDoesNotCopyPlaceholderGitOpsTree(t *testing.T) {
	root, target := writeGitOpsFixture(t, "create-control", "identity", []string{"accounts"})
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, moduleYamlPath), `kind: module
name: saas-starter
services:
  - name: accounts
`)
	if err := os.MkdirAll(filepath.Join(source, "services", "accounts"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(source, filepath.FromSlash(gitOpsRelativeDir), "overlays", "local", "application.yaml"), `repoURL: REPLACE_ME
targetRevision: main
path: deployments/modules/saas-starter/services/accounts/overlays/local
`)
	t.Setenv(sourceEnvVar, source)

	if err := Create(context.Background(), target, "identity"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(target, moduleYamlPath))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "name: saas-starter") || !strings.Contains(string(data), "name: identity") {
		t.Fatalf("module name was not structurally rewritten:\n%s", data)
	}
	gitOpsRoot := filepath.Join(target, filepath.FromSlash(gitOpsRelativeDir))
	if _, err := os.Stat(filepath.Join(gitOpsRoot, "overlays", "local", "application.yaml")); !os.IsNotExist(err) {
		t.Fatalf("placeholder Application was copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, workspaceYamlPath)); err != nil {
		t.Fatal(err)
	}
}

func writeGitOpsFixture(t *testing.T, workspaceName, moduleName string, services []string) (string, string) {
	t.Helper()
	root := t.TempDir()
	moduleDir := filepath.Join(root, "modules", moduleName)
	var module strings.Builder
	module.WriteString("kind: module\nname: " + moduleName + "\nservices:\n")
	for _, service := range services {
		module.WriteString("  - name: " + service + "\n")
		if err := os.MkdirAll(filepath.Join(moduleDir, "services", service), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, filepath.Join(moduleDir, moduleYamlPath), module.String())
	writeTestFile(t, filepath.Join(root, workspaceYamlPath), "name: "+workspaceName+`
layout: modules
gitops:
  repo-url: git@github.com:acme/platform-config.git
  path: clusters/codefly
  branch: `+fixtureRevision+`
environments:
  - name: local
    cluster:
      kind: k3d
    namespace: `+moduleName+`-local
  - name: aws
    cluster:
      kind: eks
    namespace: `+moduleName+`-aws
`)
	return root, moduleDir
}

func assertEnvironmentApplications(t *testing.T, moduleDir, environment string, want []string) {
	t.Helper()
	root := filepath.Join(moduleDir, filepath.FromSlash(gitOpsRelativeDir), "overlays", environment)
	kubectl, err := exec.LookPath("kubectl")
	if err != nil {
		t.Fatalf("kubectl is required to prove the generated Kustomize overlay renders: %v", err)
	}
	command := exec.Command(kubectl, "kustomize", root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("kubectl kustomize %s: %v\n%s", environment, err, output)
	}
	objects, err := renderKustomization(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, object := range objects {
		if object["kind"] != "Application" {
			continue
		}
		metadata := object["metadata"].(map[string]any)
		labels := metadata["labels"].(map[string]any)
		got = append(got, labels["codefly.dev/service"].(string))
	}
	slices.Sort(got)
	want = append([]string(nil), want...)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("%s Applications = %v, want %v", environment, got, want)
	}
}

func writeTestFile(t *testing.T, file, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
