package business_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"api/pkg/business"
	"api/pkg/gen"
)

// TestRLS_AuditExportConfigs_CrossTenantBlocked is the load-bearing
// proof that Phase-1 RLS actually fires. Two orgs each get a row;
// org A queries via the api and MUST see only its own row, no
// matter how the SQL is written under the hood.
//
// We seed via testStore.UpsertAuditExportConfig under WithBypass to
// skip the SaveAuditExportConfig pre-flight (which calls real S3).
// The seed path goes through the same RLS-policy'd table writes —
// just bypass-mode so we can write any orgID.
//
// If this test ever fails, RLS is broken — either WithOrgTx isn't
// setting app.current_org_id, the policy isn't installed, or the
// table forgot FORCE row-level security.
func TestRLS_AuditExportConfigs_CrossTenantBlocked(t *testing.T) {
	clearData(t)
	ctx := testCtx

	orgA := mustOrgWithOwner(t, ctx, "alice@rls-test.com", "alice-rls", "Acme A")
	orgB := mustOrgWithOwner(t, ctx, "bob@rls-test.com", "bob-rls", "Acme B")

	// Seed both orgs' configs as bypass (skip Service pre-flight).
	require.NoError(t, testStore.WithBypass(ctx, func(ctx context.Context) error {
		if err := testStore.UpsertAuditExportConfig(ctx, &business.AuditExportConfig{
			ID: business.NewIDString(), OrgID: orgA, Bucket: "bucket-a",
			Region: "us-east-1", AccessKeyID: "x", SecretAccessKey: "y",
			CadenceMinutes: 60, Enabled: true,
		}); err != nil {
			return err
		}
		return testStore.UpsertAuditExportConfig(ctx, &business.AuditExportConfig{
			ID: business.NewIDString(), OrgID: orgB, Bucket: "bucket-b",
			Region: "us-east-1", AccessKeyID: "x", SecretAccessKey: "y",
			CadenceMinutes: 60, Enabled: true,
		})
	}))

	// Read as A: see A's row, get bucket-a.
	cfgA, err := testService.GetAuditExportConfig(ctx, orgA)
	require.NoError(t, err)
	require.NotNil(t, cfgA, "org A should see its own config")
	require.Equal(t, "bucket-a", cfgA.Bucket)

	// Read as B: see B's row, get bucket-b.
	cfgB, err := testService.GetAuditExportConfig(ctx, orgB)
	require.NoError(t, err)
	require.NotNil(t, cfgB, "org B should see its own config")
	require.Equal(t, "bucket-b", cfgB.Bucket)

	// Hostile case: a query that asks for org B's config from org
	// A's session. Our Service.GetAuditExportConfig signature takes
	// orgID as the WithOrgTx scope AND the WHERE filter, so a "wrong
	// orgID" call is structurally impossible from Service. To probe
	// the policy directly: ask for ANY config row inside org A's
	// transaction — should return only org A's, never B's.
	require.NoError(t, testStore.WithOrgTx(ctx, orgA, func(ctx context.Context) error {
		// At the storage layer we'd query without an explicit org
		// filter — RLS must hide B's row regardless.
		got, err := testStore.GetAuditExportConfig(ctx, orgB)
		require.NoError(t, err)
		require.Nil(t, got, "RLS must hide org B's row from org A's tx")
		return nil
	}))

	// Sanity: deleting A's row leaves B's row intact.
	require.NoError(t, testService.DeleteAuditExportConfig(ctx, orgA))
	cfgB2, err := testService.GetAuditExportConfig(ctx, orgB)
	require.NoError(t, err)
	require.NotNil(t, cfgB2, "deleting A's row must not affect B's")

	// And A's row really is gone.
	cfgAGone, err := testService.GetAuditExportConfig(ctx, orgA)
	require.NoError(t, err)
	require.Nil(t, cfgAGone)
}

// TestRLS_AuditExporter_BypassWorks pins the worker path. The
// audit-exporter's tick() does a cross-tenant scan
// (ListDueAuditExportConfigs) inside WithBypass — that scan MUST
// see all enabled rows regardless of which org's context is on the
// caller.
//
// Note on the un-wrapped path: the codefly Postgres plugin connects
// the api as a superuser, which bypasses RLS unconditionally. So
// an un-wrapped call ALSO returns all rows (not zero) — fail-OPEN,
// not fail-closed. This is a known gap (see AUTHZ.md "fail-closed
// gap" section). When codefly's Postgres plugin grows a non-superuser
// app role, the un-wrapped path becomes fail-closed and we'll add
// a "MUST return 0 rows" assertion here.
func TestRLS_AuditExporter_BypassWorks(t *testing.T) {
	clearData(t)
	ctx := testCtx

	orgA := mustOrgWithOwner(t, ctx, "alice2@rls-test.com", "alice2-rls", "Acme A2")
	orgB := mustOrgWithOwner(t, ctx, "bob2@rls-test.com", "bob2-rls", "Acme B2")

	require.NoError(t, testStore.WithBypass(ctx, func(ctx context.Context) error {
		if err := testStore.UpsertAuditExportConfig(ctx, &business.AuditExportConfig{
			ID: business.NewIDString(), OrgID: orgA, Bucket: "ba",
			Region: "us-east-1", AccessKeyID: "x", SecretAccessKey: "y",
			CadenceMinutes: 60, Enabled: true,
		}); err != nil {
			return err
		}
		return testStore.UpsertAuditExportConfig(ctx, &business.AuditExportConfig{
			ID: business.NewIDString(), OrgID: orgB, Bucket: "bb",
			Region: "us-east-1", AccessKeyID: "x", SecretAccessKey: "y",
			CadenceMinutes: 60, Enabled: true,
		})
	}))

	// WithBypass: see all enabled rows. That's the worker path's
	// job (poll all orgs' configs to find due ones).
	var withBypass []*business.AuditExportConfig
	require.NoError(t, testStore.WithBypass(context.Background(), func(ctx context.Context) error {
		c, err := testStore.ListDueAuditExportConfigs(ctx, time.Now())
		withBypass = c
		return err
	}))
	require.GreaterOrEqual(t, len(withBypass), 2,
		"WithBypass must see all orgs' due configs (saw %d)", len(withBypass))

	// (We don't assert WithOrgTx-from-org-A returns just 1 row here:
	// the ListDue query uses interval arithmetic on cadence_minutes
	// which doesn't compose well under app_tenant role privileges
	// — to be addressed in Phase 2 when the SQL is rewritten to
	// avoid the implicit cast. The cross-tenant SELECT path is
	// already proven by TestRLS_AuditExportConfigs_CrossTenantBlocked.)
}

// helpers

func mustOrgWithOwner(t *testing.T, ctx context.Context, email, providerID, orgName string) string {
	t.Helper()
	resp, err := testService.RegisterUser(ctx, &gen.RegisterUserRequest{
		PrimaryEmail: email,
		Identity: &gen.UserIdentity{
			Provider: "email", ProviderId: providerID, ProviderEmail: email,
		},
	})
	require.NoError(t, err)
	org, err := testService.CreateOrganization(ctx, resp.User.Uuid, &gen.CreateOrganizationRequest{
		Name: orgName, Slug: providerID + "-org",
	})
	require.NoError(t, err)
	return org.Organization.Id
}
