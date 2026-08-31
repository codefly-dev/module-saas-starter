package main

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Auth-sidecar is independently buildable and may depend on Accounts only
// through the generated Codefly dependency client. A local Go-module replace
// makes native workspace tests pass while breaking isolated/container builds.
func TestAccountsDependencyUsesGeneratedClientOnly(t *testing.T) {
	t.Parallel()

	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	codeDir := filepath.Dir(currentFile)

	goMod, err := os.ReadFile(filepath.Join(codeDir, "go.mod"))
	require.NoError(t, err)
	require.NotContains(t, string(goMod), "replace accounts")
	require.NotContains(t, string(goMod), "accounts v0.0.0")

	fileSet := token.NewFileSet()
	err = filepath.WalkDir(codeDir, func(path string, entry fs.DirEntry, walkErr error) error {
		require.NoError(t, walkErr)
		if entry.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}
		source, parseErr := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
		require.NoError(t, parseErr)
		for _, imported := range source.Imports {
			importPath, unquoteErr := strconv.Unquote(imported.Path.Value)
			require.NoError(t, unquoteErr)
			require.False(
				t,
				importPath == "accounts" || strings.HasPrefix(importPath, "accounts/"),
				"%s imports the Accounts implementation module through %q",
				path,
				importPath,
			)
		}
		return nil
	})
	require.NoError(t, err)

	generatedClient := filepath.Join(
		codeDir,
		"external",
		"saas-starter",
		"accounts",
		"saas-starter_accounts_grpc_grpc.pb.go",
	)
	clientSource, err := os.ReadFile(generatedClient)
	require.NoError(t, err)
	for _, method := range []string{"RefreshToken", "Logout", "GetJWKS"} {
		require.True(
			t,
			strings.Contains(string(clientSource), method),
			"generated Accounts dependency client must expose %s",
			method,
		)
	}
}
