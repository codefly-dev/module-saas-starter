package infra_test

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Only pkg/infra can say what a store transaction is. The binder lives in a
// package internal to pkg/infra, so the Go toolchain refuses to build an import
// of it from pkg/business or anywhere else outside pkg/infra, and the storetx
// package everything else reads through exports nothing but Tx. Business code
// inside a WithControlPlane closure therefore cannot relabel the control-plane
// transaction as a request one, and have As(identity).Within join it as
// app_control_plane with no org bound. A test cannot be a program that fails
// to compile, so this one asks the go command to load such a program and
// requires the refusal. The internal rule still admits every package under
// pkg/infra, so the test also pins the binder's non-test importers to pkg/infra
// and storetx, and refuses any exported pkg/infra function shaped like a
// binder, which would hand the relabelling on to its importers.
func TestOnlyInfraCanBindAStoreTransaction(t *testing.T) {
	root, err := filepath.Abs(filepath.Clean("../.."))
	require.NoError(t, err)
	goCommand, err := exec.LookPath("go")
	require.NoError(t, err, "go test puts the go command on PATH")
	goList := func(t *testing.T, args ...string) string {
		t.Helper()
		cmd := exec.Command(goCommand, append([]string{"list", "-e"}, args...)...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return string(out)
	}

	require.True(t, strings.HasPrefix(txbindImport, "accounts/pkg/infra/internal/"),
		"the binder must sit in an internal directory of pkg/infra, or the toolchain lets anything import it")

	// A file importing the binder, loaded into each package. Outside pkg/infra
	// the go command refuses it; inside, it loads, so the refusal is the
	// internal rule and not a broken probe.
	for pkg, tc := range map[string]struct {
		name    string // the package clause the directory's files use
		refused bool
	}{
		"pkg/business": {"business", true},
		"pkg/auth/pg":  {"pgauth", true},
		"pkg/infra":    {"infra", false},
	} {
		t.Run("bind from "+pkg, func(t *testing.T) {
			dir := t.TempDir()
			name, refused := tc.name, tc.refused
			probe := filepath.Join(dir, "probe.go")
			require.NoError(t, os.WriteFile(probe, []byte("package "+name+"\n\n"+
				"import \""+txbindImport+"\"\n\n"+
				"var _ = txbind.BindRequest\n"), 0o644))
			overlay, err := json.Marshal(map[string]map[string]string{
				"Replace": {filepath.Join(root, pkg, "zz_relabel_probe.go"): probe},
			})
			require.NoError(t, err)
			overlayFile := filepath.Join(dir, "overlay.json")
			require.NoError(t, os.WriteFile(overlayFile, overlay, 0o644))

			out := goList(t, "-overlay", overlayFile, "-f", "{{if .Error}}{{.Error}}{{end}}", "accounts/"+pkg)
			if refused {
				require.Contains(t, out, "use of internal package "+txbindImport+" not allowed")
				return
			}
			require.Empty(t, strings.TrimSpace(out))
		})
	}

	// The internal rule lets any package under pkg/infra import the binder, so
	// a new pkg/infra/<pkg> could hand a wrapper to business code. Under both
	// tag sets, exactly pkg/infra and storetx import it from non-test files; any
	// other importer does so from its tests only. And no exported function of
	// any pkg/infra package takes a transaction and returns a context — the
	// shape of a binder handed on.
	for _, tags := range []string{"", "pure"} {
		require.Equal(t, binderImporters, nonTestBinderImporters(t, root, tags, ""),
			"the non-test importers of the binder changed (tags %q); a new one could re-export it", tags)
		require.Empty(t, binderShapedExports(t, root, tags, ""),
			"a pkg/infra package exports a binder-shaped function (tags %q); it hands relabelling to whoever imports it", tags)
	}
	t.Run("a synthetic re-export is caught", func(t *testing.T) {
		// A non-test file in pkg/infra/auditmetricstest, a package under pkg/infra
		// that imports the binder from its tests alone, handing the binder on.
		dir := t.TempDir()
		probe := filepath.Join(dir, "probe.go")
		require.NoError(t, os.WriteFile(probe, []byte(`package auditmetricstest

import (
	"context"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/infra/internal/txbind"
)

// Tx is the transaction under a name of this package's own.
type Tx = pgx.Tx

func AsRequest(ctx context.Context, tx pgx.Tx, orgID, userID string) context.Context {
	return txbind.BindRequest(ctx, tx, orgID, userID)
}

var AsControlPlane = txbind.BindControlPlane

type Relabeller struct{}

func (Relabeller) AsWorker(ctx context.Context, tx Tx) context.Context { return txbind.BindWorker(ctx, tx) }

func Binder() func(context.Context, pgx.Tx) context.Context { return txbind.BindControlPlane }
`), 0o644))
		overlay, err := json.Marshal(map[string]map[string]string{
			"Replace": {filepath.Join(root, "pkg/infra/auditmetricstest", "zz_reexport_probe.go"): probe},
		})
		require.NoError(t, err)
		overlayFile := filepath.Join(dir, "overlay.json")
		require.NoError(t, os.WriteFile(overlayFile, overlay, 0o644))

		require.ElementsMatch(t, append(slices.Clone(binderImporters), "accounts/pkg/infra/auditmetricstest"),
			nonTestBinderImporters(t, root, "", overlayFile))
		require.Equal(t, []string{
			"accounts/pkg/infra/auditmetricstest.AsControlPlane",
			"accounts/pkg/infra/auditmetricstest.AsRequest",
			"accounts/pkg/infra/auditmetricstest.Binder",
			"accounts/pkg/infra/auditmetricstest.Relabeller.AsWorker",
		}, binderShapedExports(t, root, "", overlayFile))
	})

	// storetx is the façade the rest of the module reads through: it binds
	// nothing, and exposes the transaction and nothing else.
	fset := token.NewFileSet()
	var exported []string
	entries, err := os.ReadDir("storetx")
	require.NoError(t, err)
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join("storetx", entry.Name()), nil, parser.SkipObjectResolution)
		require.NoError(t, err)
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Name.IsExported() {
					exported = append(exported, d.Name.Name)
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if s.Name.IsExported() {
							exported = append(exported, s.Name.Name)
						}
					case *ast.ValueSpec:
						for _, n := range s.Names {
							if n.IsExported() {
								exported = append(exported, n.Name)
							}
						}
					}
				}
			}
		}
	}
	require.Equal(t, []string{"Tx"}, exported, "storetx must stay read-only")
}

// binderImporters are the packages whose non-test files import the binder:
// pkg/infra, which opens and binds transactions, and storetx, which reads them
// back.
var binderImporters = []string{"accounts/pkg/infra", "accounts/pkg/infra/storetx"}

// goListPackages runs go list -e with args in root, under the build tags and,
// unless empty, the overlay given, and decodes the JSON value it prints for
// each package.
func goListPackages[P any](t *testing.T, root, tags, overlay string, args ...string) []P {
	t.Helper()
	goCommand, err := exec.LookPath("go")
	require.NoError(t, err, "go test puts the go command on PATH")
	list := []string{"list", "-e", "-tags=" + tags}
	if overlay != "" {
		list = append(list, "-overlay", overlay)
	}
	cmd := exec.Command(goCommand, append(list, args...)...)
	cmd.Dir = root
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, stderr.String())
	var packages []P
	decoder := json.NewDecoder(strings.NewReader(string(out)))
	for decoder.More() {
		var pkg P
		require.NoError(t, decoder.Decode(&pkg))
		packages = append(packages, pkg)
	}
	return packages
}

// nonTestBinderImporters returns, sorted, every package in the module whose
// non-test files import the binder.
func nonTestBinderImporters(t *testing.T, root, tags, overlay string) []string {
	t.Helper()
	var importers []string
	for _, pkg := range goListPackages[struct {
		ImportPath string
		Imports    []string
	}](t, root, tags, overlay, "-json=ImportPath,Imports", "./...") {
		if slices.Contains(pkg.Imports, txbindImport) {
			importers = append(importers, pkg.ImportPath)
		}
	}
	slices.Sort(importers)
	return importers
}

// binderShapedExports returns, sorted, every exported function, method or
// function-valued variable in pkg/infra and the packages below it — the
// binder's own package aside — whose signature takes a pgx.Tx and returns a
// context.Context, or returns a function that does. It reads the packages'
// compiled export data, so an alias, a variable holding a binder, or a method
// counts as surely as a declared function.
func binderShapedExports(t *testing.T, root, tags, overlay string) []string {
	t.Helper()
	exports := map[string]string{}
	var targets []string
	for _, pkg := range goListPackages[struct {
		ImportPath string
		Export     string
		GoFiles    []string
		Error      *struct{ Err string }
	}](t, root, tags, overlay, "-export", "-deps", "-json=ImportPath,Export,GoFiles,Error", "./pkg/infra/...") {
		require.Nilf(t, pkg.Error, "%s: %v", pkg.ImportPath, pkg.Error)
		exports[pkg.ImportPath] = pkg.Export
		if (pkg.ImportPath == "accounts/pkg/infra" || strings.HasPrefix(pkg.ImportPath, "accounts/pkg/infra/")) &&
			pkg.ImportPath != txbindImport && len(pkg.GoFiles) > 0 {
			targets = append(targets, pkg.ImportPath)
		}
	}
	require.Contains(t, targets, "accounts/pkg/infra", "the export scan found no pkg/infra package; it is broken")

	var binderShaped func(*types.Signature) bool
	binderShaped = func(sig *types.Signature) bool {
		takesTx, returnsContext := false, false
		for param := range sig.Params().Variables() {
			takesTx = takesTx || types.TypeString(types.Unalias(param.Type()), nil) == "github.com/jackc/pgx/v5.Tx"
		}
		for result := range sig.Results().Variables() {
			typ := types.Unalias(result.Type())
			returnsContext = returnsContext || types.TypeString(typ, nil) == "context.Context"
			if fn, ok := typ.Underlying().(*types.Signature); ok && binderShaped(fn) {
				return true
			}
		}
		return takesTx && returnsContext
	}
	imported := importer.ForCompiler(token.NewFileSet(), "gc", func(path string) (io.ReadCloser, error) {
		file, ok := exports[path]
		if !ok || file == "" {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(file)
	})
	var found []string
	for _, path := range targets {
		pkg, err := imported.Import(path)
		require.NoError(t, err, path)
		for _, name := range pkg.Scope().Names() {
			object := pkg.Scope().Lookup(name)
			if sig, ok := object.Type().Underlying().(*types.Signature); ok && object.Exported() && binderShaped(sig) {
				// A function, or a variable holding one.
				if _, isType := object.(*types.TypeName); !isType {
					found = append(found, path+"."+name)
				}
			}
			if _, isType := object.(*types.TypeName); !isType {
				continue
			}
			methods := types.NewMethodSet(types.NewPointer(object.Type()))
			for method := range methods.Methods() {
				if sig, ok := method.Type().(*types.Signature); ok && method.Obj().Exported() && binderShaped(sig) {
					found = append(found, path+"."+name+"."+method.Obj().Name())
				}
			}
		}
	}
	slices.Sort(found)
	return found
}

// The call-site walk records a site however it is spelled: taking System or a
// binder as a function value and calling it under another name is recorded
// where the value is taken, a System identity built inside pkg/business
// without calling System is recorded where it is built, and a dot-import,
// which would leave only a bare name to find, fails the walk instead.
func TestControlPlaneWalkSeesFunctionValuesAndRefusesDotImports(t *testing.T) {
	parse := func(t *testing.T, src string) *ast.File {
		t.Helper()
		file, err := parser.ParseFile(token.NewFileSet(), "synthetic.go", src, parser.SkipObjectResolution)
		require.NoError(t, err)
		return file
	}
	for name, tc := range map[string]struct {
		src  string
		want []string
	}{
		"System taken as a value outside business": {
			src: `package example

import "accounts/pkg/business"

func relabel(s store) {
	system := business.System
	s.As(system())
}`,
			want: []string{"relabel: System (value)"},
		},
		"System taken as a value inside business": {
			src: `package business

func System() Identity { return Identity{system: true} }

var system = System

func use(s Store) {
	s.As(System())
	_ = System()
}`,
			want: []string{"(package level): System (value)", "use: As(System())", "use: System()"},
		},
		"a binder taken as a value": {
			src: `package infra

import bind "accounts/pkg/infra/internal/txbind"

func relabel(ctx context.Context, tx pgx.Tx) context.Context {
	asRequest := bind.BindRequest
	_ = bind.BindWorker(ctx, tx)
	return asRequest(ctx, tx, "", "")
}`,
			want: []string{"relabel: txbind.BindRequest (value)", "relabel: txbind.BindWorker"},
		},
		"a System identity built without System": {
			src: `package business

func System() Identity { return Identity{system: true} }

var root = &Identity{system: true}

func escalate() Identity { return Identity{OrgID: "", system: true} }

func listed() []Identity { return []Identity{{system: true}} }

func keyed() map[string]*Identity { return map[string]*Identity{"root": {system: true}} }

type Root = Identity

func aliased() Root { return Root{system: true} }

func positional() Identity { return Identity{"", "", "", true} }

func elidedPositional() []Identity { return []Identity{{"", "", "", true}} }

func assigned(id Identity) Identity {
	id.system = true
	return id
}

func addressed(id *Identity) {
	flag := &id.system
	*flag = true
}

func (Identity) System() Identity { return Identity{system: true} }

func reads(id Identity) bool { return id.system }

func unrelated() map[bool]int { system := true; return map[bool]int{system: 1} }`,
			want: []string{
				"(package level): Identity{system: …}",
				"escalate: Identity{system: …}",
				"listed: Identity{system: …}",
				"keyed: Identity{system: …}",
				"aliased: Identity{system: …}",
				"positional: Identity{…} (positional)",
				"elidedPositional: Identity{…} (positional)",
				"assigned: .system = …",
				"addressed: &.system",
				"(Identity).System: Identity{system: …}",
			},
		},
		"WithControlPlane taken as a value": {
			src: `package example

func escalate(s Store) {
	run := s.WithControlPlane
	_ = run
}`,
			want: []string{"escalate: WithControlPlane"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			sites, err := controlPlaneSitesIn(parse(t, tc.src))
			require.NoError(t, err)
			require.ElementsMatch(t, tc.want, sites)
		})
	}
	for _, path := range []string{businessImport, storetxImport, txbindImport} {
		t.Run("dot-import of "+path, func(t *testing.T) {
			_, err := controlPlaneSitesIn(parse(t, "package example\n\nimport . \""+path+"\"\n\nfunc f() { _ = Tx }\n"))
			require.ErrorContains(t, err, "dot-import of "+path)
		})
	}
}
