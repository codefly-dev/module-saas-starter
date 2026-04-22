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
)

const (
	sentinelName   = "saas-starter"
	moduleYamlPath = "module.codefly.yaml"
	moduleDirName  = "module"
	sourceEnvVar   = "SAAS_STARTER_MODULE_SRC"
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

	err = copyTree(src, dir, name)
	if err != nil {
		return w.Wrapf(err, "cannot copy module")
	}

	w.Info("module created", wool.Field("name", name), wool.Field("source", src))
	return nil
}

func copyTree(src, dst, name string) error {
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
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, info.Mode().Perm(), rel == moduleYamlPath, name)
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
		data = []byte(strings.Replace(string(data),
			"name: "+sentinelName,
			"name: "+name, 1))
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
