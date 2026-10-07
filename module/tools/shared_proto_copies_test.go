package tools

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A service generates only from its own proto directory, and its
// buf.gen.local.yaml sets `clean: true` — so a generated file whose proto is not
// in that directory is deleted by the next regeneration, and Codefly's
// sync-drift phase reports it as drift. That is exactly what happened to
// auth-gateway's saas/accounts/v1/module_registration.pb.go: #527 committed the
// generated Go without the proto that produces it, and main's "Codefly quality"
// gate stayed red until the proto was copied in beside it.
//
// The copy is the price of the services being separate Go modules —
// auth-gateway cannot import accounts/pkg/gen. This test is what keeps the price
// from being paid twice: the wire contract one service compiles against must be
// the one its peer publishes, byte for byte, or the two ends of a call can
// disagree while both compile.
//
// The set of shared files is DERIVED, not enumerated. It was a hand-written map
// of two paths, and the cost of that was paid in full: `solution_registry.proto`
// was copied into auth-gateway's tree without being added to the map, so when
// the accounts copy reserved field 9 and moved `declared` to 10, the gateway
// kept decoding field 9. Both services compiled, every in-process fake agreed
// with itself because it built the message with the same struct, and
// `GetDeclared()` returned nil on every real response from accounts. A list a
// human must remember to extend is blind to exactly the file nobody remembered.
func TestSharedProtoCopiesMatchTheirSource(t *testing.T) {
	moduleDir := findModuleDir(t)
	servicesDir := filepath.Join(moduleDir, "services")

	entries, err := os.ReadDir(servicesDir)
	if err != nil {
		t.Fatalf("read services: %v", err)
	}

	// owners maps a proto's path relative to a service's proto tree to every
	// service that carries a copy of it.
	owners := map[string][]string{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		protoDir := filepath.Join(servicesDir, entry.Name(), "proto")
		if info, err := os.Stat(protoDir); err != nil || !info.IsDir() {
			continue
		}
		err := filepath.WalkDir(protoDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".proto") {
				return nil
			}
			rel, err := filepath.Rel(protoDir, path)
			if err != nil {
				return err
			}
			key := filepath.ToSlash(rel)
			owners[key] = append(owners[key], entry.Name())
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", protoDir, err)
		}
	}

	shared := 0
	keys := make([]string, 0, len(owners))
	for key := range owners {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		services := owners[key]
		if len(services) < 2 {
			continue
		}
		shared++
		sort.Strings(services)
		source := services[0]
		sourceBody, err := os.ReadFile(filepath.Join(servicesDir, source, "proto", filepath.FromSlash(key)))
		if err != nil {
			t.Fatalf("read %s/%s: %v", source, key, err)
		}
		for _, service := range services[1:] {
			copyBody, err := os.ReadFile(filepath.Join(servicesDir, service, "proto", filepath.FromSlash(key)))
			if err != nil {
				t.Fatalf("read %s/%s: %v", service, key, err)
			}
			if string(copyBody) != string(sourceBody) {
				t.Errorf("services/%s/proto/%s has diverged from services/%s/proto/%s; copy the source over it and regenerate, so both services encode the same wire contract",
					service, key, source, key)
			}
		}
	}

	// A derived set that found nothing would pass silently if the walk broke or
	// the layout moved, which is the failure mode the enumerated map had.
	if shared == 0 {
		t.Fatal("no proto is shared between two services: the walk found nothing to compare, so this gate is not enforcing anything")
	}
	t.Logf("compared %d shared proto path(s) across service trees", shared)
}
