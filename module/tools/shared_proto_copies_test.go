package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// sharedProtoCopies are protobuf files that exist in more than one service's
// proto tree, keyed by the copy and valued by the definition it must match.
//
// A service generates only from its own proto directory, and its
// buf.gen.local.yaml sets `clean: true` — so a generated file whose proto is not
// in that directory is deleted by the next regeneration, and Codefly's
// sync-drift phase reports it as drift. That is exactly what happened to
// auth-gateway's saas/accounts/v1/module_registration.pb.go: #527 committed the
// generated Go without the proto that produces it, and main's "Codefly quality"
// gate stayed red until the proto was copied in beside it.
//
// The copy is the price of the two services being separate Go modules —
// auth-gateway cannot import accounts/pkg/gen. This test is what keeps the price
// from being paid twice: the wire contract the gateway compiles against must be
// the one accounts publishes, byte for byte, or the two ends of the federation
// handshake can disagree while both compile.
var sharedProtoCopies = map[string]string{
	"services/auth-gateway/proto/saas/accounts/v1/module_registration.proto": "services/accounts/proto/saas/accounts/v1/module_registration.proto",
}

func TestSharedProtoCopiesMatchTheirSource(t *testing.T) {
	moduleDir := findModuleDir(t)
	for copyPath, sourcePath := range sharedProtoCopies {
		copyBody, err := os.ReadFile(filepath.Join(moduleDir, filepath.FromSlash(copyPath)))
		if err != nil {
			t.Fatalf("read shared proto copy %s: %v", copyPath, err)
		}
		sourceBody, err := os.ReadFile(filepath.Join(moduleDir, filepath.FromSlash(sourcePath)))
		if err != nil {
			t.Fatalf("read shared proto source %s: %v", sourcePath, err)
		}
		if string(copyBody) != string(sourceBody) {
			t.Errorf("%s has diverged from %s; copy the source over it so both services generate the same wire contract",
				copyPath, sourcePath)
		}
	}
}
