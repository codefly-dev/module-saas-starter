//go:build !pure

package infra_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
)

func envelopeJSON(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var out map[string]any
	require.NoError(t, decoder.Decode(&out))
	return out
}

func envelopeJSONClone(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(doc)
	require.NoError(t, err)
	return envelopeJSON(t, raw)
}

func envelopeJSONAt(t *testing.T, doc map[string]any, revision uint64) []byte {
	t.Helper()
	doc["revision"] = json.Number(jsonNumber(revision))
	raw, err := json.Marshal(doc)
	require.NoError(t, err)
	return raw
}

// Every change to a granted principal applies directly, the narrowings with
// the pure grants: no witness is asked first. Each narrowing below is a real
// one — a resource list that appears restricts an audience that was
// unrestricted, a required kind that appears refuses artifacts that passed, a
// tenant that is set removes every other organization — and each lands as
// the next revision.
func TestAnEnvelopeChangeToAGrantedPrincipalAppliesDirectly(t *testing.T) {
	s := newRegistryScenario(t, "rvf")
	digest := "sha256:" + strings.Repeat("ab", 32)
	base := envelopeJSON(t, s.envelope(t, 1, envelopeOptions{resources: true}))
	principal := func(doc map[string]any) map[string]any {
		return doc["principals"].(map[string]any)[s.prefix].(map[string]any)
	}
	grant := func(doc map[string]any) map[string]any { return principal(doc)["grant"].(map[string]any) }
	grant(base)["tenant"] = ""
	grant(base)["read_audiences"] = map[string]any{
		"docs": map[string]any{"audience": "example-reader", "scopes": []any{map[string]any{"resource_kind": "documents"}}},
	}
	grant(base)["artifact_policies"] = map[string]any{
		"p1": map[string]any{
			"schema":   "example/v1",
			"sources":  map[string]any{"src": s.primary.binding},
			"activate": map[string]any{"resource": "artifact", "action": "activate"},
			"run":      map[string]any{"resource": "artifact", "action": "run"},
			"contracts": []any{
				map[string]any{"kind": "a", "name": "one", "digest": digest},
				map[string]any{"kind": "b", "name": "two", "digest": digest},
			},
			"required_kinds": []any{"a"},
		},
	}

	current := s.currentRevision(t)
	_, err := s.delivery.service.PutModuleEnvelope(testCtx, s.admin, current, envelopeJSONAt(t, base, current+1))
	require.NoError(t, err, "the base envelope must be written")
	state := base

	opAudience := func(doc map[string]any) map[string]any {
		return grant(doc)["operation_audiences"].(map[string]any)["run"].(map[string]any)
	}
	cases := []struct {
		name   string
		mutate func(doc map[string]any)
	}{
		{"a content resource removed", func(doc map[string]any) { grant(doc)["resources"] = []any{} }},
		{"lookup scope restricted from every resource to one id (resource_ids added)", func(doc map[string]any) {
			opAudience(doc)["lookup_scopes"].([]any)[0].(map[string]any)["resource_ids"] = []any{"p1"}
		}},
		{"lookup and invoke scopes restricted from every resource to one id (resource_ids added)", func(doc map[string]any) {
			opAudience(doc)["lookup_scopes"].([]any)[0].(map[string]any)["resource_ids"] = []any{"p1"}
			opAudience(doc)["invoke_scopes"].([]any)[0].(map[string]any)["resource_ids"] = []any{"p1"}
		}},
		{"read audience restricted from every resource to one id (resource_ids added)", func(doc map[string]any) {
			grant(doc)["read_audiences"].(map[string]any)["docs"].(map[string]any)["scopes"].([]any)[0].(map[string]any)["resource_ids"] = []any{"d1"}
		}},
		{"artifact policy gains a REQUIRED contract kind", func(doc map[string]any) {
			grant(doc)["artifact_policies"].(map[string]any)["p1"].(map[string]any)["required_kinds"] = []any{"a", "b"}
		}},
		{"tenancy pinned: shared (every organization) -> one organization", func(doc map[string]any) { grant(doc)["tenant"] = s.tenant }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			next := envelopeJSONClone(t, state)
			tc.mutate(next)
			current := s.currentRevision(t)
			raw := envelopeJSONAt(t, next, current+1)
			_, parseErr := business.ParseModuleEnvelope(raw)
			require.NoError(t, parseErr, "the document must be a valid envelope")
			_, err := s.delivery.service.PutModuleEnvelope(testCtx, s.admin, current, raw)
			require.NoError(t, err, "a narrowing applies directly")
			require.Equal(t, current+1, s.currentRevision(t))
		})
	}

	// The pure grants: a member added to a granted principal's allowlist, and a
	// whole principal added beside the ones granted.
	t.Run("a queue added to a granted principal lands", func(t *testing.T) {
		next := envelopeJSONClone(t, state)
		grant(next)["queues"] = []any{"reports.render", "reports.extra"}
		current := s.currentRevision(t)
		_, err := s.delivery.service.PutModuleEnvelope(testCtx, s.admin, current, envelopeJSONAt(t, next, current+1))
		require.NoError(t, err)
		require.Equal(t, current+1, s.currentRevision(t))
		state = next
	})
	t.Run("a principal added lands", func(t *testing.T) {
		next := envelopeJSONClone(t, state)
		next["principals"].(map[string]any)["added-"+s.prefix] = map[string]any{"grant": map[string]any{"cross_tenant": true}}
		current := s.currentRevision(t)
		_, err := s.delivery.service.PutModuleEnvelope(testCtx, s.admin, current, envelopeJSONAt(t, next, current+1))
		require.NoError(t, err)
		require.Equal(t, current+1, s.currentRevision(t))
		state = next
	})
	t.Run("a principal removed lands", func(t *testing.T) {
		next := envelopeJSONClone(t, state)
		delete(next["principals"].(map[string]any), "added-"+s.prefix)
		current := s.currentRevision(t)
		_, err := s.delivery.service.PutModuleEnvelope(testCtx, s.admin, current, envelopeJSONAt(t, next, current+1))
		require.NoError(t, err)
		require.Equal(t, current+1, s.currentRevision(t))
	})
}

// The installation half: an envelope that takes an organization's
// installation away applies directly, revokes that installation in the same
// transaction, and the next capability decision for that organization refuses.
// The revocation reads installations on the control plane; read anywhere else
// a row policy hides them all and the installation would survive the ceiling.
func TestAnEnvelopeThatRevokesAnInstallationAppliesDirectly(t *testing.T) {
	s := newRegistryScenario(t, "rvi")
	orgB := seedOrg(t, s.admin)
	require.NoError(t, testStore.As(business.Identity{OrgID: orgB}).AddOrgMember(testCtx, s.admin, "admin"))
	shared := envelopeJSON(t, s.envelope(t, 1, envelopeOptions{resources: true}))
	grant := func(doc map[string]any) map[string]any {
		return doc["principals"].(map[string]any)[s.prefix].(map[string]any)["grant"].(map[string]any)
	}
	grant(shared)["tenant"] = ""
	current := s.currentRevision(t)
	_, err := s.delivery.service.PutModuleEnvelope(testCtx, s.admin, current, envelopeJSONAt(t, shared, current+1))
	require.NoError(t, err)
	s.deliverManifest(t, s.primary, 1)
	s.pass(t)
	_, present := s.entry()
	require.True(t, present)

	status := func(org, id string) gen.InstallationStatus {
		var out gen.InstallationStatus
		require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
			row, _, err := testStore.GetInstallation(ctx, org, id)
			if err == nil {
				out = row.Status
			}
			return err
		}))
		return out
	}

	active := s.install(t, orgB) // acknowledged: ACTIVE
	require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE, status(orgB, active.Id))
	_, err = s.delivery.service.AuthorizeModuleCapability(testCtx, s.caller(orgB))
	require.NoError(t, err, "org B's installation is usable under the shared tenancy")

	pinned := envelopeJSONClone(t, shared)
	grant(pinned)["tenant"] = s.tenant
	current = s.currentRevision(t)
	_, err = s.delivery.service.PutModuleEnvelope(testCtx, s.admin, current, envelopeJSONAt(t, pinned, current+1))
	require.NoError(t, err, "the narrowing applies directly")
	require.Equal(t, current+1, s.currentRevision(t))
	require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_REVOKED, status(orgB, active.Id),
		"the ceiling no longer allows org B, so its installation is revoked in the same write")
	s.pass(t)
	_, err = s.delivery.service.AuthorizeModuleCapability(testCtx, s.caller(orgB))
	require.Error(t, err, "org B's next capability decision refuses")
}

func jsonNumber(value uint64) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
