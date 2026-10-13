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

// Only a pure grant skips the policy-log witness: approved builds added,
// whole principals added, everything else the same value. With no policy log
// wired, a write that needs the witness is refused and writes nothing, so "it
// landed" is the proof a change was applied unwitnessed.
//
// Every shape below is a real narrowing that a comparison by "more JSON means
// more authority" let through: a resource list that appears restricts an
// audience that was unrestricted, a required kind that appears refuses
// artifacts that passed, a unit that gains a field is a different unit, a
// tenant that is set removes every other organization, and two revisions past
// 2^53 are one float.
func TestAnEnvelopeChangeToAGrantedPrincipalNeedsTheWitness(t *testing.T) {
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
	// The lookup unit sits at revision 2^53 so the float64 comparison can be probed.
	principal(base)["bindings"].([]any)[1].(map[string]any)["revision"] = json.Number("9007199254740992")

	current := s.currentRevision(t)
	_, err := s.delivery.service.PutModuleEnvelope(testCtx, s.admin, current, envelopeJSONAt(t, base, current+1))
	require.NoError(t, err, "the base envelope (witnessed) must be written")
	state := base

	// From here the host has no witness at all.
	s.delivery.service.SetPolicyLog(nil, nil)

	opAudience := func(doc map[string]any) map[string]any {
		return grant(doc)["operation_audiences"].(map[string]any)["run"].(map[string]any)
	}
	cases := []struct {
		name   string
		mutate func(doc map[string]any)
	}{
		{"a content resource removed", func(doc map[string]any) { grant(doc)["resources"] = []any{} }},
		// Even an addition inside a section: the section is compared for
		// equality, never for "at least as much".
		{"a queue added to a granted principal", func(doc map[string]any) { grant(doc)["queues"] = []any{"reports.render", "reports.extra"} }},
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
		{"an admitted unit is replaced by a different unit under the same id (queue set)", func(doc map[string]any) {
			principal(doc)["bindings"].([]any)[0].(map[string]any)["queue"] = "reports.render"
		}},
		{"an admitted unit's revision moves 2^53 -> 2^53+1", func(doc map[string]any) {
			principal(doc)["bindings"].([]any)[1].(map[string]any)["revision"] = json.Number("9007199254740993")
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
			require.ErrorIs(t, err, business.ErrPolicyLogUnreachable, "the change must need the witness")
			require.Equal(t, current, s.currentRevision(t), "a refused write must write nothing")
		})
	}

	// And the pure grant: a build approved beside the ones already approved.
	t.Run("an approved build added lands with no witness", func(t *testing.T) {
		next := envelopeJSONClone(t, state)
		builds, _ := next["approved_builds"].([]any)
		next["approved_builds"] = append(builds, "sha256:"+strings.Repeat("cd", 32))
		current := s.currentRevision(t)
		_, err := s.delivery.service.PutModuleEnvelope(testCtx, s.admin, current, envelopeJSONAt(t, next, current+1))
		require.NoError(t, err)
		require.Equal(t, current+1, s.currentRevision(t))
		state = next
	})
	t.Run("an approved build removed needs the witness", func(t *testing.T) {
		next := envelopeJSONClone(t, state)
		builds := next["approved_builds"].([]any)
		next["approved_builds"] = builds[:len(builds)-1]
		current := s.currentRevision(t)
		_, err := s.delivery.service.PutModuleEnvelope(testCtx, s.admin, current, envelopeJSONAt(t, next, current+1))
		require.ErrorIs(t, err, business.ErrPolicyLogUnreachable)
	})
	s.delivery.service.SetPolicyLog(&productionPathPolicyLog{}, testStore)
}

// The installation half: an envelope that would take an organization's
// installation away is a narrowing, and with no witness it is refused and the
// installation stays as it was. The classification reads installations on the
// control plane; read anywhere else a row policy hides them all, and the write
// revoked an active installation unwitnessed.
func TestAnEnvelopeThatWouldRevokeAnInstallationNeedsTheWitness(t *testing.T) {
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
	revision := current + 1
	s.deliverPresence(t, s.primary, 1, revision)
	s.pass(t)
	s.deliverAuthority(t, s.authority(t, s.primary, 1, revision, false, s.grantsTo(s.primary)))
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

	s.delivery.service.SetPolicyLog(nil, nil)
	pinned := envelopeJSONClone(t, shared)
	grant(pinned)["tenant"] = s.tenant
	current = s.currentRevision(t)
	_, err = s.delivery.service.PutModuleEnvelope(testCtx, s.admin, current, envelopeJSONAt(t, pinned, current+1))
	require.ErrorIs(t, err, business.ErrPolicyLogUnreachable)
	require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE, status(orgB, active.Id),
		"an unwitnessed envelope write must not revoke an installation")
	s.delivery.service.SetPolicyLog(&productionPathPolicyLog{}, testStore)
}
