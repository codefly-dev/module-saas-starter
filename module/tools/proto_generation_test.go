package tools

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestMergedProtocolGeneratorsUseOneInvocation prevents Buf from invoking
// merge-style generators once per source directory. Both OpenAPI and the
// browser client include multiple protocol trees; directory strategy would
// emit duplicate names and make checked-in generation drift nondeterministic.
func TestMergedProtocolGeneratorsUseOneInvocation(t *testing.T) {
	moduleRoot := findModuleRoot(t)
	for _, relative := range []string{
		"services/accounts/proto/buf.gen.yaml",
		"services/accounts/buf.gen.local.yaml",
	} {
		path := filepath.Join(moduleRoot, relative)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		var config struct {
			Plugins []struct {
				Local    any    `yaml:"local"`
				Strategy string `yaml:"strategy"`
			} `yaml:"plugins"`
		}
		if err := yaml.Unmarshal(body, &config); err != nil {
			t.Fatalf("parse %s: %v", relative, err)
		}
		for _, plugin := range config.Plugins {
			name := localPluginName(plugin.Local)
			if name != "protoc-gen-openapiv2" && name != "protoc-gen-es" {
				continue
			}
			if plugin.Strategy != "all" {
				t.Errorf("%s: %s strategy = %q, want all", relative, name, plugin.Strategy)
			}
		}
	}
}

// TestOpenAPIDocumentHasOneGenerator keeps `generated/openapi-raw` a
// companion-only output. Codefly regenerates it through the versioned proto
// companion image and compares bytes; the companion's well-known types come
// from the buf it carries, not from anything pinnable in a template here, so a
// local run emits a different google.protobuf.NullValue description and drifts
// a file the contributor's change never touched (#872). A local template that
// declares the plugin makes the documented local path produce bytes CI rejects.
func TestOpenAPIDocumentHasOneGenerator(t *testing.T) {
	moduleRoot := findModuleRoot(t)
	const companion = "services/accounts/proto/buf.gen.yaml"
	if !declaresOpenAPI(t, moduleRoot, companion) {
		t.Errorf("%s: no protoc-gen-openapiv2 plugin; the OpenAPI document has lost its generator", companion)
	}
	for _, relative := range localTemplates(t, moduleRoot) {
		if declaresOpenAPI(t, moduleRoot, relative) {
			t.Errorf("%s: declares protoc-gen-openapiv2; only %s may generate the OpenAPI document", relative, companion)
		}
	}
}

// Every local generation template shipped by a service, module-root-relative.
func localTemplates(t *testing.T, moduleRoot string) []string {
	t.Helper()
	var templates []string
	servicesRoot := filepath.Join(moduleRoot, "services")
	err := filepath.WalkDir(servicesRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		matched, err := filepath.Match("buf.gen.local*.yaml", entry.Name())
		if err != nil || !matched {
			return err
		}
		relative, err := filepath.Rel(moduleRoot, path)
		if err != nil {
			return err
		}
		templates = append(templates, relative)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) == 0 {
		t.Fatal("no local generation template found")
	}
	return templates
}

func declaresOpenAPI(t *testing.T, moduleRoot, relative string) bool {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(moduleRoot, relative))
	if err != nil {
		t.Fatalf("read %s: %v", relative, err)
	}
	var config struct {
		Plugins []struct {
			Local any `yaml:"local"`
		} `yaml:"plugins"`
	}
	if err := yaml.Unmarshal(body, &config); err != nil {
		t.Fatalf("parse %s: %v", relative, err)
	}
	for _, plugin := range config.Plugins {
		if localPluginName(plugin.Local) == "protoc-gen-openapiv2" {
			return true
		}
	}
	return false
}

func findModuleRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "module.codefly.yaml")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("Codefly module root not found")
		}
		directory = parent
	}
}

func localPluginName(value any) string {
	switch typed := value.(type) {
	case string:
		return namedProtocolPlugin(typed)
	case []any:
		for _, item := range typed {
			if candidate, ok := item.(string); ok {
				if name := namedProtocolPlugin(candidate); name != "" {
					return name
				}
			}
		}
	}
	return ""
}

func namedProtocolPlugin(value string) string {
	for _, name := range []string{"protoc-gen-openapiv2", "protoc-gen-es"} {
		if strings.Contains(value, name) {
			return name
		}
	}
	return ""
}
