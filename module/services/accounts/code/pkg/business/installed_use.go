package business

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/google/uuid"
)

const installedUsePrefix = "installed-use/1/"

// Each descriptor is 191 binary bytes, below Core's 512-character binding limit.
// Route indexes are meaningful only at the captured declaration and policy
// generations. No resolver searches for a replacement route.
type installedUseDependency struct {
	Principal    uuid.UUID
	Installation uuid.UUID
	Revision     uint64
	Declaration  uint64
	Envelope     uint64
}
type installedUseDescriptor struct {
	Owner        uuid.UUID
	Dependencies [3]installedUseDependency
	Routes       [2]uint16
	Variant      uint8 // 0 root; 1 read invoke; 2 auxiliary invoke; 3 auxiliary lookup
	Count        uint8
}

func IsInstalledUseBinding(id string) bool { return strings.HasPrefix(id, installedUsePrefix) }
func (d installedUseDescriptor) encode() string {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, d)
	return installedUsePrefix + base64.RawURLEncoding.EncodeToString(b.Bytes())
}
func decodeInstalledUse(id string) (installedUseDescriptor, error) {
	var d installedUseDescriptor
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(id, installedUsePrefix))
	if !IsInstalledUseBinding(id) || err != nil || len(raw) != binary.Size(d) || binary.Read(bytes.NewReader(raw), binary.BigEndian, &d) != nil || d.Owner == uuid.Nil || d.Count < 2 || d.Count > 3 || d.Variant > 3 || d.Variant > 1 && d.Count != 3 {
		return d, ErrNoOperationBinding
	}
	return d, nil
}

func (s *Service) installedUseAuthority(ctx context.Context, snapshot *installedModuleRegistry, org, principal string) (*ModuleCapabilityAuthority, installedUseDependency, error) {
	var dependency installedUseDependency
	a, _, err := s.authorizeModuleCapabilityFromSnapshot(ctx, snapshot, ModuleCaller{PrincipalID: principal, BoundOrg: org})
	if err != nil {
		return nil, dependency, err
	}
	source := snapshot.sources[principal]
	if source.binding == "" || source.generation == 0 || source.envelopeGeneration == 0 || source.binding != a.Grant.PresenceBinding {
		return nil, dependency, ErrModuleRegistryStale
	}
	if a.AppliedManifestGeneration != source.generation {
		s.requestRegistryPass()
		return nil, dependency, ErrModuleRegistryStale
	}
	dependency.Principal, err = uuid.Parse(principal)
	if err != nil {
		return nil, dependency, ErrNoOperationBinding
	}
	dependency.Installation, err = uuid.Parse(a.InstallationID)
	if err != nil {
		return nil, dependency, ErrNoOperationBinding
	}
	dependency.Revision, dependency.Declaration, dependency.Envelope = a.InstallationRevision, source.generation, source.envelopeGeneration
	return a, dependency, nil
}

// CaptureInstalledUse admits only one complete read-only invoke route. Scopes
// outside it stay subject to ordinary owner authorization; an auxiliary route
// is captured only when those requested scopes use it, and never supplies
// collection standing. The caller subsequently checks every human permission.
func (s *Service) CaptureInstalledUse(ctx context.Context, org, owner, audience string, requested []ModuleOperationScope) (string, []string, error) {
	snapshot := s.moduleRegistrySnapshot()
	origin, exists := snapshot.registry[ModulePrincipalID(audience)]
	if !exists {
		return "", nil, nil
	}
	var foreign []ModuleOperationScope
	for _, scope := range requested {
		if slices.Contains(origin.Resources, scope.ResourceKind) {
			continue
		}
		for _, candidate := range snapshot.registry {
			if candidate.Prefix != audience && slices.Contains(candidate.Resources, scope.ResourceKind) && slices.Contains(scope.Actions, "read") && len(scope.ResourceIDs) == 0 {
				foreign = append(foreign, ModuleOperationScope{ResourceKind: scope.ResourceKind, Actions: []string{"read"}})
				break
			}
		}
	}
	if len(foreign) == 0 {
		return "", nil, nil
	}
	ctx, release, err := s.BeginModuleAuthorityUse(ctx)
	if err != nil {
		return "", nil, err
	}
	defer release()
	a, dependency, err := s.installedUseAuthority(ctx, snapshot, org, ModulePrincipalID(audience))
	if err != nil {
		return "", nil, err
	}
	keys := slices.Sorted(maps.Keys(a.Grant.OperationAudiences))
	var selected []int
	for index, key := range keys {
		route := a.Grant.OperationAudiences[key]
		target := snapshot.registry[ModulePrincipalID(route.Audience)]
		readOnly := len(route.InvokeScopes) > 0
		for _, scope := range route.InvokeScopes {
			readOnly = readOnly && slices.Equal(scope.Actions, []string{"read"}) && len(scope.ResourceIDs) == 0 && slices.Contains(target.Resources, scope.ResourceKind)
		}
		if readOnly && operationScopesSubset(foreign, route.InvokeScopes) {
			selected = append(selected, index)
		}
	}
	// No qualifying route leaves ordinary role-based admission unchanged.
	if len(selected) != 1 {
		return "", nil, nil
	}
	d := installedUseDescriptor{Count: 2}
	d.Owner, err = uuid.Parse(owner)
	if err != nil {
		return "", nil, ErrNoOperationBinding
	}
	d.Dependencies[0], d.Routes[0] = dependency, uint16(selected[0])
	evidence := a.Grant.OperationAudiences[keys[selected[0]]]
	target, dependency, err := s.installedUseAuthority(ctx, snapshot, org, ModulePrincipalID(evidence.Audience))
	if err != nil {
		return "", nil, err
	}
	d.Dependencies[1] = dependency
	var auxiliary []int
	for index, key := range keys {
		if index == selected[0] {
			continue
		}
		route := a.Grant.OperationAudiences[key]
		overlap := intersectOperationScopes(requested, route.InvokeScopes)
		if len(overlap) == 0 {
			continue
		}
		// Content routes are never auxiliary execution authority.
		disjoint := true
		for _, scope := range route.InvokeScopes {
			disjoint = disjoint && !slices.Contains(target.Grant.Resources, scope.ResourceKind)
		}
		if disjoint {
			auxiliary = append(auxiliary, index)
		}
	}
	if len(auxiliary) > 1 {
		return "", nil, ErrNoOperationBinding
	}
	if len(auxiliary) == 1 {
		d.Count, d.Routes[1] = 3, uint16(auxiliary[0])
		route := a.Grant.OperationAudiences[keys[auxiliary[0]]]
		_, d.Dependencies[2], err = s.installedUseAuthority(ctx, snapshot, org, ModulePrincipalID(route.Audience))
		if err != nil {
			return "", nil, err
		}
	}
	return d.encode(), target.Grant.Resources, nil
}

func (s *Service) resolveInstalledUse(ctx context.Context, org, id string) (installedUseDescriptor, []*ModuleCapabilityAuthority, error) {
	d, err := decodeInstalledUse(id)
	if err != nil {
		return d, nil, err
	}
	snapshot := s.moduleRegistrySnapshot()
	authorities := make([]*ModuleCapabilityAuthority, d.Count)
	for i := range int(d.Count) {
		a, current, err := s.installedUseAuthority(ctx, snapshot, org, d.Dependencies[i].Principal.String())
		if err != nil {
			return d, nil, err
		}
		if current != d.Dependencies[i] {
			return d, nil, ErrNoOperationBinding
		}
		authorities[i] = a
	}
	return d, authorities, nil
}

func installedUseRoute(d installedUseDescriptor, authorities []*ModuleCapabilityAuthority, leg int) (string, ModuleOperationAudience, error) {
	routes := authorities[0].Grant.OperationAudiences
	keys := slices.Sorted(maps.Keys(routes))
	if leg < 0 || leg+1 >= int(d.Count) || int(d.Routes[leg]) >= len(keys) {
		return "", ModuleOperationAudience{}, ErrNoOperationBinding
	}
	key := keys[d.Routes[leg]]
	route := routes[key]
	if ModulePrincipalID(route.Audience) != d.Dependencies[leg+1].Principal.String() {
		return "", route, ErrNoOperationBinding
	}
	return key, route, nil
}

func (s *Service) installedUseBinding(ctx context.Context, org, id string) (WorkContextOperationBinding, error) {
	ctx, release, err := s.BeginModuleAuthorityUse(ctx)
	if err != nil {
		return WorkContextOperationBinding{}, err
	}
	defer release()
	d, authorities, err := s.resolveInstalledUse(ctx, org, id)
	if err != nil {
		return WorkContextOperationBinding{}, err
	}
	for leg := range int(d.Count) - 1 {
		if _, _, err := installedUseRoute(d, authorities, leg); err != nil {
			return WorkContextOperationBinding{}, err
		}
	}
	principal := d.Owner.String()
	if d.Variant != 0 {
		principal = d.Dependencies[0].Principal.String()
	}
	return WorkContextOperationBinding{ID: id, PrincipalID: principal, InstallationID: d.Dependencies[0].Installation.String(), Revision: 1, Incarnation: 1}, nil
}

// InstalledUseChild resolves an exact captured route and intersects the selected
// leg with the parent's effective scopes. Empty means refusal, never retain-all.
func (s *Service) InstalledUseChild(ctx context.Context, org, owner, id string, caller ModuleCaller, binding string, lookup bool, parent []ModuleOperationScope) (string, string, []ModuleOperationScope, error) {
	d, authorities, err := s.resolveInstalledUse(ctx, org, id)
	if err != nil {
		return "", "", nil, err
	}
	if d.Variant != 0 || d.Owner.String() != owner || caller.PrincipalID != d.Dependencies[0].Principal.String() || caller.BoundOrg != org {
		return "", "", nil, ErrNoOperationBinding
	}
	for leg := range int(d.Count) - 1 {
		key, route, err := installedUseRoute(d, authorities, leg)
		if err != nil {
			return "", "", nil, err
		}
		if key != binding {
			continue
		}
		scopes := route.InvokeScopes
		d.Variant = uint8(leg + 1)
		if lookup {
			if leg == 0 {
				return "", "", nil, ErrNoOperationBinding
			}
			scopes = route.LookupScopes
			d.Variant = 3
		}
		scopes = intersectOperationScopes(parent, scopes)
		if len(scopes) == 0 {
			return "", "", nil, ErrNoOperationBinding
		}
		return d.encode(), route.Audience, scopes, nil
	}
	return "", "", nil, ErrNoOperationBinding
}

// CheckInstalledUseActor proves module authority separately from human per-node
// grants. The actor is checked against its exact live route, never a personal
// agent row or an independent collection grant.
func (s *Service) CheckInstalledUseActor(ctx context.Context, org, owner, id, actor, audience string, scopes []ModuleOperationScope) error {
	d, authorities, err := s.resolveInstalledUse(ctx, org, id)
	if err != nil {
		return err
	}
	if d.Variant == 0 || owner != d.Owner.String() || actor != d.Dependencies[0].Principal.String() {
		return ErrNoOperationBinding
	}
	leg := 0
	if d.Variant > 1 {
		leg = 1
	}
	_, route, err := installedUseRoute(d, authorities, leg)
	if err != nil {
		return err
	}
	allowed := route.InvokeScopes
	if d.Variant == 3 {
		allowed = route.LookupScopes
	}
	if route.Audience != audience || len(scopes) == 0 || !operationScopesSubset(scopes, allowed) {
		return fmt.Errorf("%w: actor exceeds installed route", ErrNoOperationBinding)
	}
	return nil
}

// CheckInstalledUseRevisionSubject confirms one subject reported by a verifier.
// The signed credential's owner is checked separately by the ordinary grant oracle.
func (s *Service) CheckInstalledUseRevisionSubject(ctx context.Context, org, owner, id, principal string, scopes []ModuleOperationScope) error {
	d, authorities, err := s.resolveInstalledUse(ctx, org, id)
	if err != nil {
		return err
	}
	if owner != d.Owner.String() {
		return ErrNoOperationBinding
	}
	if principal == owner {
		return nil
	}
	if d.Variant == 0 {
		return ErrNoOperationBinding
	}
	leg := 0
	if d.Variant > 1 {
		leg = 1
	}
	_, route, err := installedUseRoute(d, authorities, leg)
	if err != nil {
		return err
	}
	return s.CheckInstalledUseActor(ctx, org, owner, id, principal, route.Audience, scopes)
}
