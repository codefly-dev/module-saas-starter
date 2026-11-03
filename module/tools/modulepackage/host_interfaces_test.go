package modulepackage

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func publishedFixture(t *testing.T, versions map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, version := range versions {
		writeFile(t, filepath.Join(dir, name, "interface.codefly.yaml"),
			"kind: interface\npublisher: codefly.dev\nname: "+name+"\nversion: "+version+"\ntype: connect\n")
	}
	return dir
}

// The real tree: accounts declares the authority interface, pins the shared
// suite at one revision, and its document names what the manifest declares.
// This half of the gate needs no network, so it runs with the package tests;
// the published half runs in CI against authority's tree at the pin.
func TestAccountsDeclaresHostInterfacesItPinsOneSuiteFor(t *testing.T) {
	moduleRoot := findModuleRoot(t)
	declared, err := DeclaredHostInterfaces(moduleRoot)
	if err != nil {
		t.Fatal(err)
	}
	var authority *HostInterfaceDeclaration
	for i := range declared {
		if declared[i].Service == "accounts" && declared[i].ID() == "codefly.dev/authority" {
			authority = &declared[i]
		}
	}
	if authority == nil {
		t.Fatalf("accounts declares no codefly.dev/authority interface; declared %+v", declared)
	}
	pin, err := ReadAuthorityPin(filepath.Join(moduleRoot, "services", "accounts", "code", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pin.Revision) < 12 {
		t.Fatalf("pin revision %q is not a commit abbreviation", pin.Revision)
	}
	documented, err := DocumentedHostInterfaces(filepath.Join(moduleRoot, "services", "accounts", "HOST_INTERFACE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if documented == nil {
		t.Fatal("accounts has no HOST_INTERFACE.md to hold to the manifest")
	}
	for _, d := range declared {
		if d.Service != "accounts" {
			continue
		}
		if versions := documented[d.ID()]; len(versions) != 1 || versions[0] != d.Version {
			t.Errorf("HOST_INTERFACE.md names %s %v where the manifest declares %s", d.ID(), versions, d.Version)
		}
	}
}

func TestReadAuthorityPinRequiresOnePseudoVersionedRevision(t *testing.T) {
	cases := map[string]struct {
		goMod string
		want  string // substring of the error, or "" for success
	}{
		"one revision": {goMod: `module accounts

require (
	github.com/codefly-dev/authority/libraries/authority-core/go v0.0.0-20261030222015-dea37afac9ad
	github.com/codefly-dev/authority/libraries/conformance/go v0.0.0-20261030222015-dea37afac9ad
	github.com/codefly-dev/authority/libraries/authority-client/go v0.1.1-0.20261030222015-dea37afac9ad
	github.com/codefly-dev/authority/libraries/hop/go v0.0.0-20261027154629-b57068f61a0f // indirect
)
`},
		"two revisions": {goMod: `module accounts

require (
	github.com/codefly-dev/authority/libraries/authority-core/go v0.0.0-20261030222015-dea37afac9ad
	github.com/codefly-dev/authority/libraries/conformance/go v0.0.0-20261026111022-6870db87da8b
)
`, want: "two revisions, 6870db87da8b (conformance) and dea37afac9ad (authority-core)"},
		"a tag": {goMod: `module accounts

require github.com/codefly-dev/authority/libraries/conformance/go v0.1.0
`, want: "names no commit"},
		"no suite": {goMod: `module accounts

require github.com/codefly-dev/authority/libraries/authority-core/go v0.0.0-20261030222015-dea37afac9ad
`, want: "pins no github.com/codefly-dev/authority/libraries/conformance/go"},
		"no authority": {goMod: "module accounts\n\nrequire github.com/google/uuid v1.6.0\n", want: "pins no github.com/codefly-dev/authority/libraries/* library"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "go.mod")
			writeFile(t, path, tc.goMod)
			pin, err := ReadAuthorityPin(path)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if pin.Revision != "dea37afac9ad" || len(pin.Libraries) != 3 {
					t.Fatalf("pin %+v", pin)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestReadPublishedInterfacesReadsEachInterfaceByItsDirectory(t *testing.T) {
	dir := publishedFixture(t, map[string]string{"authority": "0.3.5", "session": "0.2.1"})
	writeFile(t, filepath.Join(dir, "notes.md"), "not an interface\n")
	published, err := ReadPublishedInterfaces(dir)
	if err != nil {
		t.Fatal(err)
	}
	if published["codefly.dev/authority"].Version != "0.3.5" || published["codefly.dev/session"].Version != "0.2.1" || len(published) != 2 {
		t.Fatalf("published %+v", published)
	}
	writeFile(t, filepath.Join(dir, "artifact", "interface.codefly.yaml"), "publisher: codefly.dev\nname: authority\nversion: 0.1.0\n")
	if _, err := ReadPublishedInterfaces(dir); err == nil || !strings.Contains(err.Error(), `names interface "authority", not "artifact"`) {
		t.Fatalf("a directory publishing another interface's manifest was read: %v", err)
	}
}

func TestDocumentedHostInterfacesReadsTheOpeningSectionAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "HOST_INTERFACE.md")
	writeFile(t, path, "# Title\n\naccounts implements `codefly.dev/authority@0.3.5` and `codefly.dev/session@0.2.1`.\n\n## Later\n\nThe keys arrived in `codefly.dev/authority@0.3.3`.\n")
	documented, err := DocumentedHostInterfaces(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := documented["codefly.dev/authority"]; len(got) != 1 || got[0] != "0.3.5" {
		t.Fatalf("authority %v: a later section's historical version was read as the claim", got)
	}
	if got := documented["codefly.dev/session"]; len(got) != 1 || got[0] != "0.2.1" {
		t.Fatalf("session %v", got)
	}
	none, err := DocumentedHostInterfaces(filepath.Join(t.TempDir(), "absent.md"))
	if err != nil || none != nil {
		t.Fatalf("an absent document: %v %v", none, err)
	}
}

func TestCompareHostInterfacesJudgesEachDeclaration(t *testing.T) {
	published := map[string]PublishedInterface{
		"codefly.dev/authority":    {Publisher: "codefly.dev", Name: "authority", Version: "0.3.5"},
		"codefly.dev/session":      {Publisher: "codefly.dev", Name: "session", Version: "0.2.1"},
		"codefly.dev/installation": {Publisher: "codefly.dev", Name: "installation", Version: "0.4.0"},
		"codefly.dev/composition":  {Publisher: "codefly.dev", Name: "composition", Version: "0.2.0"},
	}
	declare := func(name, version string) HostInterfaceDeclaration {
		return HostInterfaceDeclaration{Service: "accounts", Publisher: "codefly.dev", Name: name, Version: version}
	}
	findings := CompareHostInterfaces([]HostInterfaceDeclaration{
		declare("authority", "0.3.5"),    // equal: ok
		declare("session", "0.2.0"),      // below: a notice
		declare("installation", "0.5.0"), // above: not proven
		declare("artifact", "0.1.0"),     // not published at the pin
		declare("composition", "0.2"),    // not semver
		{Service: "accounts", Publisher: "example.com", Name: "other", Version: "1.0.0"},
	}, published, nil)
	if len(findings) != 6 {
		t.Fatalf("%d findings: %+v", len(findings), findings)
	}
	expect := []struct {
		failure bool
		reason  string
	}{
		{false, ""},
		{false, "the pinned suite proves 0.2.1"},
		{true, "claims a version the pinned suite does not prove"},
		{true, "publishes no such interface"},
		{true, "not semver"},
		{false, "not published by codefly-dev/authority"},
	}
	for i, want := range expect {
		got := findings[i]
		if got.Failure != want.failure || !strings.Contains(got.Reason, want.reason) {
			t.Errorf("%s: failure=%v reason=%q, want failure=%v reason containing %q", got.Interface, got.Failure, got.Reason, want.failure, want.reason)
		}
	}
}

func TestCompareHostInterfacesHoldsTheDocumentToTheManifest(t *testing.T) {
	published := map[string]PublishedInterface{
		"codefly.dev/authority": {Publisher: "codefly.dev", Name: "authority", Version: "0.3.5"},
		"codefly.dev/session":   {Publisher: "codefly.dev", Name: "session", Version: "0.2.1"},
	}
	declared := []HostInterfaceDeclaration{
		{Service: "accounts", Publisher: "codefly.dev", Name: "authority", Version: "0.3.5"},
		{Service: "accounts", Publisher: "codefly.dev", Name: "session", Version: "0.2.1"},
	}
	agreeing := map[string][]string{"codefly.dev/authority": {"0.3.5"}, "codefly.dev/session": {"0.2.1"}}
	for _, f := range CompareHostInterfaces(declared, published, agreeing) {
		if f.Failure {
			t.Errorf("%s failed with an agreeing document: %s", f.Interface, f.Reason)
		}
	}
	drifted := map[string][]string{"codefly.dev/authority": {"0.3.5"}, "codefly.dev/session": {"0.2.0"}}
	findings := CompareHostInterfaces(declared, published, drifted)
	if !findings[1].Failure || !strings.Contains(findings[1].Reason, "names 0.2.0 where the manifest declares 0.2.1") {
		t.Fatalf("a document naming another version passed: %+v", findings[1])
	}
	silent := map[string][]string{"codefly.dev/authority": {"0.3.5"}}
	if findings := CompareHostInterfaces(declared, published, silent); !findings[1].Failure || !strings.Contains(findings[1].Reason, "does not name this interface") {
		t.Fatalf("a document naming no version passed: %+v", findings[1])
	}
}

// End to end over a module fixture: the manifest, the service's go.mod and its
// document, against a published tree. A declaration above the pin fails the
// run and is named with both versions.
func TestVerifyHostInterfacesFailsADeclarationAboveThePin(t *testing.T) {
	moduleRoot := t.TempDir()
	writeFile(t, filepath.Join(moduleRoot, "module.codefly.yaml"), `kind: module
name: fixture
interface:
    endpoints:
        - service: accounts
          endpoint: connect
          visibility: internal
          implements:
              - codefly.dev/authority@0.3.5
              - codefly.dev/session@0.2.1
services:
    - name: accounts
`)
	// Core's module loader resolves every service an interface entry names and
	// requires the endpoint it serves on, so the fixture carries the manifest.
	writeFile(t, filepath.Join(moduleRoot, "services", "accounts", "service.codefly.yaml"), `name: accounts
version: 0.0.0
agent:
    kind: codefly:service
    name: go-grpc
    version: 0.1.72
    publisher: codefly.dev
endpoints:
    - name: connect
      visibility: internal
`)
	writeFile(t, filepath.Join(moduleRoot, "services", "accounts", "code", "go.mod"), `module accounts

require (
	github.com/codefly-dev/authority/libraries/authority-core/go v0.0.0-20261030222015-dea37afac9ad
	github.com/codefly-dev/authority/libraries/conformance/go v0.0.0-20261030222015-dea37afac9ad
)
`)
	writeFile(t, filepath.Join(moduleRoot, "services", "accounts", "HOST_INTERFACE.md"),
		"# Host interfaces\n\naccounts implements `codefly.dev/authority@0.3.5` and `codefly.dev/session@0.2.1`.\n")

	var out bytes.Buffer
	err := VerifyHostInterfaces(HostInterfaceOptions{ModuleRoot: moduleRoot,
		InterfacesDir: publishedFixture(t, map[string]string{"authority": "0.3.5", "session": "0.2.1"}),
		Revision:      "dea37afac9adda288c7b9fc7df0dc42a2746e47f"}, &out)
	if err != nil {
		t.Fatalf("an agreeing tree failed: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "codefly.dev/authority") || !strings.Contains(out.String(), "dea37afac9ad") {
		t.Fatalf("the report names neither the interface nor the pin:\n%s", out.String())
	}

	out.Reset()
	err = VerifyHostInterfaces(HostInterfaceOptions{ModuleRoot: moduleRoot,
		InterfacesDir: publishedFixture(t, map[string]string{"authority": "0.3.4", "session": "0.2.1"})}, &out)
	if err == nil || !strings.Contains(err.Error(), "codefly.dev/authority declared 0.3.5, published 0.3.4") {
		t.Fatalf("a declaration above the pin passed: %v\n%s", err, out.String())
	}

	err = VerifyHostInterfaces(HostInterfaceOptions{ModuleRoot: moduleRoot,
		InterfacesDir: publishedFixture(t, map[string]string{"authority": "0.3.5", "session": "0.2.1"}),
		Revision:      "0000000000000000000000000000000000000000"}, &out)
	if err == nil || !strings.Contains(err.Error(), "pins dea37afac9ad") {
		t.Fatalf("a tree read at another revision than the pin passed: %v", err)
	}
}
