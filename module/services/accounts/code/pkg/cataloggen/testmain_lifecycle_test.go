package cataloggen_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestDependencyTestMainCleanupCannotBeSkippedByOSExit guards a subtle Go
// lifecycle trap: deferred cleanup does not run when the same TestMain calls
// os.Exit. Dependency-owning setup must live in a helper that returns an exit
// code; the outer TestMain may then call os.Exit after the helper's defers ran.
func TestDependencyTestMainCleanupCannotBeSkippedByOSExit(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve lifecycle test source path")
	}
	moduleRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "../../../../.."))
	fileSet := token.NewFileSet()

	err := filepath.WalkDir(moduleRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".next", "node_modules", "dist", "build":
				return filepath.SkipDir
			default:
				return nil
			}
		}
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}

		document, parseErr := parser.ParseFile(fileSet, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		for _, declaration := range document.Decls {
			function, isFunction := declaration.(*ast.FuncDecl)
			if !isFunction || function.Name.Name != "TestMain" || function.Body == nil {
				continue
			}
			hasDependencies := false
			hasExit := false
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, isCall := node.(*ast.CallExpr)
				if !isCall {
					return true
				}
				selector, isSelector := call.Fun.(*ast.SelectorExpr)
				if !isSelector {
					return true
				}
				switch selector.Sel.Name {
				case "WithDependencies":
					hasDependencies = true
				case "Exit":
					if identifier, ok := selector.X.(*ast.Ident); ok && identifier.Name == "os" {
						hasExit = true
					}
				}
				return true
			})
			if hasDependencies && hasExit {
				relative, _ := filepath.Rel(moduleRoot, path)
				t.Errorf(
					"%s: TestMain owns Codefly dependencies and calls os.Exit; move setup into a helper returning m.Run() so cleanup defers execute",
					filepath.ToSlash(relative),
				)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan module TestMain lifecycle: %v", err)
	}
}
