package tools

import (
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
