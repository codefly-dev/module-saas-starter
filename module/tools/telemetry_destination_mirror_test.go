package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// telemetryDestinationCopies are source files that exist in both Go services,
// keyed by the copy and valued by the file it must match.
//
// accounts and auth-gateway both resolve where the cell's collector is from the
// `observability` configuration group: the same three keys, the same three
// outcomes, the same refusal to start on anything else. They are separate Go
// modules with no go.work, so neither can import the other, and a shared module
// would be a third thing to version for one small decision. The copy is the
// price; this test keeps the price from being paid twice, because two producers
// that read the group differently would let one service export while the other
// refuses to start on the same configuration.
var telemetryDestinationCopies = map[string]string{
	"services/auth-gateway/code/telemetry_destination.go": "services/accounts/code/telemetry_destination.go",
}

func TestTelemetryDestinationCopiesMatchTheirSource(t *testing.T) {
	moduleDir := findModuleDir(t)
	for copyPath, sourcePath := range telemetryDestinationCopies {
		copyBody, err := os.ReadFile(filepath.Join(moduleDir, filepath.FromSlash(copyPath)))
		if err != nil {
			t.Fatalf("read telemetry destination copy %s: %v", copyPath, err)
		}
		sourceBody, err := os.ReadFile(filepath.Join(moduleDir, filepath.FromSlash(sourcePath)))
		if err != nil {
			t.Fatalf("read telemetry destination source %s: %v", sourcePath, err)
		}
		if string(copyBody) != string(sourceBody) {
			t.Errorf("%s has diverged from %s; change the source and copy it over the other so both services resolve the collector the same way",
				copyPath, sourcePath)
		}
	}
}
