package main

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

// Both Go and Go-gRPC can build Go source. Publication must use the module
// owner's declared packager, not whichever happens to be installed alone.
func TestModuleReleaseSelectsItsSourceAgent(t *testing.T) {
	raw, err := os.ReadFile("agent.codefly.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Kind   string `yaml:"kind"`
		Source struct {
			Directory string `yaml:"directory"`
			Agent     string `yaml:"agent"`
		} `yaml:"source"`
	}
	if err := yaml.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Kind != "codefly:module" || manifest.Source.Directory != "." || manifest.Source.Agent == "" {
		t.Fatalf("module release must declare its root source and packager: %+v", manifest)
	}
}
