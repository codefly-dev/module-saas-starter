package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/codefly-dev/core/shared"
	"github.com/codefly-dev/core/wool"
	"gopkg.in/yaml.v3"
)

const (
	moduleYamlPath    = "module.codefly.yaml"
	moduleDirName     = "module"
	sourceEnvVar      = "SAAS_STARTER_MODULE_SRC"
	gitOpsRelativeDir = "deployment/kustomize"
	workspaceYamlPath = "workspace.codefly.yaml"
)

// resolveSource finds the module/ directory to copy from.
// Priority: SAAS_STARTER_MODULE_SRC env var → <executable-dir>/module.
func resolveSource() (string, error) {
	if src := os.Getenv(sourceEnvVar); src != "" {
		return src, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot resolve executable path: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("cannot eval executable symlinks: %w", err)
	}
	return filepath.Join(filepath.Dir(exe), moduleDirName), nil
}

func Create(ctx context.Context, dir, name string) error {
	w := wool.Get(ctx).In("saas-starter::Create")

	src, err := resolveSource()
	if err != nil {
		return w.Wrapf(err, "cannot resolve source")
	}
	if _, err := os.Stat(filepath.Join(src, moduleYamlPath)); err != nil {
		return w.Wrapf(err, "source %q is not a module directory (missing %s)", src, moduleYamlPath)
	}

	if _, err := shared.CheckDirectoryOrCreate(ctx, dir); err != nil {
		return w.Wrapf(err, "cannot create target directory")
	}

	workspaceRoot, err := findWorkspaceRoot(dir)
	if err != nil {
		return w.Wrapf(err, "cannot find workspace")
	}
	workspace, err := loadWorkspaceManifest(workspaceRoot)
	if err != nil {
		return w.Wrapf(err, "cannot load workspace")
	}

	parent := filepath.Dir(dir)
	stage, err := os.MkdirTemp(parent, "."+filepath.Base(dir)+"-stage-*")
	if err != nil {
		return w.Wrapf(err, "cannot create module staging directory")
	}
	defer os.RemoveAll(stage)

	if err := copyTree(dir, stage, "", false); err != nil {
		return w.Wrapf(err, "cannot stage existing module scaffold")
	}
	if err := copyTree(src, stage, name, true); err != nil {
		return w.Wrapf(err, "cannot stage module source")
	}
	if err := generateGitOps(ctx, stage, workspace); err != nil {
		return w.Wrapf(err, "cannot generate GitOps manifests")
	}

	backup := stage + "-previous"
	if err := os.Rename(dir, backup); err != nil {
		return w.Wrapf(err, "cannot preserve existing module scaffold")
	}
	if err := os.Rename(stage, dir); err != nil {
		if restoreErr := os.Rename(backup, dir); restoreErr != nil {
			return w.Wrapf(err, "cannot install generated module; cannot restore scaffold: %v", restoreErr)
		}
		return w.Wrapf(err, "cannot install generated module")
	}
	if err := os.RemoveAll(backup); err != nil {
		return w.Wrapf(err, "cannot remove replaced module scaffold")
	}

	w.Info("module created", wool.Field("name", name), wool.Field("source", src))
	return nil
}

func findWorkspaceRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, workspaceYamlPath)); err == nil {
			return dir, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("%s not found above %s", workspaceYamlPath, start)
		}
		dir = parent
	}
}

func copyTree(src, dst, name string, skipGitOps bool) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if skipGitOps && (rel == gitOpsRelativeDir ||
			strings.HasPrefix(rel, gitOpsRelativeDir+string(filepath.Separator))) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, info.Mode().Perm(), rel == moduleYamlPath && name != "", name)
	})
}

func copyFile(srcPath, dstPath string, mode os.FileMode, rewriteName bool, name string) error {
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return err
	}
	if rewriteName {
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return err
		}
		var document yaml.Node
		if err := yaml.Unmarshal(data, &document); err != nil {
			return fmt.Errorf("parse %s: %w", moduleYamlPath, err)
		}
		if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
			return fmt.Errorf("%s must contain one mapping", moduleYamlPath)
		}
		mapping := document.Content[0]
		found := false
		for index := 0; index < len(mapping.Content); index += 2 {
			if mapping.Content[index].Value == "name" {
				mapping.Content[index+1].Value = name
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%s has no module name", moduleYamlPath)
		}
		data, err = yaml.Marshal(&document)
		if err != nil {
			return fmt.Errorf("render %s: %w", moduleYamlPath, err)
		}
		return os.WriteFile(dstPath, data, mode)
	}
	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: saas-starter <dir> <name>\n")
		os.Exit(1)
	}
	dir := strings.TrimSpace(os.Args[1])
	name := strings.TrimSpace(os.Args[2])
	if dir == "" || name == "" {
		fmt.Fprintf(os.Stderr, "Error: dir and name must be non-empty\n")
		os.Exit(1)
	}
	if err := Create(context.Background(), dir, name); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
