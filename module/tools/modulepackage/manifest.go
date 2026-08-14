package modulepackage

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const ManifestName = "module.package.codefly.yaml"

var (
	semverCorePattern   = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	semverLabelPattern  = regexp.MustCompile(`^[0-9A-Za-z-]+$`)
	numericLabelPattern = regexp.MustCompile(`^[0-9]+$`)
)

type Manifest struct {
	Schema                string              `yaml:"schema"`
	Kind                  string              `yaml:"kind"`
	ID                    string              `yaml:"id"`
	Version               string              `yaml:"version"`
	MinimumCodeflyVersion string              `yaml:"minimum-codefly-version"`
	Repository            string              `yaml:"repository"`
	Artifact              Artifact            `yaml:"artifact"`
	ProvidedServices      []Service           `yaml:"provided-services"`
	ContributionContracts []Contract          `yaml:"contribution-contracts"`
	Generators            []Command           `yaml:"generators"`
	ConformanceSuites     []Command           `yaml:"conformance-suites"`
	Release               Release             `yaml:"release"`
	ReservedNamespaces    []ReservedNamespace `yaml:"reserved-namespaces"`
}

type Artifact struct {
	MediaType   string            `yaml:"media-type"`
	Roots       []string          `yaml:"roots"`
	EntryPoints map[string]string `yaml:"entry-points"`
}

type Service struct {
	Name      string     `yaml:"name"`
	Endpoints []Endpoint `yaml:"endpoints"`
}

type Endpoint struct {
	Name       string `yaml:"name"`
	Protocol   string `yaml:"protocol"`
	Visibility string `yaml:"visibility"`
}

type Contract struct {
	ID          string            `yaml:"id"`
	Versions    string            `yaml:"versions"`
	EntryPoints map[string]string `yaml:"entry-points"`
}

type Command struct {
	ID               string   `yaml:"id"`
	WorkingDirectory string   `yaml:"working-directory"`
	Command          []string `yaml:"command"`
	Outputs          []string `yaml:"outputs"`
}

type Release struct {
	SupportedFrom   string           `yaml:"supported-from"`
	Migrations      []Migration      `yaml:"migrations"`
	BreakingChanges []BreakingChange `yaml:"breaking-changes"`
}

type Migration struct {
	From  string `yaml:"from"`
	Guide string `yaml:"guide"`
}

type BreakingChange struct {
	Version string `yaml:"version"`
	Summary string `yaml:"summary"`
}

type ReservedNamespace struct {
	Kind  string `yaml:"kind"`
	Value string `yaml:"value"`
}

func ReadManifest(moduleRoot string) (Manifest, error) {
	body, err := os.ReadFile(filepath.Join(moduleRoot, ManifestName))
	if err != nil {
		return Manifest{}, fmt.Errorf("read package manifest: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	decoder.KnownFields(true)
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode package manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Manifest{}, fmt.Errorf("decode package manifest: multiple YAML documents are not allowed")
		}
		return Manifest{}, fmt.Errorf("decode package manifest: %w", err)
	}
	if err := manifest.Validate(moduleRoot); err != nil {
		return Manifest{}, err
	}
	if topology, declared := manifest.Artifact.EntryPoints["topology"]; declared {
		if err := manifest.validateTopology(moduleRoot, topology); err != nil {
			return Manifest{}, err
		}
	}
	return manifest, nil
}

func (manifest Manifest) Validate(moduleRoot string) error {
	if manifest.Schema != "codefly/module-package/v2" {
		return fmt.Errorf("schema must be codefly/module-package/v2")
	}
	if manifest.Kind != "module-package" {
		return fmt.Errorf("kind must be module-package")
	}
	if manifest.ID == "" || strings.ContainsAny(manifest.ID, " \t\r\n") {
		return fmt.Errorf("id must be a non-empty package identity without whitespace")
	}
	if !isSemver(manifest.Version) {
		return fmt.Errorf("version %q is not semantic versioning", manifest.Version)
	}
	if !isSemver(manifest.MinimumCodeflyVersion) {
		return fmt.Errorf("minimum-codefly-version %q is not semantic versioning", manifest.MinimumCodeflyVersion)
	}
	if !strings.HasPrefix(manifest.Repository, "https://github.com/") || !strings.HasSuffix(manifest.Repository, ".git") {
		return fmt.Errorf("repository must be a canonical HTTPS GitHub clone URL")
	}
	if manifest.Artifact.MediaType != "application/vnd.codefly.module.v2+tar" {
		return fmt.Errorf("artifact media-type must be application/vnd.codefly.module.v2+tar")
	}
	if len(manifest.Artifact.Roots) == 0 {
		return fmt.Errorf("artifact must declare at least one root")
	}
	for _, root := range manifest.Artifact.Roots {
		if err := validatePath(moduleRoot, root, true); err != nil {
			return fmt.Errorf("artifact root %q: %w", root, err)
		}
	}
	if len(manifest.Artifact.EntryPoints) == 0 {
		return fmt.Errorf("artifact must declare entry-points")
	}
	for name, path := range manifest.Artifact.EntryPoints {
		if name == "" {
			return fmt.Errorf("artifact entry-point name cannot be empty")
		}
		if err := validatePath(moduleRoot, path, false); err != nil {
			return fmt.Errorf("artifact entry-point %q: %w", name, err)
		}
	}
	if err := validateServices(manifest.ProvidedServices); err != nil {
		return err
	}
	if len(manifest.ContributionContracts) == 0 {
		return fmt.Errorf("contribution-contracts cannot be empty")
	}
	contractIDs := map[string]struct{}{}
	for _, contract := range manifest.ContributionContracts {
		if contract.ID == "" {
			return fmt.Errorf("contribution contract id is required")
		}
		if !isSemverRange(contract.Versions) {
			return fmt.Errorf("contribution contract %q versions must be a bounded semantic version range", contract.ID)
		}
		if _, duplicate := contractIDs[contract.ID]; duplicate {
			return fmt.Errorf("contribution contract %q is duplicated", contract.ID)
		}
		contractIDs[contract.ID] = struct{}{}
		if len(contract.EntryPoints) == 0 {
			return fmt.Errorf("contribution contract %q has no entry-points", contract.ID)
		}
		for name, path := range contract.EntryPoints {
			if name == "" {
				return fmt.Errorf("contribution contract %q has an empty entry-point name", contract.ID)
			}
			if err := validatePath(moduleRoot, path, false); err != nil {
				return fmt.Errorf("contribution contract %q entry-point %q: %w", contract.ID, name, err)
			}
		}
	}
	if err := validateCommands(moduleRoot, "generator", manifest.Generators, true); err != nil {
		return err
	}
	if err := validateCommands(moduleRoot, "conformance suite", manifest.ConformanceSuites, false); err != nil {
		return err
	}
	if !isSemverRange(manifest.Release.SupportedFrom) {
		return fmt.Errorf("release supported-from must be a bounded semantic version range")
	}
	for _, migration := range manifest.Release.Migrations {
		if !isSemverRange(migration.From) {
			return fmt.Errorf("release migration from must be a bounded semantic version range")
		}
		if err := validatePath(moduleRoot, migration.Guide, false); err != nil {
			return fmt.Errorf("release migration guide: %w", err)
		}
	}
	for _, change := range manifest.Release.BreakingChanges {
		if !isSemver(change.Version) || change.Summary == "" {
			return fmt.Errorf("breaking change must have a semantic version and summary")
		}
	}
	if len(manifest.ReservedNamespaces) == 0 {
		return fmt.Errorf("reserved-namespaces cannot be empty")
	}
	reserved := map[string]struct{}{}
	for _, namespace := range manifest.ReservedNamespaces {
		if namespace.Kind == "" || namespace.Value == "" {
			return fmt.Errorf("reserved namespace kind and value are required")
		}
		key := namespace.Kind + "\x00" + namespace.Value
		if _, duplicate := reserved[key]; duplicate {
			return fmt.Errorf("reserved namespace %s %q is duplicated", namespace.Kind, namespace.Value)
		}
		reserved[key] = struct{}{}
	}
	return nil
}

func isSemver(version string) bool {
	withoutBuild, build, hasBuild := strings.Cut(version, "+")
	if hasBuild && !validSemverLabels(build, false) {
		return false
	}
	core, prerelease, hasPrerelease := strings.Cut(withoutBuild, "-")
	if !semverCorePattern.MatchString(core) {
		return false
	}
	return !hasPrerelease || validSemverLabels(prerelease, true)
}

func validSemverLabels(value string, rejectLeadingZero bool) bool {
	if value == "" {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if !semverLabelPattern.MatchString(label) {
			return false
		}
		if rejectLeadingZero && len(label) > 1 && label[0] == '0' && numericLabelPattern.MatchString(label) {
			return false
		}
	}
	return true
}

func isSemverRange(value string) bool {
	parts := strings.Fields(value)
	return len(parts) == 2 && strings.HasPrefix(parts[0], ">=") && strings.HasPrefix(parts[1], "<") &&
		isSemver(strings.TrimPrefix(parts[0], ">=")) && isSemver(strings.TrimPrefix(parts[1], "<"))
}

func validatePath(moduleRoot, relative string, allowDot bool) error {
	if relative == "" || filepath.IsAbs(relative) {
		return fmt.Errorf("path must be relative")
	}
	clean := filepath.Clean(relative)
	if clean == "." && !allowDot {
		return fmt.Errorf("path must name an artifact entry")
	}
	if clean != relative || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path must be clean and remain inside the module")
	}
	info, err := os.Lstat(filepath.Join(moduleRoot, clean))
	if err != nil {
		return fmt.Errorf("path does not exist: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlinks are not package entry-points")
	}
	return nil
}

func validateServices(services []Service) error {
	if len(services) == 0 {
		return fmt.Errorf("provided-services cannot be empty")
	}
	serviceNames := map[string]struct{}{}
	for _, service := range services {
		if service.Name == "" {
			return fmt.Errorf("provided service name cannot be empty")
		}
		if _, duplicate := serviceNames[service.Name]; duplicate {
			return fmt.Errorf("provided service %q is duplicated", service.Name)
		}
		serviceNames[service.Name] = struct{}{}
		endpointNames := map[string]struct{}{}
		for _, endpoint := range service.Endpoints {
			if endpoint.Name == "" || endpoint.Protocol == "" || endpoint.Visibility == "" {
				return fmt.Errorf("provided service %q has an incomplete endpoint", service.Name)
			}
			if _, duplicate := endpointNames[endpoint.Name]; duplicate {
				return fmt.Errorf("provided service %q endpoint %q is duplicated", service.Name, endpoint.Name)
			}
			endpointNames[endpoint.Name] = struct{}{}
		}
	}
	return nil
}

func (manifest Manifest) validateTopology(moduleRoot, relative string) error {
	body, err := os.ReadFile(filepath.Join(moduleRoot, relative))
	if err != nil {
		return fmt.Errorf("read topology entry-point: %w", err)
	}
	var topology struct {
		Services []struct {
			Name      string `yaml:"name"`
			Endpoints []struct {
				Name       string `yaml:"name"`
				Protocol   string `yaml:"api"`
				Visibility string `yaml:"visibility"`
			} `yaml:"endpoints"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(body, &topology); err != nil {
		return fmt.Errorf("decode topology entry-point: %w", err)
	}
	declared := make(map[string]map[string]Endpoint, len(manifest.ProvidedServices))
	for _, service := range manifest.ProvidedServices {
		endpoints := make(map[string]Endpoint, len(service.Endpoints))
		for _, endpoint := range service.Endpoints {
			endpoints[endpoint.Name] = endpoint
		}
		declared[service.Name] = endpoints
	}
	if len(declared) != len(topology.Services) {
		return fmt.Errorf("provided-services do not match topology: declared %d services, topology has %d", len(declared), len(topology.Services))
	}
	for _, service := range topology.Services {
		endpoints, found := declared[service.Name]
		if !found {
			return fmt.Errorf("topology service %q is missing from provided-services", service.Name)
		}
		if len(endpoints) != len(service.Endpoints) {
			return fmt.Errorf("provided service %q endpoints do not match topology", service.Name)
		}
		for _, endpoint := range service.Endpoints {
			candidate, found := endpoints[endpoint.Name]
			if !found || candidate.Protocol != endpoint.Protocol || candidate.Visibility != endpoint.Visibility {
				return fmt.Errorf("provided service %q endpoint %q does not match topology", service.Name, endpoint.Name)
			}
		}
	}
	return nil
}

func validateCommands(moduleRoot, kind string, commands []Command, requireOutputs bool) error {
	if len(commands) == 0 {
		return fmt.Errorf("at least one %s is required", kind)
	}
	ids := map[string]struct{}{}
	for _, command := range commands {
		if command.ID == "" || len(command.Command) == 0 || command.Command[0] == "" {
			return fmt.Errorf("%s id and command are required", kind)
		}
		if _, duplicate := ids[command.ID]; duplicate {
			return fmt.Errorf("%s %q is duplicated", kind, command.ID)
		}
		ids[command.ID] = struct{}{}
		if err := validatePath(moduleRoot, command.WorkingDirectory, true); err != nil {
			return fmt.Errorf("%s %q working-directory: %w", kind, command.ID, err)
		}
		if requireOutputs && len(command.Outputs) == 0 {
			return fmt.Errorf("%s %q must declare outputs", kind, command.ID)
		}
		for _, output := range command.Outputs {
			if output == "" || filepath.IsAbs(output) {
				return fmt.Errorf("%s %q output must be relative", kind, command.ID)
			}
			clean := filepath.Clean(output)
			if clean != output || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				return fmt.Errorf("%s %q output must remain inside the module", kind, command.ID)
			}
		}
	}
	return nil
}
