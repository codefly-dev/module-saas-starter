//go:build !pure

package infra_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// Migration 127 makes a team membership a child of an organization membership.
// A deployed database can already hold rows the new invariant forbids, and the
// repair for those is the part no later state reveals: they are recorded and
// removed, never fixed by granting the parent membership the organization
// never issued. This replays the real migration files — down to the pre-127
// shape, seed an orphan a deployed database could hold, then up.
func TestMigration127QuarantinesOrphanTeamMemberships(t *testing.T) {
	down := migrationSQL(t, "127_team_membership_parent_org.down.sql")
	up := migrationSQL(t, "127_team_membership_parent_org.up.sql")
	validate := migrationSQL(t, "128_team_membership_parent_org_validate.up.sql")

	orgID := uuid.NewString()
	memberID := uuid.NewString()
	outsiderID := uuid.NewString()
	teamID := uuid.NewString()

	restored := false
	t.Cleanup(func() {
		asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
			if !restored {
				// A failure between down and up would leave the shared schema
				// without the invariant for every later test in the package.
				mustExec(t, ctx, conn, up)
				mustExec(t, ctx, conn, validate)
			}
			mustExec(t, ctx, conn, `DELETE FROM team_membership_quarantine WHERE org_id = $1`, orgID)
			mustExec(t, ctx, conn, `DELETE FROM organizations WHERE id = $1`, orgID)
			mustExec(t, ctx, conn, `DELETE FROM users WHERE uuid = ANY($1)`, []string{memberID, outsiderID})
		})
	})

	asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
		mustExec(t, ctx, conn, down)

		for _, id := range []string{memberID, outsiderID} {
			mustExec(t, ctx, conn,
				`INSERT INTO users (uuid, primary_email) VALUES ($1, $2)`,
				id, fmt.Sprintf("%s@migration-127.test", id))
		}
		mustExec(t, ctx, conn,
			`INSERT INTO organizations (id, name, slug, owner_id) VALUES ($1, 'Acme', 'acme-migration-127', $2)`,
			orgID, memberID)
		mustExec(t, ctx, conn,
			`INSERT INTO organization_members (org_id, user_id, role) VALUES ($1, $2, 'owner')`,
			orgID, memberID)
		mustExec(t, ctx, conn,
			`INSERT INTO teams (id, org_id, name, slug, path) VALUES ($1, $2, 'Example Team', 'example-team', 'example-team')`,
			teamID, orgID)
		// The pre-127 shape accepts both: only the second has a parent membership.
		mustExec(t, ctx, conn,
			`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'admin'), ($1, $3, 'member')`,
			teamID, outsiderID, memberID)

		mustExec(t, ctx, conn, up)
		mustExec(t, ctx, conn, validate)
	})
	restored = true

	controlPlaneTx(t, func(ctx context.Context, tx pgx.Tx) error {
		var survivors []string
		rows, err := tx.Query(ctx, `SELECT user_id::text FROM team_members WHERE team_id = $1`, teamID)
		require.NoError(t, err)
		defer rows.Close()
		for rows.Next() {
			var id string
			require.NoError(t, rows.Scan(&id))
			survivors = append(survivors, id)
		}
		require.NoError(t, rows.Err())
		require.Equal(t, []string{memberID}, survivors,
			"the membership without a parent must not survive the migration")

		var stampedOrg, role, reason string
		require.NoError(t, tx.QueryRow(ctx, `
			SELECT org_id::text, role, reason
			FROM team_membership_quarantine
			WHERE team_id = $1 AND user_id = $2`, teamID, outsiderID,
		).Scan(&stampedOrg, &role, &reason))
		require.Equal(t, orgID, stampedOrg)
		require.Equal(t, "admin", role)
		require.NotEmpty(t, reason)

		var orgOnSurvivor string
		require.NoError(t, tx.QueryRow(ctx,
			`SELECT org_id::text FROM team_members WHERE team_id = $1 AND user_id = $2`,
			teamID, memberID).Scan(&orgOnSurvivor))
		require.Equal(t, orgID, orgOnSurvivor, "the backfill must stamp the team's own organization")

		var membership int
		require.NoError(t, tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM organization_members WHERE org_id = $1 AND user_id = $2`,
			orgID, outsiderID).Scan(&membership))
		require.Zero(t, membership, "the repair must never manufacture a parent membership")
		return nil
	})

	// Rolling back must not erase the record. The memberships it describes are
	// already deleted and `up` never restores them, so dropping the table with
	// the schema would destroy the only evidence that they ever existed.
	asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
		restored = false
		mustExec(t, ctx, conn, down)
	})
	controlPlaneTx(t, func(ctx context.Context, tx pgx.Tx) error {
		var surviving int
		require.NoError(t, tx.QueryRow(ctx, `
			SELECT COUNT(*) FROM team_membership_quarantine
			WHERE team_id = $1 AND user_id = $2`, teamID, outsiderID).Scan(&surviving))
		require.Equal(t, 1, surviving,
			"rolling back migration 127 must leave the quarantine record standing")
		return nil
	})
	asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
		mustExec(t, ctx, conn, up)
		mustExec(t, ctx, conn, validate)
		restored = true
	})
}
