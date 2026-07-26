package cataloggen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

const frontendPluginServiceManifestSource = "deployment/topology.bindings.codefly.yaml and services/frontend/code/server/plugin-service-allowlist.generated.json"

var frontendPluginLogicalIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)
var frontendPluginRoutePrefixPattern = regexp.MustCompile(`^/[A-Za-z0-9._~-]+(?:/[A-Za-z0-9._~-]+)*$`)

type frontendPluginAllowlist struct {
	SchemaVersion   int                            `json:"schemaVersion"`
	ContractVersion int                            `json:"contractVersion"`
	Entries         []frontendPluginAllowlistEntry `json:"entries"`
}

type frontendPluginAllowlistEntry struct {
	Plugin        string `json:"plugin"`
	Alias         string `json:"alias"`
	Protocol      string `json:"protocol"`
	RoutePrefix   string `json:"routePrefix"`
	Compatibility struct {
		Contract  string  `json:"contract"`
		Major     int     `json:"major"`
		ProbePath *string `json:"probePath"`
	} `json:"compatibility"`
	Target struct {
		Module   string `json:"module"`
		Service  string `json:"service"`
		Endpoint string `json:"endpoint"`
	} `json:"target"`
}

// BuildDeploymentArtifactsWithFrontendPluginAllowlist renders the normal
// topology plus application-installed cross-module service dependencies for the
// frontend. Concrete addresses remain Codefly runtime values; this input carries
// logical module/service/endpoint names only.
func BuildDeploymentArtifactsWithFrontendPluginAllowlist(
	serviceDocument, bindingDocument, allowlistDocument []byte,
) (*DeploymentArtifacts, error) {
	artifacts, err := BuildDeploymentArtifacts(serviceDocument, bindingDocument)
	if err != nil {
		return nil, err
	}
	external, err := decodeFrontendPluginServiceDependencies(allowlistDocument)
	if err != nil {
		return nil, err
	}
	bindings, err := decodeDeploymentBindings(bindingDocument)
	if err != nil {
		return nil, err
	}
	var frontend *deploymentServiceBinding
	for index := range bindings.Services {
		if bindings.Services[index].Name == "frontend" {
			frontend = &bindings.Services[index]
			break
		}
	}
	if frontend == nil {
		return nil, fmt.Errorf("deployment topology has no frontend service for plugin dependencies")
	}
	for _, dependency := range external {
		if dependency.Module == bindings.Module.Name {
			return nil, fmt.Errorf(
				"frontend plugin service target %q belongs to module %q; application plugin bindings must reference an external product module",
				dependency.Name,
				dependency.Module,
			)
		}
	}
	manifest, err := renderServiceManifestWithExternalDependencies(
		*frontend,
		external,
		frontendPluginServiceManifestSource,
	)
	if err != nil {
		return nil, err
	}
	artifacts.ServiceManifests["frontend"] = manifest
	return artifacts, nil
}

func decodeFrontendPluginServiceDependencies(document []byte) ([]manifestServiceDependency, error) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var allowlist frontendPluginAllowlist
	if err := decoder.Decode(&allowlist); err != nil {
		return nil, fmt.Errorf("decode frontend plugin service allowlist: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("decode frontend plugin service allowlist: trailing document")
	}
	if allowlist.SchemaVersion != 1 || allowlist.ContractVersion != 2 {
		return nil, fmt.Errorf(
			"unsupported frontend plugin service allowlist schema=%d contract=%d",
			allowlist.SchemaVersion,
			allowlist.ContractVersion,
		)
	}

	type target struct {
		module    string
		service   string
		endpoints map[string]bool
	}
	targets := make(map[string]*target)
	owners := make(map[string]bool)
	for _, entry := range allowlist.Entries {
		owner := entry.Plugin + "/" + entry.Alias
		if !frontendPluginLogicalIDPattern.MatchString(entry.Plugin) ||
			!frontendPluginLogicalIDPattern.MatchString(entry.Alias) {
			return nil, fmt.Errorf("frontend plugin service allowlist has invalid owner %q", owner)
		}
		if owners[owner] {
			return nil, fmt.Errorf("frontend plugin service allowlist has duplicate owner %q", owner)
		}
		owners[owner] = true
		if entry.Protocol != "rest" && entry.Protocol != "connect" {
			return nil, fmt.Errorf("frontend plugin service %q has unsupported protocol %q", owner, entry.Protocol)
		}
		routeSegments := strings.Split(strings.TrimPrefix(entry.RoutePrefix, "/"), "/")
		if !frontendPluginRoutePrefixPattern.MatchString(entry.RoutePrefix) {
			return nil, fmt.Errorf("frontend plugin service %q has unsafe route prefix %q", owner, entry.RoutePrefix)
		}
		for _, segment := range routeSegments {
			if segment == "." || segment == ".." {
				return nil, fmt.Errorf("frontend plugin service %q has unsafe route prefix %q", owner, entry.RoutePrefix)
			}
		}
		if !frontendPluginLogicalIDPattern.MatchString(entry.Compatibility.Contract) || entry.Compatibility.Major <= 0 {
			return nil, fmt.Errorf("frontend plugin service %q has invalid compatibility metadata", owner)
		}
		if entry.Compatibility.ProbePath != nil {
			probePath := *entry.Compatibility.ProbePath
			probeSegments := strings.Split(strings.TrimPrefix(probePath, "/"), "/")
			if entry.Protocol != "rest" ||
				!frontendPluginRoutePrefixPattern.MatchString(probePath) {
				return nil, fmt.Errorf(
					"frontend plugin service %q has unsafe compatibility probe path %q",
					owner,
					probePath,
				)
			}
			for _, segment := range probeSegments {
				if segment == "." || segment == ".." {
					return nil, fmt.Errorf(
						"frontend plugin service %q has unsafe compatibility probe path %q",
						owner,
						probePath,
					)
				}
			}
		}
		if entry.Target.Endpoint != entry.Protocol {
			return nil, fmt.Errorf(
				"frontend plugin service %q endpoint %q disagrees with protocol %q",
				owner,
				entry.Target.Endpoint,
				entry.Protocol,
			)
		}
		if !endpointNamePattern.MatchString(entry.Target.Module) || !endpointNamePattern.MatchString(entry.Target.Service) {
			return nil, fmt.Errorf("frontend plugin service %q has unsafe Codefly target", owner)
		}
		key := entry.Target.Module + "/" + entry.Target.Service
		value := targets[key]
		if value == nil {
			value = &target{
				module:    entry.Target.Module,
				service:   entry.Target.Service,
				endpoints: make(map[string]bool),
			}
			targets[key] = value
		}
		value.endpoints[entry.Target.Endpoint] = true
	}

	keys := make([]string, 0, len(targets))
	for key := range targets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	dependencies := make([]manifestServiceDependency, 0, len(keys))
	for _, key := range keys {
		value := targets[key]
		endpoints := make([]string, 0, len(value.endpoints))
		for endpoint := range value.endpoints {
			endpoints = append(endpoints, endpoint)
		}
		sort.Strings(endpoints)
		dependency := manifestServiceDependency{Module: value.module, Name: value.service}
		for _, endpoint := range endpoints {
			dependency.Endpoints = append(
				dependency.Endpoints,
				manifestEndpointReference{Name: endpoint},
			)
		}
		dependencies = append(dependencies, dependency)
	}
	return dependencies, nil
}
