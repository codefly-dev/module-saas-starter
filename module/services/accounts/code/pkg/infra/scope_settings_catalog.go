package infra

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// scopeSettingReader is the one capability the catalog check needs. A pool and a
// transaction both satisfy it, so the check runs at startup on the control-plane
// pool and, in the suite, inside a transaction that stages a catalog to refuse.
type scopeSettingReader interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// scopeSettingPolicyQuery is every policy in the schema, with whether each half
// of its judgement reads the library's setting names and whether either reads
// the host's retired ones. A setting name is matched as a whole word with its
// dot escaped — unescaped it is a wildcard, and `app.current_org_id` would then
// also match a name that merely resembles it.
const scopeSettingPolicyQuery = `
WITH policy AS (
    SELECT p.polrelid::regclass::text || '.' || p.polname AS what,
           coalesce(pg_get_expr(p.polqual, p.polrelid), '') || ' ' ||
           coalesce(pg_get_expr(p.polwithcheck, p.polrelid), '') AS body
      FROM pg_catalog.pg_policy p
      JOIN pg_catalog.pg_class c ON c.oid = p.polrelid
      JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = 'public'
), setting AS (
    SELECT '\m' || replace($1, '.', '\.') || '\M' AS tenant,
           '\m' || replace($2, '.', '\.') || '\M' AS "user",
           '\m' || replace($3, '.', '\.') || '\M' AS retired_tenant,
           '\m' || replace($4, '.', '\.') || '\M' AS retired_user
)
SELECT what,
       body ~ s.tenant OR body ~ s."user" AS reads_library,
       body ~ s.retired_tenant OR body ~ s.retired_user AS reads_retired
  FROM policy, setting s
 ORDER BY what`

// requireCatalogReadsScopeSettings refuses to serve against a database whose
// policies do not read the two settings this process can bind.
//
// The predicate a scoped policy carries is
// `col::text = current_setting(<name>, true)`, and `current_setting(…, true)`
// returns NULL for a setting nothing bound: the predicate is false, the
// relation reads as empty for every caller, and no error is raised anywhere.
// So a binary and a catalog that disagree about the name do not fail — the
// product goes blank. Rolling back the accounts image with migration 65 applied
// is that state for as long as the rollback lasts, and the reverse (an image
// that binds only the library's names against a catalog from before the rename)
// is it too.
//
// bindRequestScopeSQL binds both generations, so this process survives either
// catalog on its own request pool. What it cannot do is widen the two scoped
// pools service-postgres owns: those bind the library's names and nothing else
// — the library takes two names, not four — so against a catalog that reads
// only the retired ones every read through them is silently empty. That is the
// reason this is a refusal to start rather than a warning: there is no binding
// the host can add to make such a catalog serve, and a started process would
// report healthy while answering nothing.
//
// It judges policies rather than the ledger's version row: the question is what
// the database will actually evaluate, and a version row is a claim about how
// it got there. A policy inside the compatibility window reads both names and
// is admitted by the first half of the rule.
//
// It runs where the served process is assembled, NewPostgresStoreWithCapabilities,
// and not in the single-credential tooling store: that one opens no scoped
// pool, so the reason this is fatal rather than advisory does not apply to it,
// and a migration tool that refused to open a database it was about to move
// forward would be refusing its own job.
func requireCatalogReadsScopeSettings(ctx context.Context, q scopeSettingReader) error {
	rows, err := q.Query(ctx, scopeSettingPolicyQuery,
		TenantScopeSetting, UserScopeSetting, LegacyTenantScopeSetting, LegacyUserScopeSetting)
	if err != nil {
		return fmt.Errorf("read the scope settings this database's policies judge by: %w", err)
	}
	defer rows.Close()
	var judged []scopeSettingPolicy
	for rows.Next() {
		var policy scopeSettingPolicy
		if err := rows.Scan(&policy.What, &policy.ReadsLibrary, &policy.ReadsRetired); err != nil {
			return fmt.Errorf("read the scope settings this database's policies judge by: %w", err)
		}
		judged = append(judged, policy)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the scope settings this database's policies judge by: %w", err)
	}
	return scopeSettingRefusal(judged)
}

// scopeSettingPolicy is one policy as the query above reports it: which
// generation of the two setting names its judgement reads.
type scopeSettingPolicy struct {
	What         string
	ReadsLibrary bool
	ReadsRetired bool
}

// scopeSettingRefusal is the decision requireCatalogReadsScopeSettings makes,
// separated from the query that feeds it: a catalog this process cannot serve
// is a property of a list of policies, and stating it this way is what lets the
// pre-rename catalog — every policy reading the retired names alone — be a test
// case rather than a second database.
func scopeSettingRefusal(judged []scopeSettingPolicy) error {
	var stale []string
	var library int
	for _, policy := range judged {
		switch {
		case policy.ReadsLibrary:
			library++
		case policy.ReadsRetired:
			stale = append(stale, policy.What)
		}
	}
	if len(stale) > 0 {
		return fmt.Errorf("this database judges %d row-security polic%s by the retired scope settings alone (%s), which the scoped pools cannot bind: every read through them would be silently empty. Apply store migration 66_scope_settings_transition, or tools/rls-scope-settings-transition.sql for a policy this module does not own",
			len(stale), policyPlural(len(stale)), strings.Join(sampleScopeSettingPolicies(stale, 5), ", "))
	}
	if library == 0 {
		return fmt.Errorf("no row-security policy in this database reads %s or %s, so nothing isolates a tenant from the request pools: this is not a database the store's ledger installed",
			TenantScopeSetting, UserScopeSetting)
	}
	return nil
}

// sampleScopeSettingPolicies is the first `most` of list, with a count of what
// it leaves out, so a startup refusal names the offenders without printing
// seventy of them.
func sampleScopeSettingPolicies(list []string, most int) []string {
	if len(list) <= most {
		return list
	}
	return append(list[:most:most], fmt.Sprintf("and %d more", len(list)-most))
}

func policyPlural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
