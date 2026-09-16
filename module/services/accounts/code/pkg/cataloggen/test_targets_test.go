package cataloggen_test

import (
	"encoding/json"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestAccountsPureTargetExcludesDependencyHarnesses(t *testing.T) {
	codeRoot := filepath.Clean("../..")
	pure := build.Default
	pure.BuildTags = []string{"pure"}
	for _, path := range []string{
		"pkg/business/service_test.go",
		"pkg/infra/postgres_webhooks_test.go",
		"pkg/auth/pg/session_store_test.go",
		"pkg/billing/pg/store_test.go",
	} {
		dir, file := filepath.Split(filepath.Join(codeRoot, path))
		included, err := pure.MatchFile(dir, file)
		require.NoError(t, err)
		require.False(t, included, path)
		included, err = build.Default.MatchFile(dir, file)
		require.NoError(t, err)
		require.True(t, included, "default gate must retain %s", path)
	}
	for _, path := range []string{
		"pkg/business/audit_durability_gate_test.go",
		"pkg/business/audit_actor_type_gate_test.go",
		"pkg/business/audit_registry_test.go",
		"pkg/infra/zz_rls_test_guard_test.go",
	} {
		dir, file := filepath.Split(filepath.Join(codeRoot, path))
		included, err := pure.MatchFile(dir, file)
		require.NoError(t, err)
		require.True(t, included, path)
	}
}

func TestAccountsTargetMetadataRetainsConservativeInputs(t *testing.T) {
	data, err := os.ReadFile("../../../test-targets.json")
	require.NoError(t, err)
	var catalog struct {
		SchemaVersion int    `json:"schema_version"`
		Fallback      string `json:"fallback"`
		Targets       []struct {
			Suite           string   `json:"suite"`
			Complete        bool     `json:"complete"`
			Command         []string `json:"command"`
			RuntimeServices []string `json:"runtime_services"`
			InputRoots      []string `json:"input_roots"`
		} `json:"targets"`
	}
	require.NoError(t, json.Unmarshal(data, &catalog))
	require.Equal(t, 1, catalog.SchemaVersion)
	require.Equal(t, "whole-service-and-dependency-closure", catalog.Fallback)
	require.Len(t, catalog.Targets, 5)
	topologyData, err := os.ReadFile("../../../../../deployment/topology.bindings.codefly.yaml")
	require.NoError(t, err)
	var topology struct {
		Services []struct {
			Name         string `yaml:"name"`
			Dependencies []struct {
				Service string `yaml:"service"`
			} `yaml:"dependencies"`
		} `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal(topologyData, &topology))
	var dependencies []string
	for _, service := range topology.Services {
		if service.Name == "accounts" {
			for _, dependency := range service.Dependencies {
				dependencies = append(dependencies, dependency.Service)
			}
		}
	}
	require.NotEmpty(t, dependencies)
	harnesses := map[string]string{
		"business-db": "business/service_test.go",
		"infra-db":    "infra/postgres_webhooks_test.go",
		"auth-db":     "auth/pg/session_store_test.go",
		"billing-db":  "billing/pg/store_test.go",
	}
	for _, target := range catalog.Targets {
		require.False(t, target.Complete, "native discovery and resolved identities are still required")
		for _, input := range []string{
			"module/services/accounts/code/pkg/business/audit_registry.go",
			"module/services/accounts/code/pkg/gen/saas/accounts/v1/accounts.pb.go",
			"module/services/store/migrations/42_rls_principals.up.sql",
			"module/contracts/api/accounts/connect/proto/saas/accounts/v1/audit.proto",
			"module/tools/composition/composition.go",
			"module/deployment/topology.bindings.codefly.yaml",
		} {
			covered := false
			for _, root := range target.InputRoots {
				covered = covered || strings.HasPrefix(input, root)
			}
			require.True(t, covered, "%s must invalidate %s", input, target.Suite)
		}
		if target.Suite == "pure" {
			require.Empty(t, target.RuntimeServices)
			require.Contains(t, target.Command, "-tags=pure")
		} else {
			require.Contains(t, target.RuntimeServices, "store")
			require.NotContains(t, target.RuntimeServices, "telemetry")
			require.NotContains(t, target.RuntimeServices, "cache")
			harness, ok := harnesses[target.Suite]
			require.True(t, ok, target.Suite)
			document, err := parser.ParseFile(token.NewFileSet(), filepath.Join("..", harness), nil, 0)
			require.NoError(t, err)
			excluded := map[string]bool{}
			ast.Inspect(document, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "WithExcludedDependencies" {
					return true
				}
				for _, argument := range call.Args {
					literal, ok := argument.(*ast.BasicLit)
					require.True(t, ok)
					name, err := strconv.Unquote(literal.Value)
					require.NoError(t, err)
					excluded[name] = true
				}
				return true
			})
			var runtimeServices []string
			for _, dependency := range dependencies {
				if !excluded[dependency] {
					runtimeServices = append(runtimeServices, dependency)
				}
			}
			require.ElementsMatch(t, target.RuntimeServices, runtimeServices, target.Suite)
		}
	}
}
