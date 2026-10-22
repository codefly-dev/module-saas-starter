package business

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const installedContentReadPrefix = "installed-read/"

// NamesInstalledContentRead discriminates issuer-owned installed read bindings.
func NamesInstalledContentRead(id string) bool {
	return strings.HasPrefix(id, installedContentReadPrefix)
}

type installedContentReadID struct {
	Principal string
	Binding   string
	Lifetime  string
}

// InstalledContentReadBinding resolves one person-invocable content-only binding.
// The identity commits to monotonic owner facts, not a hash of current policy:
// removing and restoring identical policy can never revive a retained child.
func (s *Service) InstalledContentReadBinding(ctx context.Context, caller ModuleCaller, bindingID string) (WorkContextOperationBinding, ModuleOperationAudience, error) {
	ctx, releaseAuthority, err := s.BeginModuleAuthorityUse(ctx)
	if err != nil {
		return WorkContextOperationBinding{}, ModuleOperationAudience{}, err
	}
	defer releaseAuthority()
	origin, snapshot, err := s.authorizeModuleCapability(ctx, caller)
	if err != nil {
		return WorkContextOperationBinding{}, ModuleOperationAudience{}, err
	}
	binding, ok := origin.Grant.OperationAudiences[bindingID]
	if !ok {
		return WorkContextOperationBinding{}, ModuleOperationAudience{}, ErrNoOperationBinding
	}
	if binding.DelegationMode != DelegationModeContentRead {
		return WorkContextOperationBinding{}, ModuleOperationAudience{}, ErrNoOperationBinding
	}
	if binding.headlessOnly() || len(binding.SourceDelegationScopes) != 0 || len(binding.InvokeScopes) == 0 {
		return WorkContextOperationBinding{}, ModuleOperationAudience{}, ErrNoOperationBinding
	}
	target, _, err := s.authorizeModuleCapabilityFromSnapshot(ctx, snapshot, ModuleCaller{PrincipalID: ModulePrincipalID(binding.Audience), BoundOrg: caller.BoundOrg})
	if err != nil {
		return WorkContextOperationBinding{}, ModuleOperationAudience{}, err
	}
	for _, scope := range binding.InvokeScopes {
		if len(scope.Actions) != 1 || scope.Actions[0] != "read" || !slices.Contains(target.Grant.Resources, scope.ResourceKind) {
			return WorkContextOperationBinding{}, ModuleOperationAudience{}, ErrNoOperationBinding
		}
	}
	if origin.Grant.PresenceGeneration == 0 || target.Grant.PresenceGeneration == 0 || snapshot.envelopeRevision == 0 {
		return WorkContextOperationBinding{}, ModuleOperationAudience{}, ErrNoOperationBinding
	}
	if s.deliveredSince(ctx, snapshot) {
		return WorkContextOperationBinding{}, ModuleOperationAudience{}, ErrModuleRegistryStale
	}
	facts := []any{caller.BoundOrg, snapshot.envelopeRevision,
		origin.Grant.PresenceBinding, origin.Grant.PresenceGeneration, origin.InstallationID, origin.InstallationRevision, origin.ProducerEpoch,
		target.Grant.PresenceBinding, target.Grant.PresenceGeneration, target.InstallationID, target.InstallationRevision, target.ProducerEpoch}
	encoded, err := json.Marshal(facts)
	if err != nil {
		return WorkContextOperationBinding{}, ModuleOperationAudience{}, err
	}
	id := installedContentReadID{caller.PrincipalID, bindingID, fmt.Sprintf("%x", sha256.Sum256(encoded))}
	encoded, err = json.Marshal(id)
	if err != nil {
		return WorkContextOperationBinding{}, ModuleOperationAudience{}, err
	}
	if err := s.FenceModuleMint(ctx, ModuleWorkContextAuthority{PrincipalID: caller.PrincipalID, Tenant: caller.BoundOrg, InstallationID: origin.InstallationID, InstallationRevision: origin.InstallationRevision, snapshot: snapshot}); err != nil {
		return WorkContextOperationBinding{}, ModuleOperationAudience{}, err
	}
	return WorkContextOperationBinding{ID: installedContentReadPrefix + base64.RawURLEncoding.EncodeToString(encoded), PrincipalID: caller.PrincipalID, InstallationID: origin.InstallationID, Revision: snapshot.envelopeRevision, Incarnation: origin.Grant.PresenceGeneration}, binding, nil
}

func (s *Service) resolveInstalledContentRead(ctx context.Context, org, encoded string) (WorkContextOperationBinding, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encoded, installedContentReadPrefix))
	if err != nil || len(raw) > 4096 {
		return WorkContextOperationBinding{}, ErrNoOperationBinding
	}
	var id installedContentReadID
	if json.Unmarshal(raw, &id) != nil || id.Principal == "" || id.Binding == "" || id.Lifetime == "" {
		return WorkContextOperationBinding{}, ErrNoOperationBinding
	}
	current, _, err := s.InstalledContentReadBinding(ctx, ModuleCaller{PrincipalID: id.Principal, BoundOrg: org}, id.Binding)
	if err != nil {
		return WorkContextOperationBinding{}, err
	}
	if current.ID != encoded {
		return WorkContextOperationBinding{}, ErrNoOperationBinding
	}
	return current, nil
}

// InstalledContentReadStanding recognizes foreign read standing only through an
// exact current binding. The returned identities must still resolve at release.
// This grants no visibility: the person's current grants remain authoritative.
func (s *Service) InstalledContentReadStanding(ctx context.Context, org, audience string, kinds []string) ([]string, []string, error) {
	snapshot := s.moduleRegistrySnapshot()
	grant, ok := snapshot.registry[ModulePrincipalID(audience)]
	if !ok {
		return nil, nil, nil
	}
	var resources, identities []string
	for key, candidate := range grant.OperationAudiences {
		needed := false
		for _, scope := range candidate.InvokeScopes {
			needed = needed || slices.Contains(kinds, scope.ResourceKind)
		}
		if !needed {
			continue
		}
		if candidate.DelegationMode != DelegationModeContentRead {
			continue
		}
		identity, binding, err := s.InstalledContentReadBinding(ctx, ModuleCaller{PrincipalID: ModulePrincipalID(audience), BoundOrg: org}, key)
		if err != nil {
			if err == ErrNoOperationBinding || status.Code(err) == codes.PermissionDenied {
				continue
			}
			return nil, nil, err
		}
		identities = append(identities, identity.ID)
		for _, scope := range binding.InvokeScopes {
			if len(scope.ResourceIDs) == 0 {
				resources = append(resources, scope.ResourceKind)
			}
		}
	}
	return resources, identities, nil
}
