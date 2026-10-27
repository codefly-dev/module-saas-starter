package business

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
)

// moduleConsent is what one module installation's consent allows the
// authority core to issue through, decided exactly as the native mint decides
// it: the installed registry's grant for the module (its manifest within the
// module envelope) intersected with what the organization's administrator
// consented to (consentedGrant). An installation whose consent allows no
// capability, or that awaits a re-consent, is not active for the core; a
// binding the grant or the consent drops is not usable there, and one either
// narrows is used within both. A widening the administrator has not consented
// to therefore reaches no issue.
//
// The registry is in memory and derived per replica; committedEnvelope is the
// revision committed in the core's reading transaction, and a registry
// derived from any other refuses, as fenceModuleRegistrySnapshot does, so a
// ceiling a newer envelope replaced is never read.
func (s *Service) moduleConsent(prefix string, raw []byte, committedEnvelope uint64) (map[string]domain.Binding, bool, error) {
	var consent InstallationScope
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &consent); err != nil {
			return nil, false, fmt.Errorf("module %s: the recorded consent does not read: %w", prefix, err)
		}
	}
	if !consent.Capabilities {
		return nil, false, nil
	}
	snapshot := s.moduleRegistrySnapshot()
	if s.moduleEnvelopes != nil && snapshot.envelopeRevision != committedEnvelope {
		s.requestRegistryPass()
		return nil, false, fmt.Errorf("%w: the registry was derived from envelope revision %d and revision %d is committed",
			ErrModuleRegistryStale, snapshot.envelopeRevision, committedEnvelope)
	}
	grant, registered := snapshot.registry[ModulePrincipalID(prefix)]
	if !registered {
		// Not in the registry: the envelope allows the module nothing.
		return map[string]domain.Binding{}, true, nil
	}
	consented, err := snapshot.consentedGrant(grant, consent)
	if errors.Is(err, ErrModuleInstallationConsentRequired) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	bindings := make(map[string]domain.Binding, len(consented.OperationAudiences))
	for id, b := range consented.OperationAudiences {
		bindings[id] = domain.Binding{ID: id, Audience: b.Audience,
			InvokeScopes: workScopes(b.InvokeScopes), HeadlessScopes: workScopes(b.HeadlessScopes), LookupScopes: workScopes(b.LookupScopes)}
	}
	return bindings, true, nil
}

func workScopes(in []ModuleOperationScope) []*basev0.WorkScopeV1 {
	out := make([]*basev0.WorkScopeV1, 0, len(in))
	for _, sc := range in {
		out = append(out, &basev0.WorkScopeV1{ResourceKind: sc.ResourceKind, Actions: slices.Clone(sc.Actions), ResourceIds: slices.Clone(sc.ResourceIDs)})
	}
	return out
}
