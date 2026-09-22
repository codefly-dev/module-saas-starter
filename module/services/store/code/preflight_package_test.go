package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

func TestServicePreflightPackage(t *testing.T) {
	read := func(path string) []byte {
		t.Helper()
		b, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	var manifest struct {
		Contract string            `json:"contract_version"`
		Name     string            `json:"name"`
		Version  string            `json:"version"`
		Files    map[string]string `json:"files"`
	}
	if err := json.Unmarshal(read("../preflight/manifest.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Contract != "codefly.dev/postgres-preflight-package/v1" || manifest.Name != "accounts-empty-baseline" || manifest.Version != "1.0.0" || len(manifest.Files) != 2 {
		t.Fatal("unexpected package identity")
	}
	for _, name := range []string{"policy.json", "observe.sql"} {
		sum := sha256.Sum256(read("../preflight/" + name))
		if manifest.Files[name] != hex.EncodeToString(sum[:]) {
			t.Fatalf("reviewed %s digest differs", name)
		}
	}
	var policy struct {
		Roles      []string `json:"application_roles"`
		Types      []string `json:"collision_types"`
		Extensions []string `json:"extensions"`
		Contract   string   `json:"contract_version"`
	}
	if err := json.Unmarshal(read("../preflight/policy.json"), &policy); err != nil {
		t.Fatal(err)
	}
	if policy.Contract != "codefly.dev/postgres-empty-baseline-policy/v1" {
		t.Fatal("unsupported classifier contract")
	}
	// Domain inventories remain beside their migrations. Adding a domain role,
	// enum or extension updates this owner package, never an operator source file.
	var source []byte
	files, err := filepath.Glob("../migrations/*.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		source = append(source, read(file)...)
	}
	for _, check := range []struct {
		label, pattern string
		declared       []string
	}{
		{"roles", `CREATE ROLE (app_[a-z_]+)`, policy.Roles},
		{"types", `CREATE TYPE (?:public\.)?([a-z_]+) AS ENUM`, policy.Types},
		{"extensions", `CREATE EXTENSION IF NOT EXISTS "?([a-z_-]+)"?(?: WITH SCHEMA [a-z_]+)?;`, policy.Extensions},
	} {
		expected := map[string]bool{}
		for _, match := range regexp.MustCompile(check.pattern).FindAllSubmatch(source, -1) {
			expected[string(match[1])] = true
		}
		if len(expected) != len(check.declared) {
			t.Fatalf("%s source inventory differs from preflight: %v vs %v", check.label, expected, check.declared)
		}
		for value := range expected {
			if !slices.Contains(check.declared, value) {
				t.Fatalf("missing %s preflight requirement %s", check.label, value)
			}
		}
	}
}
