//go:build !pure

package infra_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Tenant-isolation attack guards. Each test replays a common escalation from a
// tenant transaction that the database already refuses, and pins that refusal
// so a later grant, extension or role change cannot reopen it silently. Unlike
// the attack battery these are green today.

// TestGuard_BareLoginHoldsNoTablePrivilege: SET ROLE NONE drops a tenant
// transaction to the runtime login itself. With runtime roles configured the
// login must hold role membership only, never a table privilege of its own.
func TestGuard_BareLoginHoldsNoTablePrivilege(t *testing.T) {
	var held []string
	require.NoError(t, inTenantTx(t, func(ctx context.Context) error {
		tx := txFromCtx(t, ctx)
		if _, err := tx.Exec(ctx, "SET ROLE NONE"); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT c.relname
			  FROM pg_catalog.pg_class c
			  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
			 WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p', 'v', 'm')
			   AND (has_table_privilege(c.oid, 'SELECT') OR has_table_privilege(c.oid, 'INSERT')
			     OR has_table_privilege(c.oid, 'UPDATE') OR has_table_privilege(c.oid, 'DELETE'))
			 ORDER BY 1`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			held = append(held, name)
		}
		return rows.Err()
	}))
	require.Empty(t, held, "the bare runtime login holds table privileges of its own")
}

// TestGuard_TenantCannotEscalateThroughTheServer covers the server-level
// escalations an injected statement reaches for first: planting objects in
// the public schema (a trojan function or a search_path hijack), reading or
// writing server files, running server programs, signalling other backends,
// disabling triggers with session_replication_role, and dialing out through a
// connection extension.
func TestGuard_TenantCannotEscalateThroughTheServer(t *testing.T) {
	probes := map[string]string{
		"create in public":              "SELECT has_schema_privilege('public', 'CREATE')",
		"read server files":             "SELECT pg_has_role('pg_read_server_files', 'MEMBER')",
		"write server files":            "SELECT pg_has_role('pg_write_server_files', 'MEMBER')",
		"execute server programs":       "SELECT pg_has_role('pg_execute_server_program', 'MEMBER')",
		"signal other backends":         "SELECT pg_has_role('pg_signal_backend', 'MEMBER')",
		"superuser":                     "SELECT rolsuper FROM pg_catalog.pg_roles WHERE rolname = current_user",
		"dial out through an extension": "SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_extension WHERE extname IN ('dblink', 'postgres_fdw'))",
	}
	for name, probe := range probes {
		t.Run(name, func(t *testing.T) {
			var granted bool
			require.NoError(t, inTenantTx(t, func(ctx context.Context) error {
				return txFromCtx(t, ctx).QueryRow(ctx, probe).Scan(&granted)
			}))
			require.Falsef(t, granted, "a tenant transaction can %s", name)
		})
	}

	t.Run("disable triggers", func(t *testing.T) {
		err := inTenantTx(t, func(ctx context.Context) error {
			_, err := txFromCtx(t, ctx).Exec(ctx, "SET LOCAL session_replication_role = replica")
			return err
		})
		require.Error(t, err, "a tenant transaction disabled triggers, and with them the audit and revision triggers")
	})

	t.Run("disable row-level security", func(t *testing.T) {
		err := inTenantTx(t, func(ctx context.Context) error {
			_, err := txFromCtx(t, ctx).Exec(ctx, "ALTER TABLE public.webhook_subscriptions NO FORCE ROW LEVEL SECURITY")
			return err
		})
		require.Error(t, err, "a tenant transaction altered row-level security")
	})
}
