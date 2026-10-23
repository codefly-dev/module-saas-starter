// Package auditstorage reads the `audit-storage` workspace group: which backend
// keeps the audit trail, and the names of the warehouse dataset and table when
// that backend is the warehouse.
//
// The group carries names and nothing else. A warehouse is reached through the
// service the composition provides, resolved at startup like every other
// endpoint, so no endpoint, address, project, credential or token is configured
// here, and none may be added to the group.
package auditstorage

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	codefly "github.com/codefly-dev/sdk-go"

	"accounts/pkg/business"
)

// ConfigurationGroup is the workspace group that selects the audit storage.
const ConfigurationGroup = "audit-storage"

// The keys the group carries. Values only: a dataset or table name is not a
// secret, and the group holds no credential.
const (
	// BackendKey selects where audit entries are kept: `postgres` (the default,
	// audit_events) or `warehouse`.
	BackendKey = "AUDIT_STORAGE_BACKEND"
	// DatasetKey names the warehouse dataset. Required for `warehouse`.
	DatasetKey = "AUDIT_STORAGE_DATASET"
	// TableKey names the warehouse table. Required for `warehouse`.
	TableKey = "AUDIT_STORAGE_TABLE"
)

// Config is what the group resolved to.
type Config struct {
	Backend business.AuditBackend
	// Dataset and Table name the warehouse table entries are appended to. They
	// are empty, and ignored, under the Postgres backend.
	Dataset string
	Table   string
}

// name is the shape a dataset or table name may take: letters, digits,
// underscores and dashes, not starting with a dash, at most maxNameLength long.
// It names no project, path or address, so a value that does is a mistake caught
// at startup.
var name = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]*$`)

const maxNameLength = 1024

// Load reads the `audit-storage` group, falling back to a plain process
// variable exactly as the rest of accounts' workspace reads do.
func Load(ctx context.Context) (Config, error) {
	return LoadFrom(func(key string) string {
		value, err := codefly.For(ctx).WorkspaceValue(ConfigurationGroup, key)
		if err != nil || value == "" {
			value = os.Getenv(key)
		}
		return value
	})
}

// LoadFrom resolves the group from read, which answers one key.
func LoadFrom(read func(key string) string) (Config, error) {
	backend, err := business.ParseAuditBackend(read(BackendKey))
	if err != nil {
		return Config{}, fmt.Errorf("%s in the `%s` group: %w", BackendKey, ConfigurationGroup, err)
	}
	config := Config{
		Backend: backend,
		Dataset: strings.TrimSpace(read(DatasetKey)),
		Table:   strings.TrimSpace(read(TableKey)),
	}
	if backend != business.AuditBackendWarehouse {
		return config, nil
	}
	for _, key := range []struct{ name, value string }{{DatasetKey, config.Dataset}, {TableKey, config.Table}} {
		if key.value == "" {
			return Config{}, fmt.Errorf("the `%s` audit backend requires %s in the `%s` group: it names the warehouse table the audit trail is appended to", backend, key.name, ConfigurationGroup)
		}
		if len(key.value) > maxNameLength || !name.MatchString(key.value) {
			return Config{}, fmt.Errorf("%s in the `%s` group is %q, which is not a dataset or table name: use letters, digits, underscores and dashes, and no project, path or address", key.name, ConfigurationGroup, key.value)
		}
	}
	return config, nil
}
