package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Tenant-isolation attack battery, configuration and database half. Each test
// checks one finding from the tenant-isolation audit against the shipped module
// tree and asserts the secure state; each was red before the fix it names.

// TestAttack_TenantRoleCannotWritePlatformAdmins: platform_admins has no RLS and
// app_tenant holds INSERT, UPDATE and DELETE on it, so any write reachable from
// a tenant transaction — an injection, a bug, a confused handler — can mint a
// platform administrator.
func TestAttack_TenantRoleCannotWritePlatformAdmins(t *testing.T) {
	held := tablePrivileges(t, "platform_admins", "app_tenant")
	for _, write := range []string{"INSERT", "UPDATE", "DELETE"} {
		if held[write] {
			t.Errorf("app_tenant holds %s on platform_admins", write)
		}
	}
}

// tablePrivileges replays every GRANT and REVOKE on one table for one role
// across the ordered store migrations and returns what the role holds at the end.
func tablePrivileges(t *testing.T, table, role string) map[string]bool {
	t.Helper()
	statement := regexp.MustCompile(`(GRANT|REVOKE) ([A-Z, ]+) ON TABLE public\.` + table + ` (?:TO|FROM) ` + role + `;`)
	held := map[string]bool{}
	for _, migration := range upMigrations(t) {
		for _, match := range statement.FindAllStringSubmatch(migration, -1) {
			for _, privilege := range strings.Split(match[2], ",") {
				privilege = strings.TrimSpace(privilege)
				if privilege == "ALL" {
					for _, each := range []string{"SELECT", "INSERT", "UPDATE", "DELETE"} {
						held[each] = match[1] == "GRANT"
					}
					continue
				}
				held[privilege] = match[1] == "GRANT"
			}
		}
	}
	return held
}

// upMigrations returns the store's up migrations in version order.
func upMigrations(t *testing.T) []string {
	t.Helper()
	directory := filepath.Join(findModuleDir(t), "services", "store", "migrations")
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read %s: %v", directory, err)
	}
	type versioned struct {
		version int
		name    string
	}
	var files []versioned
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		version, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			t.Fatalf("migration %s has no numeric version", name)
		}
		files = append(files, versioned{version, name})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].version < files[j].version })
	var out []string
	for _, file := range files {
		out = append(out, readModuleFile(t, "services/store/migrations/"+file.name))
	}
	return out
}
