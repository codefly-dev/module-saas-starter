package auditstorage

import (
	"strings"
	"testing"

	"accounts/pkg/business"
)

func values(m map[string]string) func(string) string {
	return func(key string) string { return m[key] }
}

func TestLoadDefaultsToPostgres(t *testing.T) {
	for _, raw := range []string{"", "postgres", " POSTGRES "} {
		config, err := LoadFrom(values(map[string]string{BackendKey: raw}))
		if err != nil || config.Backend != business.AuditBackendPostgres {
			t.Fatalf("backend %q = %+v %v", raw, config, err)
		}
	}
	// Names under Postgres are carried but never required.
	config, err := LoadFrom(values(map[string]string{DatasetKey: "audit"}))
	if err != nil || config.Dataset != "audit" {
		t.Fatalf("config = %+v %v", config, err)
	}
}

func TestLoadWarehouseNeedsBothNames(t *testing.T) {
	config, err := LoadFrom(values(map[string]string{BackendKey: "warehouse", DatasetKey: " audit_v1 ", TableKey: "entries-2026"}))
	if err != nil {
		t.Fatal(err)
	}
	if config.Backend != business.AuditBackendWarehouse || config.Dataset != "audit_v1" || config.Table != "entries-2026" {
		t.Fatalf("config = %+v", config)
	}
	for name, in := range map[string]map[string]string{
		"no dataset": {BackendKey: "warehouse", TableKey: "t"},
		"no table":   {BackendKey: "warehouse", DatasetKey: "d"},
	} {
		if _, err := LoadFrom(values(in)); err == nil || !strings.Contains(err.Error(), ConfigurationGroup) {
			t.Errorf("%s: err = %v, want a refusal naming the group", name, err)
		}
	}
}

// The group carries names, and a value that looks like a project, a path or an
// address is refused rather than quietly passed on.
func TestLoadRefusesWhatIsNotAName(t *testing.T) {
	for _, bad := range []string{"project.dataset", "my-project:audit", "a/b", "https://example.com", "-leading", "has space", strings.Repeat("a", 1025)} {
		in := map[string]string{BackendKey: "warehouse", DatasetKey: bad, TableKey: "t"}
		if _, err := LoadFrom(values(in)); err == nil {
			t.Errorf("dataset %q was accepted", bad)
		}
		in = map[string]string{BackendKey: "warehouse", DatasetKey: "d", TableKey: bad}
		if _, err := LoadFrom(values(in)); err == nil {
			t.Errorf("table %q was accepted", bad)
		}
	}
}

func TestLoadRefusesAnUnknownBackend(t *testing.T) {
	_, err := LoadFrom(values(map[string]string{BackendKey: "clickhouse"}))
	if err == nil || !strings.Contains(err.Error(), BackendKey) {
		t.Fatalf("err = %v", err)
	}
}
