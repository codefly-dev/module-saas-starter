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

const (
	installedContentReadPrefix = "installed-read/"
	installedModuleActorPrefix = "installed-actor/"
)

// installedBindingPrefixes is the namespace each delegation mode's issuer-owned
// binding ids take, so the id alone says which mode resolves it.
var installedBindingPrefixes = map[string]string{
	DelegationModeContentRead: installedContentReadPrefix,
	DelegationModeModuleActor: installedModuleActorPrefix,
}

// NamesInstalledContentRead discriminates issuer-owned installed read bindings.
func NamesInstalledContentRead(id string) bool {
	return strings.HasPrefix(id, installedContentReadPrefix)
}

// NamesInstalledBinding discriminates an issuer-owned installed binding of any
// delegation mode. Its children are terminal: none is exchanged, reminted,
// renewed or transformed.
func NamesInstalledBinding(id string) bool {
	return installedBindingMode(id) != ""
}

func installedBindingMode(id string) string {
	for mode, prefix := range installedBindingPrefixes {
		if strings.HasPrefix(id, prefix) {
			return mode
		}
	}
	return ""
}

type installedContentReadID struct {
	Principal string
	Binding   string
	Lifetime  string
}

// InstalledContentReadBinding resolves one person-invocable content-only binding.
func (s *Service) InstalledContentReadBinding(ctx context.Context, caller ModuleCaller, bindingID string) (WorkContextOperationBinding, ModuleOperationAudience, error) {
	return s.InstalledBinding(ctx, caller, bindingID, DelegationModeContentRead)
}

// InstalledBinding resolves one person-invocable binding installed under mode.
// The identity commits to monotonic owner facts, not a hash of current policy:
// removing and restoring identical policy can never revive a retained child.
//
// content_read requires a module audience serving the content kinds it reads.
// module_actor requires only that the audience be installed in the
// organization, a module or a solution, and commits to that installation, so
// withdrawing the audience retires every child exchanged for it.
func (s *Service) InstalledBinding(ctx context.Context, caller ModuleCaller, bindingID, mode string) (WorkContextOperationBinding, ModuleOperationAudience, error) {
	prefix, known := installedBindingPrefixes[mode]
	if !known {
		return WorkContextOperationBinding{}, ModuleOperationAudience{}, ErrNoOperationBinding
	}
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
	if binding.DelegationMode != mode {
		return WorkContextOperationBinding{}, ModuleOperationAudience{}, ErrNoOperationBinding
	}
	if binding.headlessOnly() || len(binding.SourceDelegationScopes) != 0 || len(binding.InvokeScopes) == 0 {
		return WorkContextOperationBinding{}, ModuleOperationAudience{}, ErrNoOperationBinding
	}
	if origin.Grant.PresenceGeneration == 0 || snapshot.envelopeRevision == 0 {
		return WorkContextOperationBinding{}, ModuleOperationAudience{}, ErrNoOperationBinding
	}
	facts := []any{caller.BoundOrg, snapshot.envelopeRevision,
		origin.Grant.PresenceBinding, origin.Grant.PresenceGeneration, origin.InstallationID, origin.InstallationRevision, origin.ProducerEpoch}
	if mode == DelegationModeModuleActor && strings.HasPrefix(binding.Audience, SolutionAudiencePrefix) {
		// A solution audience is held by the organization's installation of
		// that solution, which an uninstall or a reinstall replaces.
		installation, err := s.WorkContextAudienceInstallation(ctx, caller.BoundOrg, binding.Audience)
		if err != nil {
			return WorkContextOperationBinding{}, ModuleOperationAudience{}, err
		}
		facts = append(facts, binding.Audience, installation)
	} else {
		target, _, err := s.authorizeModuleCapabilityFromSnapshot(ctx, snapshot, ModuleCaller{PrincipalID: ModulePrincipalID(binding.Audience), BoundOrg: caller.BoundOrg})
		if err != nil {
			return WorkContextOperationBinding{}, ModuleOperationAudience{}, err
		}
		for _, scope := range binding.InvokeScopes {
			if mode == DelegationModeContentRead && (len(scope.Actions) != 1 || scope.Actions[0] != "read" || !slices.Contains(target.Grant.Resources, scope.ResourceKind)) {
				return WorkContextOperationBinding{}, ModuleOperationAudience{}, ErrNoOperationBinding
			}
		}
		if target.Grant.PresenceGeneration == 0 {
			return WorkContextOperationBinding{}, ModuleOperationAudience{}, ErrNoOperationBinding
		}
		facts = append(facts, target.Grant.PresenceBinding, target.Grant.PresenceGeneration, target.InstallationID, target.InstallationRevision, target.ProducerEpoch)
	}
	if s.deliveredSince(ctx, snapshot) {
		return WorkContextOperationBinding{}, ModuleOperationAudience{}, ErrModuleRegistryStale
	}
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
	return WorkContextOperationBinding{ID: prefix + base64.RawURLEncoding.EncodeToString(encoded), PrincipalID: caller.PrincipalID, InstallationID: origin.InstallationID, Revision: snapshot.envelopeRevision, Incarnation: origin.Grant.PresenceGeneration}, binding, nil
}

// decodeInstalledBinding reads the mode and the installed binding an issued id
// names. It authorizes nothing: the id is a pointer, re-resolved by its caller.
func decodeInstalledBinding(encoded string) (string, installedContentReadID, bool) {
	mode := installedBindingMode(encoded)
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encoded, installedBindingPrefixes[mode]))
	if mode == "" || err != nil || len(raw) > 4096 {
		return "", installedContentReadID{}, false
	}
	var id installedContentReadID
	if json.Unmarshal(raw, &id) != nil || id.Principal == "" || id.Binding == "" || id.Lifetime == "" {
		return "", installedContentReadID{}, false
	}
	return mode, id, true
}

func (s *Service) resolveInstalledBinding(ctx context.Context, org, encoded string) (WorkContextOperationBinding, error) {
	mode, id, ok := decodeInstalledBinding(encoded)
	if !ok {
		return WorkContextOperationBinding{}, ErrNoOperationBinding
	}
	current, _, err := s.InstalledBinding(ctx, ModuleCaller{PrincipalID: id.Principal, BoundOrg: org}, id.Binding, mode)
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
