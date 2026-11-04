package infra_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/infra"
)

// The catalog a database presents, as the startup check reads it, at each
// version of the ledger that can be under a running pod — and what the check
// must do with each.
//
// The one that matters is version 64: every policy reads the host's retired
// names and none reads the library's. An image built after migration 65 binds
// both generations, so its own request pool would serve that catalog correctly;
// the two pools service-postgres owns would not, because the library binds its
// two names and takes no more. A process that started there would report
// healthy and answer nothing through them, which is the failure mode this
// refusal exists to convert into a refusal to start.
func TestScopeSettingRefusalReadsTheCatalogsVersion(t *testing.T) {
	policy := func(what string, library, retired bool) infra.ScopeSettingPolicy {
		return infra.ScopeSettingPolicy{What: what, ReadsLibrary: library, ReadsRetired: retired}
	}
	for _, c := range []struct {
		name    string
		judged  []infra.ScopeSettingPolicy
		refused string
	}{{
		name: "version 64: the retired names alone, which the scoped pools cannot bind",
		judged: []infra.ScopeSettingPolicy{
			policy("public.organizations.organizations_self", false, true),
			policy("public.api_keys.api_keys_tenant", false, true),
		},
		refused: "retired scope settings alone",
	}, {
		name:   "version 65: the library's names alone",
		judged: []infra.ScopeSettingPolicy{policy("public.organizations.organizations_self", true, false)},
	}, {
		name:   "version 66: both, which is the compatibility window",
		judged: []infra.ScopeSettingPolicy{policy("public.organizations.organizations_self", true, true)},
	}, {
		name: "a half-applied rewrite: one policy left behind is still a silent empty relation",
		judged: []infra.ScopeSettingPolicy{
			policy("public.organizations.organizations_self", true, true),
			policy("public.api_keys.api_keys_tenant", false, true),
		},
		refused: "public.api_keys.api_keys_tenant",
	}, {
		name:    "a database with no scoped policy at all is not one this ledger installed",
		judged:  nil,
		refused: "no row-security policy in this database reads",
	}, {
		name: "a policy that reads neither setting is some other boundary's, and not this check's business",
		judged: []infra.ScopeSettingPolicy{
			policy("public.organizations.organizations_self", true, false),
			policy("public.audit_events.audit_events_worker_write", false, false),
		},
	}} {
		t.Run(c.name, func(t *testing.T) {
			err := infra.ScopeSettingRefusal(c.judged)
			if c.refused == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), c.refused)
		})
	}
}

// The refusal has to tell an operator what to do about it, which for a policy
// this module owns is one migration and for a consumer's own policy is the
// recipe that migration embeds. A refusal that only reports the fact leaves
// them reading the catalog.
func TestScopeSettingRefusalNamesTheRemedy(t *testing.T) {
	err := infra.ScopeSettingRefusal([]infra.ScopeSettingPolicy{
		{What: "public.organizations.organizations_self", ReadsRetired: true},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "66_scope_settings_transition")
	require.Contains(t, err.Error(), "tools/rls-scope-settings-transition.sql")
}

// Seventy refused policies is not a startup log line anyone reads, and a
// refusal that prints all of them buries the one fact that matters. Five and a
// count.
func TestScopeSettingRefusalSamplesALongList(t *testing.T) {
	var judged []infra.ScopeSettingPolicy
	for _, what := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		judged = append(judged, infra.ScopeSettingPolicy{What: "public.t." + what, ReadsRetired: true})
	}
	err := infra.ScopeSettingRefusal(judged)
	require.Error(t, err)
	require.Contains(t, err.Error(), "judges 7 row-security policies")
	require.Contains(t, err.Error(), "public.t.e")
	require.NotContains(t, err.Error(), "public.t.f")
	require.Contains(t, err.Error(), "and 2 more")
}
