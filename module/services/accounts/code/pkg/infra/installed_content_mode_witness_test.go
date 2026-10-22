//go:build !pure

package infra_test

import (
	"accounts/pkg/business"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInstalledContentReadModeMigrationRequiresWitnessPostgres(t *testing.T) {
	for _, removing := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary to content", true: "content to ordinary"}[removing], func(t *testing.T) {
			s := newRegistryScenario(t, "modewitness")
			before := envelopeJSON(t, s.envelope(t, 1, envelopeOptions{resources: true}))
			binding := func(doc map[string]any) map[string]any {
				return doc["principals"].(map[string]any)[s.prefix].(map[string]any)["grant"].(map[string]any)["operation_audiences"].(map[string]any)["run"].(map[string]any)
			}
			binding(before)["invoke_scopes"] = []any{map[string]any{"resource_kind": "profiles", "actions": []string{"read"}}}
			if removing {
				binding(before)["delegation_mode"] = business.DelegationModeContentRead
			}
			current := s.currentRevision(t)
			_, err := s.delivery.service.PutModuleEnvelope(testCtx, s.admin, current, envelopeJSONAt(t, before, current+1))
			require.NoError(t, err)
			s.delivery.service.SetPolicyLog(nil, nil)
			next := envelopeJSONClone(t, before)
			if removing {
				delete(binding(next), "delegation_mode")
			} else {
				binding(next)["delegation_mode"] = business.DelegationModeContentRead
			}
			_, err = s.delivery.service.PutModuleEnvelope(testCtx, s.admin, current+1, envelopeJSONAt(t, next, current+2))
			require.Error(t, err)
			require.ErrorIs(t, err, business.ErrPolicyLogUnreachable)
			record, err := s.delivery.service.GetModuleEnvelope(testCtx, s.admin)
			require.NoError(t, err)
			require.Equal(t, current+1, record.Revision)
		})
	}
}
