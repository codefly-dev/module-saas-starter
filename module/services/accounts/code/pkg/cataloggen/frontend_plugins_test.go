package cataloggen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"accounts/pkg/cataloggen"
	catalogv1 "accounts/pkg/gen/saas/catalog/v1"
)

func TestFrontendPluginCatalogIsDeterministicAndCurrent(t *testing.T) {
	serviceCatalog := readFixture(t, "../../../generated/service-catalog.json")
	topology := readFixture(t, "../../../../../deployment/generated/service-topology.json")
	bindings := readFixture(t, "../../../../frontend/frontend.bindings.codefly.yaml")
	codeRoot := filepath.Clean("../../../../frontend/code")

	first, err := cataloggen.BuildFrontendPluginArtifacts(serviceCatalog, topology, bindings, codeRoot)
	require.NoError(t, err)
	second, err := cataloggen.BuildFrontendPluginArtifacts(serviceCatalog, topology, bindings, codeRoot)
	require.NoError(t, err)
	require.Equal(t, first.CatalogJSON, second.CatalogJSON)
	require.Equal(t, first.TypeScript, second.TypeScript)
	require.Equal(t, string(readFixture(t, "../../../../frontend/generated/plugin-catalog.json")), string(first.CatalogJSON), "run: go generate ./pkg/cataloggen")
	require.Equal(t, string(readFixture(t, "../../../../frontend/code/src/gen/saas/frontend/v1/plugin_catalog.ts")), string(first.TypeScript), "run: go generate ./pkg/cataloggen")

	// Plugin count is a meaningful, endpoint-stable invariant (adding an RPC or
	// page doesn't change it). Route/navigation/surface totals churn on every
	// page and are already pinned byte-for-byte by the plugin-catalog.json and
	// plugin_catalog.ts fixture-equalities above, so they're not re-asserted here.
	require.Len(t, first.Catalog.GetPlugins(), 3)
	require.Contains(t, string(first.TypeScript), `path: "/admin/{*slug}"`)
}

func TestFrontendPageDiscoveryPinsAccessAndMatch(t *testing.T) {
	routes, err := cataloggen.DiscoverNextPageRoutes(filepath.Clean("../../../../frontend/code"))
	require.NoError(t, err)
	require.Len(t, routes, 47)
	byPath := make(map[string]*catalogv1.FrontendRoute, len(routes))
	for _, route := range routes {
		byPath[route.GetPath()] = route
	}
	require.Equal(t, catalogv1.FrontendRouteAccess_FRONTEND_ROUTE_ACCESS_PUBLIC, byPath["/auth/login"].GetAccess())
	require.Equal(t, catalogv1.FrontendRouteAccess_FRONTEND_ROUTE_ACCESS_AUTHENTICATED, byPath["/settings/mfa"].GetAccess())
	require.Equal(t, catalogv1.FrontendRouteAccess_FRONTEND_ROUTE_ACCESS_ADMIN, byPath["/admin/users"].GetAccess())
	require.Equal(t, catalogv1.FrontendRouteAccess_FRONTEND_ROUTE_ACCESS_SUPER_ADMIN, byPath["/admin/platform"].GetAccess())
	require.Equal(t, catalogv1.FrontendRouteAccess_FRONTEND_ROUTE_ACCESS_SUPER_ADMIN, byPath["/admin/platform/jobs"].GetAccess())
	require.Equal(t, catalogv1.FrontendRouteMatch_FRONTEND_ROUTE_MATCH_CATCH_ALL, byPath["/admin/{*slug}"].GetMatch())

	temporary := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(temporary, "src", "app", "(unknown)"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(temporary, "src", "app", "(unknown)", "page.tsx"), []byte("export default function Page() { return null }\n"), 0o644))
	_, err = cataloggen.DiscoverNextPageRoutes(temporary)
	require.ErrorContains(t, err, "unsupported route group")
}

func TestFrontendPluginBindingsRejectUnsafeDrift(t *testing.T) {
	serviceCatalog := readFixture(t, "../../../generated/service-catalog.json")
	topology := readFixture(t, "../../../../../deployment/generated/service-topology.json")
	bindings := string(readFixture(t, "../../../../frontend/frontend.bindings.codefly.yaml"))
	codeRoot := filepath.Clean("../../../../frontend/code")

	unknownField := strings.Replace(bindings, "version: v1", "version: v1\nunknown: true", 1)
	_, err := cataloggen.BuildFrontendPluginArtifacts(serviceCatalog, topology, []byte(unknownField), codeRoot)
	require.ErrorContains(t, err, "field unknown not found")

	unknownRoute := strings.Replace(bindings, "    path: /admin/users", "    path: /admin/missing", 1)
	_, err = cataloggen.BuildFrontendPluginArtifacts(serviceCatalog, topology, []byte(unknownRoute), codeRoot)
	require.ErrorContains(t, err, "unknown or dynamic route")

	unknownPermission := strings.Replace(bindings, "    permission: users:read", "    permission: users:unknown", 1)
	_, err = cataloggen.BuildFrontendPluginArtifacts(serviceCatalog, topology, []byte(unknownPermission), codeRoot)
	require.ErrorContains(t, err, "unknown permission")

	weakerAccess := strings.Replace(bindings, "    path: /admin/users\n    icon: Users\n    group: Users & Access\n    access: admin", "    path: /admin/users\n    icon: Users\n    group: Users & Access\n    access: authenticated", 1)
	_, err = cataloggen.BuildFrontendPluginArtifacts(serviceCatalog, topology, []byte(weakerAccess), codeRoot)
	require.ErrorContains(t, err, "weakens route access")

	missingPlugin := strings.Replace(bindings, "    source_path: src/plugins/audit.ts", "    source_path: src/plugins/missing.ts", 1)
	_, err = cataloggen.BuildFrontendPluginArtifacts(serviceCatalog, topology, []byte(missingPlugin), codeRoot)
	require.ErrorContains(t, err, "does not match source")
}

func TestFrontendPluginCatalogRejectsConsumerUnsafeDrift(t *testing.T) {
	document := readFixture(t, "../../../../frontend/generated/plugin-catalog.json")
	catalog := &catalogv1.FrontendPluginCatalog{}
	require.NoError(t, (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(document, catalog))
	require.NoError(t, cataloggen.ValidateFrontendPluginCatalog(catalog))

	unsorted := proto.Clone(catalog).(*catalogv1.FrontendPluginCatalog)
	unsorted.Routes[0], unsorted.Routes[1] = unsorted.Routes[1], unsorted.Routes[0]
	require.ErrorContains(t, cataloggen.ValidateFrontendPluginCatalog(unsorted), "unsorted")

	unknownPlugin := proto.Clone(catalog).(*catalogv1.FrontendPluginCatalog)
	unknownPlugin.Navigation[0].Plugin = "missing"
	require.ErrorContains(t, cataloggen.ValidateFrontendPluginCatalog(unknownPlugin), "navigation")

	invalidSurface := proto.Clone(catalog).(*catalogv1.FrontendPluginCatalog)
	invalidSurface.Navigation[0].Surfaces[0] = catalogv1.FrontendNavigationSurface_FRONTEND_NAVIGATION_SURFACE_UNSPECIFIED
	require.ErrorContains(t, cataloggen.ValidateFrontendPluginCatalog(invalidSurface), "surfaces")

	sourceMismatch := proto.Clone(catalog).(*catalogv1.FrontendPluginCatalog)
	sourceMismatch.Routes[0].SourcePath = "src/app/(dashboard)/notifications/page.tsx"
	require.ErrorContains(t, cataloggen.ValidateFrontendPluginCatalog(sourceMismatch), "disagrees with source")

	weakerAccess := proto.Clone(catalog).(*catalogv1.FrontendPluginCatalog)
	weakerAccess.Navigation[2].Access = catalogv1.FrontendRouteAccess_FRONTEND_ROUTE_ACCESS_AUTHENTICATED
	require.ErrorContains(t, cataloggen.ValidateFrontendPluginCatalog(weakerAccess), "route/access")
}
