package cataloggen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"gopkg.in/yaml.v3"

	"accounts/pkg/business"
	catalogv1 "accounts/pkg/gen/saas/catalog/v1"
)

const frontendPluginSchemaVersion = "saas.frontend.plugins.v1"

var (
	frontendNavigationIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	frontendIconPattern         = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
	frontendDynamicSegment      = regexp.MustCompile(`^\[([A-Za-z][A-Za-z0-9_]*)\]$`)
	frontendCatchAllSegment     = regexp.MustCompile(`^\[\.\.\.([A-Za-z][A-Za-z0-9_]*)\]$`)
	frontendRouteParameter      = regexp.MustCompile(`^\{[A-Za-z][A-Za-z0-9_]*\}$`)
	frontendRouteCatchAll       = regexp.MustCompile(`^\{\*[A-Za-z][A-Za-z0-9_]*\}$`)
	frontendPermissionPattern   = regexp.MustCompile(`^[a-z][a-z0-9_]*:[a-z][a-z0-9_]*$`)
)

var frontendNavigationIcons = map[string]bool{
	"Activity": true, "Bell": true, "BookOpen": true, "Building2": true,
	"CreditCard": true, "FileText": true, "Flag": true, "Globe": true,
	"HardDriveDownload": true, "Key": true, "KeyRound": true, "Layers": true,
	"LayoutDashboard": true, "ListChecks": true, "Mail": true, "Monitor": true, "Shield": true,
	"Settings": true, "ShieldCheck": true, "UserSearch": true, "Users": true, "UsersRound": true,
}

type frontendPluginBindings struct {
	Version    string                          `yaml:"version"`
	Owner      frontendOwnerBinding            `yaml:"owner"`
	Plugins    []frontendPluginBinding         `yaml:"plugins"`
	Navigation []frontendNavigationItemBinding `yaml:"navigation"`
}

type frontendOwnerBinding struct {
	Module  string `yaml:"module"`
	Service string `yaml:"service"`
}

type frontendPluginBinding struct {
	Name       string `yaml:"name"`
	SourcePath string `yaml:"source_path"`
}

type frontendNavigationItemBinding struct {
	ID         string   `yaml:"id"`
	Plugin     string   `yaml:"plugin"`
	Label      string   `yaml:"label"`
	Path       string   `yaml:"path"`
	Icon       string   `yaml:"icon"`
	Group      string   `yaml:"group,omitempty"`
	Access     string   `yaml:"access"`
	Permission string   `yaml:"permission,omitempty"`
	Surfaces   []string `yaml:"surfaces"`
	Order      uint32   `yaml:"order"`
}

// FrontendPluginArtifacts contains the normalized page/plugin inventory and
// the TypeScript data consumed by the existing compile-time plugin system.
type FrontendPluginArtifacts struct {
	Catalog     *catalogv1.FrontendPluginCatalog
	CatalogJSON []byte
	TypeScript  []byte
}

// BuildFrontendPluginArtifacts joins descriptor-owned permissions, the
// generated deployment owner, discovered Next.js pages, and strict editorial
// navigation metadata. The code root is the frontend/code directory.
func BuildFrontendPluginArtifacts(serviceDocument, topologyDocument, bindingDocument []byte, codeRoot string) (*FrontendPluginArtifacts, error) {
	serviceCatalog := &catalogv1.ServiceCatalog{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(serviceDocument, serviceCatalog); err != nil {
		return nil, fmt.Errorf("decode service catalog: %w", err)
	}
	if err := business.ValidateServiceCatalog(serviceCatalog); err != nil {
		return nil, fmt.Errorf("validate service catalog: %w", err)
	}

	topology := &catalogv1.DeploymentCatalog{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(topologyDocument, topology); err != nil {
		return nil, fmt.Errorf("decode deployment topology: %w", err)
	}
	if err := ValidateDeploymentCatalog(topology); err != nil {
		return nil, fmt.Errorf("validate deployment topology: %w", err)
	}

	bindings, err := decodeFrontendPluginBindings(bindingDocument)
	if err != nil {
		return nil, err
	}
	routes, err := DiscoverNextPageRoutes(codeRoot)
	if err != nil {
		return nil, err
	}
	pluginSources, err := discoverBuiltinPluginSources(codeRoot)
	if err != nil {
		return nil, err
	}
	if err := validateFrontendPluginBindings(serviceCatalog, topology, bindings, routes, pluginSources); err != nil {
		return nil, err
	}

	catalog, err := buildFrontendPluginCatalog(bindings, routes)
	if err != nil {
		return nil, err
	}
	catalogJSON, err := renderFrontendPluginCatalogJSON(catalog)
	if err != nil {
		return nil, err
	}
	typeScript, err := RenderFrontendPluginTypeScript(catalog)
	if err != nil {
		return nil, err
	}
	return &FrontendPluginArtifacts{Catalog: catalog, CatalogJSON: catalogJSON, TypeScript: typeScript}, nil
}

func decodeFrontendPluginBindings(document []byte) (frontendPluginBindings, error) {
	var bindings frontendPluginBindings
	decoder := yaml.NewDecoder(bytes.NewReader(document))
	decoder.KnownFields(true)
	if err := decoder.Decode(&bindings); err != nil {
		return frontendPluginBindings{}, fmt.Errorf("decode frontend plugin bindings: %w", err)
	}
	if bindings.Version != "v1" {
		return frontendPluginBindings{}, fmt.Errorf("unsupported frontend plugin bindings version %q", bindings.Version)
	}
	return bindings, nil
}

// DiscoverNextPageRoutes converts the finite Next.js app-router page tree to a
// stable route inventory. Unknown route groups and optional catch-alls fail
// rather than receiving guessed access semantics.
func DiscoverNextPageRoutes(codeRoot string) ([]*catalogv1.FrontendRoute, error) {
	appRoot := filepath.Join(codeRoot, "src", "app")
	var routes []*catalogv1.FrontendRoute
	err := filepath.WalkDir(appRoot, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() != "page.tsx" {
			return nil
		}
		relative, err := filepath.Rel(codeRoot, filePath)
		if err != nil {
			return err
		}
		route, err := nextPageRoute(filepath.ToSlash(relative))
		if err != nil {
			return err
		}
		routes = append(routes, route)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover Next.js pages: %w", err)
	}
	if len(routes) == 0 {
		return nil, fmt.Errorf("discover Next.js pages: no page.tsx files under %s", appRoot)
	}
	sort.Slice(routes, func(i, j int) bool { return frontendRouteSortKey(routes[i]) < frontendRouteSortKey(routes[j]) })
	previousPath := ""
	for _, route := range routes {
		if route.GetPath() == previousPath {
			return nil, fmt.Errorf("discover Next.js pages: duplicate route %q", route.GetPath())
		}
		previousPath = route.GetPath()
	}
	return routes, nil
}

func nextPageRoute(sourcePath string) (*catalogv1.FrontendRoute, error) {
	const prefix = "src/app/"
	if !strings.HasPrefix(sourcePath, prefix) || !strings.HasSuffix(sourcePath, "/page.tsx") {
		return nil, fmt.Errorf("invalid Next.js page source %q", sourcePath)
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(sourcePath, prefix), "/page.tsx"), "/")
	if len(parts) == 1 && parts[0] == "" {
		parts = nil
	}
	access := catalogv1.FrontendRouteAccess_FRONTEND_ROUTE_ACCESS_UNSPECIFIED
	match := catalogv1.FrontendRouteMatch_FRONTEND_ROUTE_MATCH_EXACT
	pathParts := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.HasPrefix(part, "(") && strings.HasSuffix(part, ")") {
			switch part {
			case "(auth)":
				access = catalogv1.FrontendRouteAccess_FRONTEND_ROUTE_ACCESS_PUBLIC
			case "(dashboard)":
				access = catalogv1.FrontendRouteAccess_FRONTEND_ROUTE_ACCESS_AUTHENTICATED
			default:
				return nil, fmt.Errorf("page %q uses unsupported route group %q", sourcePath, part)
			}
			continue
		}
		if strings.HasPrefix(part, "[[...") {
			return nil, fmt.Errorf("page %q uses unsupported optional catch-all segment %q", sourcePath, part)
		}
		if found := frontendCatchAllSegment.FindStringSubmatch(part); found != nil {
			if match != catalogv1.FrontendRouteMatch_FRONTEND_ROUTE_MATCH_EXACT {
				return nil, fmt.Errorf("page %q contains multiple dynamic segments", sourcePath)
			}
			match = catalogv1.FrontendRouteMatch_FRONTEND_ROUTE_MATCH_CATCH_ALL
			pathParts = append(pathParts, "{*"+found[1]+"}")
			continue
		}
		if found := frontendDynamicSegment.FindStringSubmatch(part); found != nil {
			if match == catalogv1.FrontendRouteMatch_FRONTEND_ROUTE_MATCH_CATCH_ALL {
				return nil, fmt.Errorf("page %q declares a parameter after a catch-all", sourcePath)
			}
			match = catalogv1.FrontendRouteMatch_FRONTEND_ROUTE_MATCH_PARAMETER
			pathParts = append(pathParts, "{"+found[1]+"}")
			continue
		}
		if strings.ContainsAny(part, "[]{}") || part == "" {
			return nil, fmt.Errorf("page %q has invalid route segment %q", sourcePath, part)
		}
		pathParts = append(pathParts, part)
	}
	if len(pathParts) > 0 && pathParts[0] == "admin" {
		access = catalogv1.FrontendRouteAccess_FRONTEND_ROUTE_ACCESS_ADMIN
	}
	if len(pathParts) > 1 && pathParts[0] == "admin" && pathParts[1] == "platform" {
		access = catalogv1.FrontendRouteAccess_FRONTEND_ROUTE_ACCESS_SUPER_ADMIN
	}
	if access == catalogv1.FrontendRouteAccess_FRONTEND_ROUTE_ACCESS_UNSPECIFIED {
		return nil, fmt.Errorf("page %q has no recognized access boundary", sourcePath)
	}
	routePath := "/" + strings.Join(pathParts, "/")
	if routePath == "/" || len(pathParts) > 0 {
		return &catalogv1.FrontendRoute{Path: routePath, SourcePath: sourcePath, Match: match, Access: access}, nil
	}
	return nil, fmt.Errorf("page %q produced an invalid route", sourcePath)
}

func discoverBuiltinPluginSources(codeRoot string) ([]string, error) {
	pluginsRoot := filepath.Join(codeRoot, "src", "plugins")
	entries, err := os.ReadDir(pluginsRoot)
	if err != nil {
		return nil, fmt.Errorf("discover built-in frontend plugins: %w", err)
	}
	var sources []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".ts") || entry.Name() == "registry.generated.ts" {
			continue
		}
		sources = append(sources, "src/plugins/"+entry.Name())
	}
	sort.Strings(sources)
	if len(sources) == 0 {
		return nil, fmt.Errorf("discover built-in frontend plugins: no plugin sources")
	}
	return sources, nil
}

func validateFrontendPluginBindings(serviceCatalog *catalogv1.ServiceCatalog, topology *catalogv1.DeploymentCatalog, bindings frontendPluginBindings, routes []*catalogv1.FrontendRoute, discoveredSources []string) error {
	if !endpointNamePattern.MatchString(bindings.Owner.Module) || !endpointNamePattern.MatchString(bindings.Owner.Service) {
		return fmt.Errorf("frontend owner is incomplete or invalid")
	}
	if bindings.Owner.Module != serviceCatalog.GetOwner().GetModule() || bindings.Owner.Module != topology.GetModule() {
		return fmt.Errorf("frontend owner module %q disagrees with catalog topology", bindings.Owner.Module)
	}
	foundOwner := false
	for _, service := range topology.GetServices() {
		if service.GetName() == bindings.Owner.Service {
			foundOwner = true
		}
	}
	if !foundOwner {
		return fmt.Errorf("frontend owner service %q is absent from deployment topology", bindings.Owner.Service)
	}
	if len(bindings.Plugins) == 0 || len(bindings.Navigation) == 0 {
		return fmt.Errorf("frontend plugin or navigation inventory is empty")
	}

	plugins := make(map[string]frontendPluginBinding, len(bindings.Plugins))
	previousPlugin := ""
	boundSources := make([]string, 0, len(bindings.Plugins))
	for _, plugin := range bindings.Plugins {
		if !endpointNamePattern.MatchString(plugin.Name) || plugin.Name <= previousPlugin || !validBuiltinPluginSource(plugin.SourcePath) {
			return fmt.Errorf("frontend plugins are invalid or unsorted at %q", plugin.Name)
		}
		previousPlugin = plugin.Name
		if strings.TrimSuffix(filepath.Base(plugin.SourcePath), ".ts") != plugin.Name {
			return fmt.Errorf("frontend plugin %q does not match source %q", plugin.Name, plugin.SourcePath)
		}
		plugins[plugin.Name] = plugin
		boundSources = append(boundSources, plugin.SourcePath)
	}
	sort.Strings(boundSources)
	if strings.Join(boundSources, "\x00") != strings.Join(discoveredSources, "\x00") {
		return fmt.Errorf("frontend plugin bindings do not match discovered sources: bound=%v discovered=%v", boundSources, discoveredSources)
	}

	knownRoutes := make(map[string]*catalogv1.FrontendRoute, len(routes))
	for _, route := range routes {
		knownRoutes[route.GetPath()] = route
	}
	knownPermissions := make(map[string]bool, len(serviceCatalog.GetPermissions()))
	for _, permission := range serviceCatalog.GetPermissions() {
		knownPermissions[permission.GetPermission()] = true
	}
	seenIDs := make(map[string]bool, len(bindings.Navigation))
	seenSurfacePaths := make(map[string]string)
	pluginNavigation := make(map[string]bool, len(plugins))
	previousOrder := uint32(0)
	for _, item := range bindings.Navigation {
		if !frontendNavigationIDPattern.MatchString(item.ID) || seenIDs[item.ID] || item.Order == 0 || item.Order <= previousOrder ||
			plugins[item.Plugin].Name == "" || item.Label == "" || !frontendIconPattern.MatchString(item.Icon) || !frontendNavigationIcons[item.Icon] {
			return fmt.Errorf("frontend navigation item %q is incomplete, duplicated, or unsorted", item.ID)
		}
		seenIDs[item.ID] = true
		previousOrder = item.Order
		route := knownRoutes[item.Path]
		if route == nil || route.GetMatch() != catalogv1.FrontendRouteMatch_FRONTEND_ROUTE_MATCH_EXACT {
			return fmt.Errorf("frontend navigation item %q references unknown or dynamic route %q", item.ID, item.Path)
		}
		access, err := frontendRouteAccess(item.Access)
		if err != nil {
			return fmt.Errorf("frontend navigation item %q: %w", item.ID, err)
		}
		if access < route.GetAccess() {
			return fmt.Errorf("frontend navigation item %q weakens route access", item.ID)
		}
		if item.Permission != "" && !knownPermissions[item.Permission] {
			return fmt.Errorf("frontend navigation item %q references unknown permission %q", item.ID, item.Permission)
		}
		if len(item.Surfaces) == 0 {
			return fmt.Errorf("frontend navigation item %q has no surfaces", item.ID)
		}
		previousSurface := catalogv1.FrontendNavigationSurface_FRONTEND_NAVIGATION_SURFACE_UNSPECIFIED
		for _, surfaceName := range item.Surfaces {
			surface, err := frontendNavigationSurface(surfaceName)
			if err != nil || surface <= previousSurface {
				return fmt.Errorf("frontend navigation item %q has invalid or unsorted surfaces", item.ID)
			}
			previousSurface = surface
			key := fmt.Sprintf("%d\x00%s", surface, item.Path)
			if previous := seenSurfacePaths[key]; previous != "" {
				return fmt.Errorf("frontend navigation items %q and %q duplicate a surface/path", previous, item.ID)
			}
			seenSurfacePaths[key] = item.ID
			if surface == catalogv1.FrontendNavigationSurface_FRONTEND_NAVIGATION_SURFACE_PLUGIN_REGISTRY {
				pluginNavigation[item.Plugin] = true
			}
		}
	}
	for plugin := range plugins {
		if !pluginNavigation[plugin] {
			return fmt.Errorf("frontend plugin %q has no plugin-registry navigation", plugin)
		}
	}
	return nil
}

func validBuiltinPluginSource(source string) bool {
	clean := filepath.ToSlash(filepath.Clean(source))
	return source == clean && strings.HasPrefix(source, "src/plugins/") && strings.HasSuffix(source, ".ts") && !hasParentPathSegment(source)
}

func frontendRouteAccess(value string) (catalogv1.FrontendRouteAccess, error) {
	switch value {
	case "public":
		return catalogv1.FrontendRouteAccess_FRONTEND_ROUTE_ACCESS_PUBLIC, nil
	case "authenticated":
		return catalogv1.FrontendRouteAccess_FRONTEND_ROUTE_ACCESS_AUTHENTICATED, nil
	case "admin":
		return catalogv1.FrontendRouteAccess_FRONTEND_ROUTE_ACCESS_ADMIN, nil
	case "super_admin":
		return catalogv1.FrontendRouteAccess_FRONTEND_ROUTE_ACCESS_SUPER_ADMIN, nil
	default:
		return catalogv1.FrontendRouteAccess_FRONTEND_ROUTE_ACCESS_UNSPECIFIED, fmt.Errorf("unsupported frontend route access %q", value)
	}
}

func frontendNavigationSurface(value string) (catalogv1.FrontendNavigationSurface, error) {
	switch value {
	case "command_palette":
		return catalogv1.FrontendNavigationSurface_FRONTEND_NAVIGATION_SURFACE_COMMAND_PALETTE, nil
	case "plugin_registry":
		return catalogv1.FrontendNavigationSurface_FRONTEND_NAVIGATION_SURFACE_PLUGIN_REGISTRY, nil
	case "sidebar":
		return catalogv1.FrontendNavigationSurface_FRONTEND_NAVIGATION_SURFACE_SIDEBAR, nil
	case "user_menu":
		return catalogv1.FrontendNavigationSurface_FRONTEND_NAVIGATION_SURFACE_USER_MENU, nil
	default:
		return catalogv1.FrontendNavigationSurface_FRONTEND_NAVIGATION_SURFACE_UNSPECIFIED, fmt.Errorf("unsupported frontend navigation surface %q", value)
	}
}

func buildFrontendPluginCatalog(bindings frontendPluginBindings, routes []*catalogv1.FrontendRoute) (*catalogv1.FrontendPluginCatalog, error) {
	catalog := &catalogv1.FrontendPluginCatalog{
		SchemaVersion: frontendPluginSchemaVersion,
		Module:        bindings.Owner.Module,
		Service:       bindings.Owner.Service,
		Routes:        routes,
	}
	for _, plugin := range bindings.Plugins {
		catalog.Plugins = append(catalog.Plugins, &catalogv1.FrontendPlugin{Name: plugin.Name, SourcePath: plugin.SourcePath})
	}
	for _, item := range bindings.Navigation {
		access, _ := frontendRouteAccess(item.Access)
		entry := &catalogv1.FrontendNavigationItem{
			Id: item.ID, Plugin: item.Plugin, Label: item.Label, Path: item.Path, Icon: item.Icon, Group: item.Group,
			Access: access, Permission: item.Permission, Order: item.Order,
		}
		for _, surface := range item.Surfaces {
			value, _ := frontendNavigationSurface(surface)
			entry.Surfaces = append(entry.Surfaces, value)
		}
		catalog.Navigation = append(catalog.Navigation, entry)
	}
	if err := ValidateFrontendPluginCatalog(catalog); err != nil {
		return nil, err
	}
	return catalog, nil
}

func frontendRouteSortKey(route *catalogv1.FrontendRoute) string {
	return route.GetPath() + "\x00" + route.GetSourcePath()
}

// ValidateFrontendPluginCatalog validates normalized output independently of
// the authored binding and discovered filesystem.
func ValidateFrontendPluginCatalog(catalog *catalogv1.FrontendPluginCatalog) error {
	if catalog == nil || catalog.GetSchemaVersion() != frontendPluginSchemaVersion {
		return fmt.Errorf("unsupported frontend plugin catalog schema version")
	}
	if !endpointNamePattern.MatchString(catalog.GetModule()) || !endpointNamePattern.MatchString(catalog.GetService()) ||
		len(catalog.GetPlugins()) == 0 || len(catalog.GetRoutes()) == 0 || len(catalog.GetNavigation()) == 0 {
		return fmt.Errorf("frontend plugin catalog identity or inventory is incomplete")
	}
	plugins := make(map[string]bool, len(catalog.GetPlugins()))
	previousPlugin := ""
	for _, plugin := range catalog.GetPlugins() {
		if plugin == nil || !endpointNamePattern.MatchString(plugin.GetName()) || plugin.GetName() <= previousPlugin || !validBuiltinPluginSource(plugin.GetSourcePath()) {
			return fmt.Errorf("frontend plugins are incomplete or unsorted")
		}
		if strings.TrimSuffix(filepath.Base(plugin.GetSourcePath()), ".ts") != plugin.GetName() {
			return fmt.Errorf("frontend plugin %q does not match its source", plugin.GetName())
		}
		previousPlugin = plugin.GetName()
		plugins[plugin.GetName()] = true
	}
	routes := make(map[string]*catalogv1.FrontendRoute, len(catalog.GetRoutes()))
	previousRoute := ""
	for _, route := range catalog.GetRoutes() {
		if route == nil {
			return fmt.Errorf("frontend routes contain a nil entry")
		}
		key := frontendRouteSortKey(route)
		if !validFrontendRoutePath(route) {
			return fmt.Errorf("frontend route %q has invalid path/match", route.GetPath())
		}
		if !validFrontendPageSource(route.GetSourcePath()) {
			return fmt.Errorf("frontend route %q has invalid source %q", route.GetPath(), route.GetSourcePath())
		}
		if !validFrontendRouteAccess(route.GetAccess()) {
			return fmt.Errorf("frontend route %q has invalid access", route.GetPath())
		}
		derived, err := nextPageRoute(route.GetSourcePath())
		if err != nil || derived.GetPath() != route.GetPath() || derived.GetMatch() != route.GetMatch() || derived.GetAccess() != route.GetAccess() {
			return fmt.Errorf("frontend route %q disagrees with source %q", route.GetPath(), route.GetSourcePath())
		}
		if previousRoute != "" && key <= previousRoute {
			return fmt.Errorf("frontend routes are unsorted at %q", route.GetPath())
		}
		if routes[route.GetPath()] != nil {
			return fmt.Errorf("frontend route %q is duplicated", route.GetPath())
		}
		previousRoute = key
		routes[route.GetPath()] = route
	}
	previousOrder := uint32(0)
	seenIDs := make(map[string]bool, len(catalog.GetNavigation()))
	seenSurfacePaths := make(map[string]bool)
	pluginNavigation := make(map[string]bool, len(plugins))
	for _, item := range catalog.GetNavigation() {
		if item == nil || !frontendNavigationIDPattern.MatchString(item.GetId()) || seenIDs[item.GetId()] || !plugins[item.GetPlugin()] ||
			item.GetLabel() == "" || !frontendNavigationIcons[item.GetIcon()] || item.GetOrder() == 0 || item.GetOrder() <= previousOrder || len(item.GetSurfaces()) == 0 {
			return fmt.Errorf("frontend navigation is incomplete, duplicated, or unsorted")
		}
		seenIDs[item.GetId()] = true
		previousOrder = item.GetOrder()
		route := routes[item.GetPath()]
		if route == nil || route.GetMatch() != catalogv1.FrontendRouteMatch_FRONTEND_ROUTE_MATCH_EXACT ||
			!validFrontendRouteAccess(item.GetAccess()) || item.GetAccess() < route.GetAccess() {
			return fmt.Errorf("frontend navigation item %q has invalid route/access", item.GetId())
		}
		if item.GetPermission() != "" && !frontendPermissionPattern.MatchString(item.GetPermission()) {
			return fmt.Errorf("frontend navigation item %q has malformed permission", item.GetId())
		}
		previousSurface := catalogv1.FrontendNavigationSurface_FRONTEND_NAVIGATION_SURFACE_UNSPECIFIED
		for _, surface := range item.GetSurfaces() {
			if surface <= catalogv1.FrontendNavigationSurface_FRONTEND_NAVIGATION_SURFACE_UNSPECIFIED ||
				surface > catalogv1.FrontendNavigationSurface_FRONTEND_NAVIGATION_SURFACE_USER_MENU || surface <= previousSurface {
				return fmt.Errorf("frontend navigation item %q surfaces are invalid or unsorted", item.GetId())
			}
			previousSurface = surface
			key := fmt.Sprintf("%d\x00%s", surface, item.GetPath())
			if seenSurfacePaths[key] {
				return fmt.Errorf("frontend navigation duplicates surface/path %q", item.GetPath())
			}
			seenSurfacePaths[key] = true
			if surface == catalogv1.FrontendNavigationSurface_FRONTEND_NAVIGATION_SURFACE_PLUGIN_REGISTRY {
				pluginNavigation[item.GetPlugin()] = true
			}
		}
	}
	for plugin := range plugins {
		if !pluginNavigation[plugin] {
			return fmt.Errorf("frontend plugin %q has no registry navigation", plugin)
		}
	}
	return nil
}

func validFrontendRouteAccess(access catalogv1.FrontendRouteAccess) bool {
	return access >= catalogv1.FrontendRouteAccess_FRONTEND_ROUTE_ACCESS_PUBLIC &&
		access <= catalogv1.FrontendRouteAccess_FRONTEND_ROUTE_ACCESS_SUPER_ADMIN
}

func validFrontendPageSource(source string) bool {
	clean := filepath.ToSlash(filepath.Clean(source))
	return source == clean && strings.HasPrefix(source, "src/app/") && strings.HasSuffix(source, "/page.tsx") && !hasParentPathSegment(source)
}

func hasParentPathSegment(source string) bool {
	for _, segment := range strings.Split(source, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

func validFrontendRoutePath(route *catalogv1.FrontendRoute) bool {
	path := route.GetPath()
	if path == "" || !strings.HasPrefix(path, "/") || (path != "/" && strings.HasSuffix(path, "/")) || strings.Contains(path, "//") {
		return false
	}
	if path == "/" {
		return route.GetMatch() == catalogv1.FrontendRouteMatch_FRONTEND_ROUTE_MATCH_EXACT
	}
	parameterCount := 0
	catchAllCount := 0
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for index, segment := range segments {
		switch {
		case frontendRouteParameter.MatchString(segment):
			parameterCount++
		case frontendRouteCatchAll.MatchString(segment):
			catchAllCount++
			if index != len(segments)-1 {
				return false
			}
		case strings.ContainsAny(segment, "{}"):
			return false
		}
	}
	switch route.GetMatch() {
	case catalogv1.FrontendRouteMatch_FRONTEND_ROUTE_MATCH_EXACT:
		return parameterCount == 0 && catchAllCount == 0
	case catalogv1.FrontendRouteMatch_FRONTEND_ROUTE_MATCH_PARAMETER:
		return parameterCount > 0 && catchAllCount == 0
	case catalogv1.FrontendRouteMatch_FRONTEND_ROUTE_MATCH_CATCH_ALL:
		return catchAllCount == 1
	default:
		return false
	}
}

func renderFrontendPluginCatalogJSON(catalog *catalogv1.FrontendPluginCatalog) ([]byte, error) {
	if err := ValidateFrontendPluginCatalog(catalog); err != nil {
		return nil, err
	}
	document, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(catalog)
	if err != nil {
		return nil, fmt.Errorf("render frontend plugin catalog: %w", err)
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, document, "", "  "); err != nil {
		return nil, fmt.Errorf("format frontend plugin catalog: %w", err)
	}
	formatted.WriteByte('\n')
	return formatted.Bytes(), nil
}

// RenderFrontendPluginTypeScript emits pure typed data. React components and
// icon implementations remain explicit runtime bindings in frontend code.
func RenderFrontendPluginTypeScript(catalog *catalogv1.FrontendPluginCatalog) ([]byte, error) {
	if err := ValidateFrontendPluginCatalog(catalog); err != nil {
		return nil, err
	}
	var source strings.Builder
	source.WriteString(`// Code generated by cmd/frontend-plugins. DO NOT EDIT.

import type { Permission } from "../../accounts/v1/frontend_catalog";

export type FrontendRouteAccess = "public" | "authenticated" | "admin" | "super_admin";
export type FrontendRouteMatch = "exact" | "parameter" | "catch_all";
export type FrontendNavigationSurface = "command_palette" | "plugin_registry" | "sidebar" | "user_menu";

export interface FrontendPluginDefinition {
  readonly name: string;
  readonly sourcePath: string;
}

export interface FrontendRouteDefinition {
  readonly path: string;
  readonly sourcePath: string;
  readonly match: FrontendRouteMatch;
  readonly access: FrontendRouteAccess;
}

export interface FrontendNavigationItem {
  readonly id: string;
  readonly plugin: FrontendPluginName;
  readonly label: string;
  readonly href: string;
  readonly icon: FrontendNavigationIcon;
  readonly group?: string;
  readonly access: FrontendRouteAccess;
  readonly requiredRole?: "admin" | "super_admin";
  readonly requiredPermission?: Permission;
  readonly surfaces: readonly FrontendNavigationSurface[];
  readonly order: number;
}

export const FRONTEND_PLUGINS = [
`)
	for _, plugin := range catalog.GetPlugins() {
		fmt.Fprintf(&source, "  { name: %s, sourcePath: %s },\n", quoteTS(plugin.GetName()), quoteTS(plugin.GetSourcePath()))
	}
	source.WriteString(`] as const satisfies readonly FrontendPluginDefinition[];

export type FrontendPluginName = (typeof FRONTEND_PLUGINS)[number]["name"];

export const FRONTEND_ROUTES = [
`)
	for _, route := range catalog.GetRoutes() {
		access, _ := frontendRouteAccessName(route.GetAccess())
		match, _ := frontendRouteMatchName(route.GetMatch())
		fmt.Fprintf(&source, "  { path: %s, sourcePath: %s, match: %s, access: %s },\n",
			quoteTS(route.GetPath()), quoteTS(route.GetSourcePath()), quoteTS(match), quoteTS(access))
	}
	source.WriteString(`] as const satisfies readonly FrontendRouteDefinition[];

export type FrontendNavigationIcon =
`)
	icons := sortedBoolMapKeys(frontendNavigationIcons)
	for index, icon := range icons {
		separator := "  | "
		if index == 0 {
			separator = "  "
		}
		fmt.Fprintf(&source, "%s%s\n", separator, quoteTS(icon))
	}
	source.WriteString(`;

export const FRONTEND_NAVIGATION: readonly FrontendNavigationItem[] = [
`)
	for _, item := range catalog.GetNavigation() {
		access, _ := frontendRouteAccessName(item.GetAccess())
		fmt.Fprintf(&source, "  { id: %s, plugin: %s, label: %s, href: %s, icon: %s, ",
			quoteTS(item.GetId()), quoteTS(item.GetPlugin()), quoteTS(item.GetLabel()), quoteTS(item.GetPath()), quoteTS(item.GetIcon()))
		if item.GetGroup() != "" {
			fmt.Fprintf(&source, "group: %s, ", quoteTS(item.GetGroup()))
		}
		fmt.Fprintf(&source, "access: %s, ", quoteTS(access))
		switch item.GetAccess() {
		case catalogv1.FrontendRouteAccess_FRONTEND_ROUTE_ACCESS_ADMIN:
			source.WriteString(`requiredRole: "admin", `)
		case catalogv1.FrontendRouteAccess_FRONTEND_ROUTE_ACCESS_SUPER_ADMIN:
			source.WriteString(`requiredRole: "super_admin", `)
		}
		if item.GetPermission() != "" {
			fmt.Fprintf(&source, "requiredPermission: %s, ", quoteTS(item.GetPermission()))
		}
		surfaceNames := make([]string, 0, len(item.GetSurfaces()))
		for _, surface := range item.GetSurfaces() {
			name, _ := frontendNavigationSurfaceName(surface)
			surfaceNames = append(surfaceNames, name)
		}
		fmt.Fprintf(&source, "surfaces: %s, order: %d },\n", typescriptStringArray(surfaceNames), item.GetOrder())
	}
	source.WriteString(`];

export const SIDEBAR_NAVIGATION = FRONTEND_NAVIGATION.filter((item) => item.surfaces.includes("sidebar"));
export const COMMAND_PALETTE_NAVIGATION = FRONTEND_NAVIGATION.filter((item) => item.surfaces.includes("command_palette"));
export const USER_MENU_NAVIGATION = FRONTEND_NAVIGATION.filter((item) => item.surfaces.includes("user_menu"));

export const PLUGIN_NAVIGATION: Readonly<Record<FrontendPluginName, readonly FrontendNavigationItem[]>> = {
`)
	for _, plugin := range catalog.GetPlugins() {
		fmt.Fprintf(&source, "  %s: FRONTEND_NAVIGATION.filter((item) => item.plugin === %s && item.surfaces.includes(\"plugin_registry\")),\n",
			quoteTS(plugin.GetName()), quoteTS(plugin.GetName()))
	}
	source.WriteString("};\n")
	return []byte(source.String()), nil
}

func frontendRouteAccessName(value catalogv1.FrontendRouteAccess) (string, error) {
	switch value {
	case catalogv1.FrontendRouteAccess_FRONTEND_ROUTE_ACCESS_PUBLIC:
		return "public", nil
	case catalogv1.FrontendRouteAccess_FRONTEND_ROUTE_ACCESS_AUTHENTICATED:
		return "authenticated", nil
	case catalogv1.FrontendRouteAccess_FRONTEND_ROUTE_ACCESS_ADMIN:
		return "admin", nil
	case catalogv1.FrontendRouteAccess_FRONTEND_ROUTE_ACCESS_SUPER_ADMIN:
		return "super_admin", nil
	default:
		return "", fmt.Errorf("unsupported frontend route access %s", value)
	}
}

func frontendRouteMatchName(value catalogv1.FrontendRouteMatch) (string, error) {
	switch value {
	case catalogv1.FrontendRouteMatch_FRONTEND_ROUTE_MATCH_EXACT:
		return "exact", nil
	case catalogv1.FrontendRouteMatch_FRONTEND_ROUTE_MATCH_PARAMETER:
		return "parameter", nil
	case catalogv1.FrontendRouteMatch_FRONTEND_ROUTE_MATCH_CATCH_ALL:
		return "catch_all", nil
	default:
		return "", fmt.Errorf("unsupported frontend route match %s", value)
	}
}

func frontendNavigationSurfaceName(value catalogv1.FrontendNavigationSurface) (string, error) {
	switch value {
	case catalogv1.FrontendNavigationSurface_FRONTEND_NAVIGATION_SURFACE_COMMAND_PALETTE:
		return "command_palette", nil
	case catalogv1.FrontendNavigationSurface_FRONTEND_NAVIGATION_SURFACE_PLUGIN_REGISTRY:
		return "plugin_registry", nil
	case catalogv1.FrontendNavigationSurface_FRONTEND_NAVIGATION_SURFACE_SIDEBAR:
		return "sidebar", nil
	case catalogv1.FrontendNavigationSurface_FRONTEND_NAVIGATION_SURFACE_USER_MENU:
		return "user_menu", nil
	default:
		return "", fmt.Errorf("unsupported frontend navigation surface %s", value)
	}
}

func sortedBoolMapKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key, enabled := range values {
		if enabled {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}
