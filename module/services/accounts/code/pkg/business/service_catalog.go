package business

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	catalogv1 "accounts/pkg/gen/saas/catalog/v1"
	policyv1 "accounts/pkg/gen/saas/policy/v1"
)

const (
	// ServiceCatalogSchemaVersion identifies the normalized catalog contract.
	ServiceCatalogSchemaVersion = "saas.catalog.v1"
	serviceCatalogModule        = "saas-starter"
	serviceCatalogOwner         = "accounts"
)

// BuildServiceCatalog compiles the registered service descriptors into the
// typed catalog contract. Invalid descriptor metadata fails generation rather
// than producing a partial or permissive inventory.
func BuildServiceCatalog() (*catalogv1.ServiceCatalog, error) {
	policies := RPCPolicies()
	if len(policies) == 0 {
		return nil, fmt.Errorf("service catalog contains no methods")
	}

	owner := &catalogv1.ServiceOwner{Module: serviceCatalogModule, Service: serviceCatalogOwner}
	catalog := &catalogv1.ServiceCatalog{
		SchemaVersion: ServiceCatalogSchemaVersion,
		Owner:         owner,
		ApiVersion:    ServiceVersion,
		Methods:       make([]*catalogv1.Method, 0, len(policies)),
		Permissions:   catalogPermissionDefinitions(),
		Entitlements:  catalogEntitlementDefinitions(),
	}
	serviceProcedures := make(map[string][]string)
	serviceNames := make(map[string]string)
	seenProcedures := make(map[string]struct{}, len(policies))
	seenHTTPRoutes := make(map[string]string)

	for _, rpc := range policies {
		if err := validateCatalogRPC(rpc); err != nil {
			return nil, err
		}
		if _, exists := seenProcedures[rpc.FullMethod]; exists {
			return nil, fmt.Errorf("duplicate procedure %q", rpc.FullMethod)
		}
		seenProcedures[rpc.FullMethod] = struct{}{}

		apiPackage := packageName(rpc.FullService)
		if catalog.ApiPackage == "" {
			catalog.ApiPackage = apiPackage
		} else if catalog.ApiPackage != apiPackage {
			return nil, fmt.Errorf("procedure %q belongs to package %q, catalog package is %q", rpc.FullMethod, apiPackage, catalog.ApiPackage)
		}

		httpBindings := make([]*catalogv1.HTTPBinding, 0, len(rpc.HTTPBindings))
		for _, binding := range rpc.HTTPBindings {
			routeKey := binding.Method + " " + binding.Path
			if previous, exists := seenHTTPRoutes[routeKey]; exists {
				return nil, fmt.Errorf("duplicate HTTP route %q on %q and %q", routeKey, previous, rpc.FullMethod)
			}
			seenHTTPRoutes[routeKey] = rpc.FullMethod
			httpBindings = append(httpBindings, &catalogv1.HTTPBinding{
				Method:       binding.Method,
				Path:         binding.Path,
				Body:         binding.Body,
				ResponseBody: binding.ResponseBody,
			})
		}

		protocols := []catalogv1.Protocol{
			catalogv1.Protocol_PROTOCOL_GRPC,
			catalogv1.Protocol_PROTOCOL_CONNECT,
		}
		if len(httpBindings) > 0 {
			protocols = append(protocols, catalogv1.Protocol_PROTOCOL_REST)
		}
		methodPolicy, _ := proto.Clone(rpc.MethodPolicy).(*policyv1.MethodPolicy)
		catalog.Methods = append(catalog.Methods, &catalogv1.Method{
			Procedure:       rpc.FullMethod,
			Service:         rpc.Service,
			FullService:     rpc.FullService,
			Method:          rpc.Method,
			InputType:       rpc.InputType,
			OutputType:      rpc.OutputType,
			ClientStreaming: rpc.ClientStreaming,
			ServerStreaming: rpc.ServerStreaming,
			Deprecated:      rpc.Deprecated,
			SourceProto:     rpc.SourceProto,
			Protocols:       protocols,
			HttpBindings:    httpBindings,
			Policy:          methodPolicy,
			PolicyTier:      string(rpc.Tier),
			Description:     rpc.Description,
			Owner:           &catalogv1.ServiceOwner{Module: owner.Module, Service: owner.Service},
		})
		serviceProcedures[rpc.FullService] = append(serviceProcedures[rpc.FullService], rpc.FullMethod)
		serviceNames[rpc.FullService] = rpc.Service
	}

	serviceFullNames := make([]string, 0, len(serviceProcedures))
	for fullName := range serviceProcedures {
		serviceFullNames = append(serviceFullNames, fullName)
	}
	sort.Strings(serviceFullNames)
	for _, fullName := range serviceFullNames {
		procedures := serviceProcedures[fullName]
		sort.Strings(procedures)
		catalog.Services = append(catalog.Services, &catalogv1.Service{
			Name:       serviceNames[fullName],
			FullName:   fullName,
			Procedures: procedures,
		})
	}

	if err := ValidateServiceCatalog(catalog); err != nil {
		return nil, err
	}
	return catalog, nil
}

func validateCatalogRPC(rpc RPCPolicy) error {
	if rpc.PolicyError != "" {
		return fmt.Errorf("procedure %q has invalid method policy: %s", rpc.FullMethod, rpc.PolicyError)
	}
	if rpc.MethodPolicy == nil {
		return fmt.Errorf("procedure %q has no method policy", rpc.FullMethod)
	}
	if !rpc.Tier.Valid() {
		return fmt.Errorf("procedure %q has invalid compatibility policy tier %q", rpc.FullMethod, rpc.Tier)
	}
	if rpc.FullMethod == "" || rpc.FullService == "" || rpc.Service == "" || rpc.Method == "" ||
		rpc.InputType == "" || rpc.OutputType == "" || rpc.SourceProto == "" {
		return fmt.Errorf("procedure %q has incomplete descriptor identity", rpc.FullMethod)
	}
	if rpc.Description == "" {
		return fmt.Errorf("procedure %q has no description", rpc.FullMethod)
	}
	if rpc.MethodPolicy.GetExposure() == policyv1.Exposure_EXPOSURE_INTERNAL && len(rpc.HTTPBindings) > 0 {
		return fmt.Errorf("internal procedure %q must not declare HTTP bindings", rpc.FullMethod)
	}
	for _, binding := range rpc.HTTPBindings {
		if binding.Method == "" || binding.Path == "" {
			return fmt.Errorf("procedure %q has an incomplete HTTP binding", rpc.FullMethod)
		}
	}
	return nil
}

func packageName(fullService string) string {
	index := strings.LastIndexByte(fullService, '.')
	if index < 0 {
		return ""
	}
	return fullService[:index]
}

// ValidateServiceCatalog checks invariants required by downstream generators.
// It deliberately does not repair or reorder malformed input.
func ValidateServiceCatalog(catalog *catalogv1.ServiceCatalog) error {
	if catalog == nil {
		return fmt.Errorf("service catalog is nil")
	}
	if catalog.GetSchemaVersion() != ServiceCatalogSchemaVersion {
		return fmt.Errorf("unsupported service catalog schema version %q", catalog.GetSchemaVersion())
	}
	if catalog.GetOwner().GetModule() == "" || catalog.GetOwner().GetService() == "" {
		return fmt.Errorf("service catalog owner is incomplete")
	}
	if catalog.GetApiPackage() == "" || catalog.GetApiVersion() == "" {
		return fmt.Errorf("service catalog API identity is incomplete")
	}
	if len(catalog.GetMethods()) == 0 || len(catalog.GetServices()) == 0 {
		return fmt.Errorf("service catalog inventory is empty")
	}
	if len(catalog.GetPermissions()) == 0 || len(catalog.GetEntitlements()) == 0 {
		return fmt.Errorf("service catalog frontend vocabulary is empty")
	}

	permissionDefinitions := make(map[string]*catalogv1.PermissionDefinition, len(catalog.GetPermissions()))
	previousPermission := ""
	for _, permission := range catalog.GetPermissions() {
		if permission == nil || permission.GetPermission() == "" || permission.GetResource() == "" ||
			permission.GetAction() == "" || permission.GetDescription() == "" {
			return fmt.Errorf("service catalog contains an incomplete permission definition")
		}
		if previousPermission != "" && permission.GetPermission() <= previousPermission {
			return fmt.Errorf("permissions are not strictly sorted at %q", permission.GetPermission())
		}
		previousPermission = permission.GetPermission()
		if !policyVocabulary.MatchString(permission.GetPermission()) ||
			permission.GetPermission() != permission.GetResource()+":"+permission.GetAction() {
			return fmt.Errorf("permission definition %q is not canonical", permission.GetPermission())
		}
		previousRole := ""
		for _, role := range permission.GetBuiltInRoles() {
			if role == "" || (previousRole != "" && role <= previousRole) {
				return fmt.Errorf("permission %q built-in roles are not strictly sorted", permission.GetPermission())
			}
			previousRole = role
		}
		permissionDefinitions[permission.GetPermission()] = permission
	}

	previousEntitlement := ""
	for _, entitlement := range catalog.GetEntitlements() {
		if entitlement == nil || entitlement.GetKey() == "" || entitlement.GetUnit() == "" ||
			entitlement.GetDescription() == "" || entitlement.GetKind() == catalogv1.EntitlementKind_ENTITLEMENT_KIND_UNSPECIFIED {
			return fmt.Errorf("service catalog contains an incomplete entitlement definition")
		}
		if previousEntitlement != "" && entitlement.GetKey() <= previousEntitlement {
			return fmt.Errorf("entitlements are not strictly sorted at %q", entitlement.GetKey())
		}
		previousEntitlement = entitlement.GetKey()
		if !entitlementKeyPattern.MatchString(entitlement.GetKey()) {
			return fmt.Errorf("entitlement key %q is not canonical", entitlement.GetKey())
		}
		switch entitlement.GetKind() {
		case catalogv1.EntitlementKind_ENTITLEMENT_KIND_FEATURE,
			catalogv1.EntitlementKind_ENTITLEMENT_KIND_QUOTA:
		default:
			return fmt.Errorf("entitlement %q has unsupported kind %s", entitlement.GetKey(), entitlement.GetKind())
		}
	}

	methodProcedures := make(map[string]struct{}, len(catalog.GetMethods()))
	seenHTTPRoutes := make(map[string]string)
	previousProcedure := ""
	for _, method := range catalog.GetMethods() {
		if method == nil || method.GetProcedure() == "" {
			return fmt.Errorf("service catalog contains an unidentified method")
		}
		if previousProcedure != "" && method.GetProcedure() <= previousProcedure {
			return fmt.Errorf("methods are not strictly sorted at %q", method.GetProcedure())
		}
		previousProcedure = method.GetProcedure()
		methodProcedures[method.GetProcedure()] = struct{}{}
		if method.GetService() == "" || method.GetFullService() == "" || method.GetMethod() == "" ||
			method.GetInputType() == "" || method.GetOutputType() == "" || method.GetSourceProto() == "" ||
			method.GetDescription() == "" {
			return fmt.Errorf("procedure %q descriptor identity is incomplete", method.GetProcedure())
		}
		if method.GetProcedure() != "/"+method.GetFullService()+"/"+method.GetMethod() {
			return fmt.Errorf("procedure %q does not match its service and method identity", method.GetProcedure())
		}
		if packageName(method.GetFullService()) != catalog.GetApiPackage() {
			return fmt.Errorf("procedure %q does not belong to API package %q", method.GetProcedure(), catalog.GetApiPackage())
		}
		if method.GetOwner().GetModule() != catalog.GetOwner().GetModule() ||
			method.GetOwner().GetService() != catalog.GetOwner().GetService() {
			return fmt.Errorf("procedure %q owner does not match catalog owner", method.GetProcedure())
		}
		if method.GetPolicy() == nil || !RPCPolicyTier(method.GetPolicyTier()).Valid() {
			return fmt.Errorf("procedure %q policy metadata is incomplete", method.GetProcedure())
		}
		for _, permission := range method.GetPolicy().GetPermissions() {
			if _, exists := permissionDefinitions[permission]; !exists {
				return fmt.Errorf("procedure %q references unknown permission %q", method.GetProcedure(), permission)
			}
		}
		for _, scope := range method.GetPolicy().GetScopes() {
			definition, exists := permissionDefinitions[scope]
			if !exists || !definition.GetApiKeyScope() {
				return fmt.Errorf("procedure %q references unsupported API-key scope %q", method.GetProcedure(), scope)
			}
		}
		hasREST := len(method.GetHttpBindings()) > 0
		if hasREST && method.GetPolicy().GetExposure() == policyv1.Exposure_EXPOSURE_INTERNAL {
			return fmt.Errorf("internal procedure %q must not declare HTTP bindings", method.GetProcedure())
		}
		expectedProtocolCount := 2
		if hasREST {
			expectedProtocolCount = 3
		}
		if len(method.GetProtocols()) != expectedProtocolCount ||
			method.GetProtocols()[0] != catalogv1.Protocol_PROTOCOL_GRPC ||
			method.GetProtocols()[1] != catalogv1.Protocol_PROTOCOL_CONNECT ||
			(hasREST && method.GetProtocols()[2] != catalogv1.Protocol_PROTOCOL_REST) {
			return fmt.Errorf("procedure %q protocol list is not canonical", method.GetProcedure())
		}
		for _, binding := range method.GetHttpBindings() {
			if binding.GetMethod() == "" || binding.GetPath() == "" {
				return fmt.Errorf("procedure %q has an incomplete HTTP binding", method.GetProcedure())
			}
			routeKey := binding.GetMethod() + " " + binding.GetPath()
			if previous, exists := seenHTTPRoutes[routeKey]; exists {
				return fmt.Errorf("duplicate HTTP route %q on %q and %q", routeKey, previous, method.GetProcedure())
			}
			seenHTTPRoutes[routeKey] = method.GetProcedure()
		}
	}

	previousService := ""
	groupedProcedures := make(map[string]struct{}, len(methodProcedures))
	for _, service := range catalog.GetServices() {
		if service == nil || service.GetName() == "" || service.GetFullName() == "" {
			return fmt.Errorf("service catalog contains an unidentified service")
		}
		if previousService != "" && service.GetFullName() <= previousService {
			return fmt.Errorf("services are not strictly sorted at %q", service.GetFullName())
		}
		previousService = service.GetFullName()
		previousProcedure = ""
		for _, procedure := range service.GetProcedures() {
			if previousProcedure != "" && procedure <= previousProcedure {
				return fmt.Errorf("service %q procedures are not strictly sorted at %q", service.GetFullName(), procedure)
			}
			previousProcedure = procedure
			if _, exists := methodProcedures[procedure]; !exists {
				return fmt.Errorf("service %q references unknown procedure %q", service.GetFullName(), procedure)
			}
			if _, exists := groupedProcedures[procedure]; exists {
				return fmt.Errorf("procedure %q is grouped more than once", procedure)
			}
			groupedProcedures[procedure] = struct{}{}
		}
	}
	if len(groupedProcedures) != len(methodProcedures) {
		return fmt.Errorf("service grouping covers %d of %d methods", len(groupedProcedures), len(methodProcedures))
	}
	return nil
}

// RenderServiceCatalogJSON builds and serializes the catalog using proto field
// names. No timestamps or ambient paths enter the output.
func RenderServiceCatalogJSON() ([]byte, error) {
	catalog, err := BuildServiceCatalog()
	if err != nil {
		return nil, err
	}
	document, err := (protojson.MarshalOptions{
		UseProtoNames:   true,
		EmitUnpopulated: true,
	}).Marshal(catalog)
	if err != nil {
		return nil, fmt.Errorf("marshal service catalog: %w", err)
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, document, "", "  "); err != nil {
		return nil, fmt.Errorf("format service catalog: %w", err)
	}
	formatted.WriteByte('\n')
	return formatted.Bytes(), nil
}
