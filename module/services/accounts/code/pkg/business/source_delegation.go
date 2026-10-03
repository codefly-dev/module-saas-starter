package business

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// A source delegation is the record that a person, by connecting a datasource
// source (or reconnecting it), delegated that source's sync to one installed
// operation binding of a consuming module. The module later presents it to
// MintSourceOperationContext and receives a short-lived operation context owned
// by that person, in the source's organization, carrying exactly the binding's
// source_delegation_scopes — never the module's own authority.
//
// Which module and which binding a delegation names is decided by declared
// configuration, never by a request: the one operation binding of each module
// principal that declares `source_delegation_scopes`. Declaring it is the whole
// opt-in. A source is a host concept; the module that consumes it does not get
// to pick what it is delegated.
//
// The delegation, not the module's declared tenancy, authorizes the
// organization. `tenant` and `cross_tenant` are never consulted on these paths:
// a module bound to one tenant mints for a delegation in another, and a module
// holding `cross_tenant` gains nothing here — it reaches an organization only
// through a delegation a person there made, and only for the one binding that
// delegation names.
//
// The row only records what was delegated. Authority is re-derived on every
// mint and every revision check from current facts — the source still exists,
// the person is still an active member holding an org administrator role (the
// authority that let them connect), and the binding is unchanged — so a
// revocation that no event hook marked still takes effect immediately.

// SourceOperationContextTTL bounds a context minted from a source delegation:
// the operation-context ceiling, for the same reason (ModuleOperationContextTTL).
const SourceOperationContextTTL = ModuleOperationContextTTL

// Revocation reasons, as stored in source_delegations.revoked_reason.
const (
	// SourceDelegationRevokedByAdmin: an org administrator revoked it.
	SourceDelegationRevokedByAdmin = "revoked"
	// SourceDelegationReplaced: the source was reconnected; a new delegation
	// under the reconnecting person replaced this one in the same transaction.
	SourceDelegationReplaced = "replaced"
	// SourceDelegationSourceDeleted: the source no longer exists.
	SourceDelegationSourceDeleted = "source_deleted"
	// SourceDelegationMemberRemoved: the person is no longer a member of the org.
	SourceDelegationMemberRemoved = "member_removed"
	// SourceDelegationPermissionLost: the person is a member but no longer holds
	// the org administrator role that let them connect the source.
	SourceDelegationPermissionLost = "permission_lost"
	// SourceDelegationUserInactive: the person's account is deleted or suspended.
	SourceDelegationUserInactive = "user_inactive"
	// SourceDelegationBindingChanged: the module no longer declares the binding,
	// or its audience or delegation scopes changed since the person connected.
	SourceDelegationBindingChanged = "binding_changed"
)

// SourceDelegationRevocationReasons is every stored reason, in the order the
// wire enum declares them.
var SourceDelegationRevocationReasons = []string{
	SourceDelegationRevokedByAdmin,
	SourceDelegationReplaced,
	SourceDelegationSourceDeleted,
	SourceDelegationMemberRemoved,
	SourceDelegationPermissionLost,
	SourceDelegationUserInactive,
	SourceDelegationBindingChanged,
}

// SourceDelegation is one row of source_delegations.
type SourceDelegation struct {
	ID            string
	OrgID         string
	SourceID      string
	PrincipalID   string
	ModulePrefix  string
	BindingID     string
	BindingDigest string
	CreatedAt     time.Time
	RevokedAt     *time.Time
	RevokedReason string
	RevokedBy     string
}

// Active reports whether the delegation has not been revoked.
func (d *SourceDelegation) Active() bool { return d != nil && d.RevokedAt == nil }

// SourceDelegationFilter selects active delegations to revoke. OrgID is always
// required; every other set field narrows the match.
type SourceDelegationFilter struct {
	OrgID        string
	ID           string
	SourceID     string
	PrincipalID  string
	ModulePrefix string
}

// SourceDelegationFacts are the current facts a delegation is re-checked
// against. MemberRole and UserStatus are empty when there is no such row;
// the revisions are zero when there is no revision row.
type SourceDelegationFacts struct {
	SourceExists         bool
	MemberRole           string
	UserStatus           string
	OrganizationRevision uint64
	PrincipalRevision    uint64
}

// EffectiveRevision is the person's authorization revision in the org — the
// same value a person-owned Work Context seals (WorkContextAuthorityFacts).
func (f *SourceDelegationFacts) EffectiveRevision() uint64 {
	if f.PrincipalRevision > f.OrganizationRevision {
		return f.PrincipalRevision
	}
	return f.OrganizationRevision
}

// revocation reports why the facts no longer support a delegation, or "" when
// they do. The order is the order an administrator reads it in: a deleted
// source before anything about the person.
func (f *SourceDelegationFacts) revocation() string {
	switch {
	case !f.SourceExists:
		return SourceDelegationSourceDeleted
	case f.MemberRole == "":
		return SourceDelegationMemberRemoved
	case f.UserStatus != "active":
		return SourceDelegationUserInactive
	case !sourceDelegationConnectRole(f.MemberRole):
		return SourceDelegationPermissionLost
	default:
		return ""
	}
}

// sourceDelegationConnectRole reports whether an org role carries the authority
// that connecting (and reconnecting) a source requires: DatasourceService's
// AddSource, AddGitHubSource, SyncSource and MigrateGitHubSourceToApp all
// declare TENANT_REQUIREMENT_ORG_ADMIN, which the handlers enforce as the
// owner or admin role. A platform operator's bypass of that check is not a
// role in the organization, so it is never recorded as one.
func sourceDelegationConnectRole(role string) bool {
	return role == "owner" || role == "admin"
}

var (
	// ErrSourceDelegationMissing: the source has no active delegation to the
	// calling module. A person must connect or reconnect the source.
	ErrSourceDelegationMissing = errors.New("source has no active delegation to this module")
	// ErrSourceDelegationRevoked: the named delegation is revoked, or was just
	// found no longer supported by current facts and revoked.
	ErrSourceDelegationRevoked = errors.New("source delegation is revoked")
	// ErrSourceDelegationInvalid: the delegation does not exist or belongs to
	// another module. The two are deliberately indistinguishable.
	ErrSourceDelegationInvalid = errors.New("source delegation is not usable by this module")
	// ErrSourceDelegationNotFound: an administrator named a delegation that is
	// not in their organization.
	ErrSourceDelegationNotFound = errors.New("source delegation not found")
	// ErrSourceDelegationContextStale: a revision check presented a context the
	// delegation it was minted from no longer confirms.
	ErrSourceDelegationContextStale = errors.New("source delegation context is not confirmed by a current delegation")
	// ErrSourceDelegationLookupUnauthorized: the binding's receipt-lookup
	// scopes and what the person delegated do not overlap, so this delegation
	// authorizes producing an effect but not recovering its receipt. A refusal
	// rather than an empty capability: an unauthorized lookup must say so.
	ErrSourceDelegationLookupUnauthorized = errors.New("source delegation does not authorize receipt lookup for this binding")
)

// SourceDelegationBindingDigest fingerprints what a binding lets a delegation
// confer — its audience and its source_delegation_scopes, nothing else — so a
// delegation recorded against one shape is refused once the shape changes.
func SourceDelegationBindingDigest(binding ModuleOperationAudience) string {
	encoded, err := json.Marshal(struct {
		Audience string                 `json:"audience"`
		Scopes   []ModuleOperationScope `json:"scopes"`
	}{binding.Audience, binding.SourceDelegationScopes})
	if err != nil {
		// Plain strings and slices; they cannot fail to encode.
		panic("source delegation: binding does not encode: " + err.Error())
	}
	sum := sha256.Sum256(append([]byte("codefly.saas.source-delegation-binding.v1\x00"), encoded...))
	return hex.EncodeToString(sum[:])
}

// SourceDelegationContextRevision is the authorization revision a context
// minted from a delegation is sealed with. It binds the delegation row, the
// binding shape it was recorded against, and the person's authorization
// revision in the org, so CheckAuthorizationRevision stops confirming it the
// moment the delegation is revoked or replaced, the binding changes, or the
// person's membership, role or account status moves (the database bumps that
// revision on each). It is never zero.
func SourceDelegationContextRevision(delegationID, bindingDigest string, personRevision uint64) uint64 {
	material := strings.Join([]string{delegationID, bindingDigest, strconv.FormatUint(personRevision, 10)}, "\x00")
	sum := sha256.Sum256(append([]byte("codefly.saas.source-delegation-context.v1\x00"), material...))
	revision := binary.BigEndian.Uint64(sum[:8]) & (1<<63 - 1)
	if revision == 0 {
		return 1
	}
	return revision
}

// sourceDelegationBinding is the one binding of a module that accepts source
// delegations, if it declares one.
func (g ModulePrincipalGrant) sourceDelegationBinding() (string, ModuleOperationAudience, bool) {
	ids := make([]string, 0, len(g.OperationAudiences))
	for id := range g.OperationAudiences {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if binding := g.OperationAudiences[id]; binding.SourceDelegationScopes != nil {
			return id, binding, true
		}
	}
	return "", ModuleOperationAudience{}, false
}

// currentSourceDelegationBinding resolves the binding a delegation names as the
// module declares it now, refusing one that is gone, invalid, no longer
// accepting delegations, or changed in audience or scopes.
func (g ModulePrincipalGrant) currentSourceDelegationBinding(delegation *SourceDelegation) (ModuleOperationAudience, bool) {
	binding, declared := g.OperationAudiences[delegation.BindingID]
	if !declared || binding.SourceDelegationScopes == nil ||
		validateOperationAudiences(g.Prefix, map[string]ModuleOperationAudience{delegation.BindingID: binding}) != nil ||
		SourceDelegationBindingDigest(binding) != delegation.BindingDigest {
		return ModuleOperationAudience{}, false
	}
	return binding, true
}

// SourceDelegationTarget is one module binding a connect in an organization
// records a delegation for.
type SourceDelegationTarget struct {
	Prefix    string
	BindingID string
	Binding   ModuleOperationAudience
}

// SourceDelegationTargets lists, sorted by module prefix, the binding of every
// declared module that accepts source delegations. The declaration is the
// opt-in; the module's declared tenancy is deliberately not consulted, because
// the delegation a connect records is what authorizes the organization.
func (r ModulePrincipalRegistry) SourceDelegationTargets() []SourceDelegationTarget {
	var targets []SourceDelegationTarget
	for _, grant := range r {
		if id, binding, ok := grant.sourceDelegationBinding(); ok {
			targets = append(targets, SourceDelegationTarget{Prefix: grant.Prefix, BindingID: id, Binding: binding})
		}
	}
	slices.SortFunc(targets, func(a, b SourceDelegationTarget) int { return strings.Compare(a.Prefix, b.Prefix) })
	return targets
}

// ---------------------------------------------------------------------------
// Recording and revoking
// ---------------------------------------------------------------------------

// recordSourceDelegationsTx records the delegations a connect or reconnect of
// sourceID by actorID makes, inside the caller's organization transaction:
// for every module binding that accepts delegations in this organization, any
// active delegation of the source to that module is revoked as replaced and a
// new one is recorded under actorID. Both commit with the connect or not at
// all, so a reconnect can never leave a source with two delegations or none.
//
// A connect by someone who is not, in this organization, an owner or admin
// records nothing: the platform operator's bypass of the connect check is not a
// role a later mint could re-check. The source then reads as having no
// delegation until an administrator of the organization reconnects it.
func (s *Service) recordSourceDelegationsTx(ctx context.Context, actorID, orgID, sourceID string) error {
	targets := s.declaredModules().SourceDelegationTargets()
	if len(targets) == 0 {
		return nil
	}
	role, err := s.store.SourceDelegationMemberRole(ctx, orgID, actorID)
	if err != nil {
		return fmt.Errorf("resolve connecting member role: %w", err)
	}
	if !sourceDelegationConnectRole(role) {
		return nil
	}
	for _, target := range targets {
		replaced, err := s.store.RevokeSourceDelegations(ctx, SourceDelegationFilter{
			OrgID: orgID, SourceID: sourceID, ModulePrefix: target.Prefix,
		}, SourceDelegationReplaced, actorID)
		if err != nil {
			return fmt.Errorf("revoke replaced source delegation: %w", err)
		}
		for _, previous := range replaced {
			if err := s.emitSourceDelegationRevokedTx(ctx, actorID, previous, SourceDelegationReplaced); err != nil {
				return err
			}
		}
		delegation := &SourceDelegation{
			ID:            NewIDString(),
			OrgID:         orgID,
			SourceID:      sourceID,
			PrincipalID:   actorID,
			ModulePrefix:  target.Prefix,
			BindingID:     target.BindingID,
			BindingDigest: SourceDelegationBindingDigest(target.Binding),
		}
		if err := s.store.InsertSourceDelegation(ctx, delegation); err != nil {
			return fmt.Errorf("record source delegation: %w", err)
		}
		if err := s.emitTx(ctx, actorID, "user", EventSourceDelegationCreated, "datasource", sourceID, orgID,
			sourceDelegationPayload(delegation)); err != nil {
			return err
		}
	}
	return nil
}

// revokeSourceDelegationsTx revokes every active delegation the filter matches,
// inside the caller's transaction, and records each revocation. It is how the
// events that end a delegation — a source deleted, a member removed or
// demoted — mark the row in the same transaction as the event itself. The mint
// re-checks the same facts regardless, so a writer that bypasses these paths
// still cannot keep a delegation alive.
func (s *Service) revokeSourceDelegationsTx(ctx context.Context, actorID string, filter SourceDelegationFilter, reason string) error {
	revoked, err := s.store.RevokeSourceDelegations(ctx, filter, reason, actorID)
	if err != nil {
		return fmt.Errorf("revoke source delegations: %w", err)
	}
	for _, delegation := range revoked {
		if err := s.emitSourceDelegationRevokedTx(ctx, actorID, delegation, reason); err != nil {
			return err
		}
	}
	return nil
}

// emitSourceDelegationRevokedTx records a revocation a person's action caused.
// A revocation the mint detected is recorded by the mint itself, as the system.
func (s *Service) emitSourceDelegationRevokedTx(ctx context.Context, actorID string, delegation *SourceDelegation, reason string) error {
	return s.emitTx(ctx, actorID, "user", EventSourceDelegationRevoked, "datasource", delegation.SourceID, delegation.OrgID,
		sourceDelegationRevokedPayload(delegation, reason))
}

func sourceDelegationRevokedPayload(delegation *SourceDelegation, reason string) map[string]any {
	payload := sourceDelegationPayload(delegation)
	payload["reason"] = reason
	return payload
}

func sourceDelegationPayload(delegation *SourceDelegation) map[string]any {
	return map[string]any{
		"delegation_id": delegation.ID,
		"source_id":     delegation.SourceID,
		"principal_id":  delegation.PrincipalID,
		"module":        delegation.ModulePrefix,
		"binding_id":    delegation.BindingID,
	}
}

// ListSourceDelegations lists an organization's delegations, newest first: one
// source's when sourceID is set, and revoked ones too when includeRevoked.
func (s *Service) ListSourceDelegations(ctx context.Context, orgID, sourceID string, includeRevoked bool) ([]*SourceDelegation, error) {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return nil, errors.New("org id is required")
	}
	var out []*SourceDelegation
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		list, err := s.store.ListSourceDelegations(ctx, orgID, strings.TrimSpace(sourceID), includeRevoked)
		out = list
		return err
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// RevokeSourceDelegation revokes one delegation of the organization and
// returns it as stored. Revoking one already revoked changes and records
// nothing. A delegation that is not in the organization is
// ErrSourceDelegationNotFound, whether or not it exists elsewhere.
func (s *Service) RevokeSourceDelegation(ctx context.Context, actorID, orgID, id string) (*SourceDelegation, error) {
	orgID = strings.TrimSpace(orgID)
	id = strings.TrimSpace(id)
	if orgID == "" || id == "" {
		return nil, errors.New("org id and delegation id are required")
	}
	var out *SourceDelegation
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		revoked, err := s.store.RevokeSourceDelegations(ctx, SourceDelegationFilter{OrgID: orgID, ID: id}, SourceDelegationRevokedByAdmin, actorID)
		if err != nil {
			return err
		}
		if len(revoked) == 1 {
			out = revoked[0]
			return s.emitSourceDelegationRevokedTx(ctx, actorID, out, SourceDelegationRevokedByAdmin)
		}
		existing, err := s.store.GetSourceDelegation(ctx, orgID, id)
		if err != nil {
			return err
		}
		if existing == nil {
			return ErrSourceDelegationNotFound
		}
		out = existing
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Minting
// ---------------------------------------------------------------------------

// SourceDelegationRef names what a module mints from: a delegation id, or a
// source id whose active delegation to the calling module is used. Exactly one
// is set.
type SourceDelegationRef struct {
	DelegationID string
	SourceID     string
}

// SourceOperationContextAuthority is what one mint from a delegation asserts.
// ModuleWorkContextAuthority names the calling module's principal (the actor)
// with Tenant set to the delegation's organization; the owner is the person.
type SourceOperationContextAuthority struct {
	ModuleWorkContextAuthority
	ModulePrefix     string
	OwnerPrincipalID string
	Delegation       SourceDelegation
	Audience         string
	// Scopes is what the person delegated: the owner's authority in the minted
	// capability.
	Scopes []ModuleOperationScope
	// ActorScopes is what this one call may do with it — the caller's binding
	// narrowed to Scopes. Empty means the actor is granted Scopes whole, which
	// is what a plain mint from the delegation seals.
	ActorScopes []ModuleOperationScope
	// Lookup records that this capability was narrowed to receipt lookup, so
	// the durable record of the mint says which of the two it was.
	Lookup   bool
	Revision uint64
}

// WireScopes projects the binding's delegation scopes onto the signing surface.
func (a SourceOperationContextAuthority) WireScopes() []*gen.WorkContextScope {
	return wireOperationScopes(a.Scopes)
}

// WireActorScopes projects what this one call may do onto the signing surface.
func (a SourceOperationContextAuthority) WireActorScopes() []*gen.WorkContextScope {
	return wireOperationScopes(a.ActorScopes)
}

// AuthorizeSourceOperationContext resolves the context a composed module may be
// issued from a source delegation, failing closed on every check:
//
//   - the module proves its identity exactly as for its own Work Context (the
//     identity secret for its prefix) — otherwise ErrModuleRegistrationDenied;
//   - the delegation exists and names this module — otherwise
//     ErrSourceDelegationInvalid (by id) or
//     ErrSourceDelegationMissing (by source, when it has no active delegation
//     to this module);
//   - it is active — otherwise ErrSourceDelegationRevoked;
//   - the binding is still declared, still accepts delegations, and is
//     unchanged in audience and scopes; the source still exists; the person is
//     still an active member holding an owner or admin role. A failure here
//     revokes the row with the reason, records it, and is
//     ErrSourceDelegationRevoked.
//
// The tenant is the delegation's organization; nothing in the request names it,
// and the module's declared tenant and cross_tenant grant play no part.
func (s *Service) AuthorizeSourceOperationContext(ctx context.Context, prefix, secret string, ref SourceDelegationRef) (SourceOperationContextAuthority, error) {
	identity, err := s.ModuleAuthorizeWorkContext(prefix, secret)
	if err != nil {
		return SourceOperationContextAuthority{}, err
	}
	grant, registered := s.declaredModules()[identity.PrincipalID]
	if !registered {
		return SourceOperationContextAuthority{}, ErrModuleRegistrationDenied
	}
	return s.authorizeSourceDelegation(ctx, identity.PrincipalID, grant, ref)
}

// AuthorizeDelegationReferenceExchange resolves the capability one call of
// long-running delegated work may be issued, from a reference to a delegation
// and nothing else — no parent capability is presented, and none is held.
//
// This is what lets work outlive every Work Context. The caller keeps an
// identifier; the authority behind it is re-read here on every call, so a task
// running for an hour is checked as hard on its thousandth call as its first,
// and a revocation between two calls refuses the second.
//
// A reference is not a bearer capability, and what stops it becoming one is the
// same fact the parent-token arm relies on. A delegation is a grant to one
// binding of one module, and that binding declares the audience it may call.
// Only the module named as that audience may present the reference: an id
// learned by anyone else resolves to a delegation that does not authorize
// calling them, and is refused. The parent-token arm enforces exactly this by
// requiring the parent's audience to be the caller's prefix; here the same link
// is read from the delegation directly, because there is no parent to read it
// from.
//
// The capability is deliberately indistinguishable from the one the
// parent-token arm produces — owned by the person, one actor hop carrying the
// delegating module and the delegation id, sealed to the same revision — so the
// revision check confirms it and a consumer cannot tell which arm admitted the
// work. Its scopes are the caller's binding narrowed to what the person
// delegated, which is the ceiling the signer's attenuation would have applied
// had there been a parent.
func (s *Service) AuthorizeDelegationReferenceExchange(
	ctx context.Context, caller ModuleCaller, delegationID, bindingID string, lookup bool,
) (SourceOperationContextAuthority, error) {
	grant, err := s.moduleCapability(ctx, caller)
	if err != nil {
		return SourceOperationContextAuthority{}, err
	}
	if delegationID == "" || bindingID == "" {
		return SourceOperationContextAuthority{}, ErrSourceDelegationInvalid
	}

	var (
		authority SourceOperationContextAuthority
		revokeAs  string
		stale     *SourceDelegation
	)
	err = s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		delegation, err := s.store.GetSourceDelegation(ctx, "", delegationID)
		if err != nil {
			return err
		}
		if delegation == nil {
			return ErrSourceDelegationInvalid
		}
		if !delegation.Active() {
			return ErrSourceDelegationRevoked
		}
		// The delegating module is the one the person granted to, which is not
		// the caller: the caller is the module that grant authorizes calling.
		delegating, declared := s.declaredModules()[ModulePrincipalID(delegation.ModulePrefix)]
		if !declared {
			return ErrSourceDelegationInvalid
		}
		granted, current := delegating.currentSourceDelegationBinding(delegation)
		if !current {
			revokeAs, stale = SourceDelegationBindingChanged, delegation
			return nil
		}
		// The audience link, and the whole reason a reference is safe to hold.
		// Refused as invalid rather than as a different code, so a module that
		// obtained an id it was never the audience of learns nothing from the
		// refusal beyond the fact that it cannot use it.
		if granted.Audience != grant.Prefix {
			return ErrSourceDelegationInvalid
		}
		binding, installed := grant.OperationAudiences[bindingID]
		if !installed || validateOperationAudiences(grant.Prefix, map[string]ModuleOperationAudience{bindingID: binding}) != nil {
			return ErrSourceDelegationInvalid
		}
		// What this call may do: the caller's own binding, for invoking or for
		// recovering a receipt, and never more than the person delegated.
		reach := binding.InvokeScopes
		if lookup {
			reach = binding.LookupScopes
		}
		actorScopes := intersectOperationScopes(reach, granted.SourceDelegationScopes)
		if len(actorScopes) == 0 {
			if lookup {
				return ErrSourceDelegationLookupUnauthorized
			}
			return ErrSourceDelegationInvalid
		}
		facts, err := s.store.SourceDelegationFacts(ctx, delegation.OrgID, delegation.PrincipalID, delegation.SourceID)
		if err != nil {
			return err
		}
		if reason := facts.revocation(); reason != "" {
			revokeAs, stale = reason, delegation
			return nil
		}
		personRevision := facts.EffectiveRevision()
		if personRevision == 0 {
			return ErrSourceDelegationInvalid
		}
		authority = SourceOperationContextAuthority{
			// The actor is the delegating module, exactly as a mint from this
			// delegation would seal it — not the caller, which is the audience.
			ModuleWorkContextAuthority: ModuleWorkContextAuthority{PrincipalID: ModulePrincipalID(delegation.ModulePrefix), Tenant: delegation.OrgID},
			ModulePrefix:               delegation.ModulePrefix,
			OwnerPrincipalID:           delegation.PrincipalID,
			Delegation:                 *delegation,
			Audience:                   binding.Audience,
			Scopes:                     cloneOperationScopes(granted.SourceDelegationScopes),
			ActorScopes:                cloneOperationScopes(actorScopes),
			Lookup:                     lookup,
			Revision:                   SourceDelegationContextRevision(delegation.ID, delegation.BindingDigest, personRevision),
		}
		return nil
	})
	if err != nil {
		return SourceOperationContextAuthority{}, err
	}
	if stale != nil {
		if err := s.revokeStaleDelegation(ctx, caller.PrincipalID, stale, revokeAs); err != nil {
			return SourceOperationContextAuthority{}, err
		}
		return SourceOperationContextAuthority{}, ErrSourceDelegationRevoked
	}
	return authority, nil
}

// revokeStaleDelegation marks a delegation whose facts no longer support it,
// in its own transaction: the refusal that follows must not roll back the
// record of why the delegation ended.
func (s *Service) revokeStaleDelegation(ctx context.Context, principalID string, stale *SourceDelegation, reason string) error {
	return s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		revoked, err := s.store.RevokeSourceDelegations(ctx, SourceDelegationFilter{OrgID: stale.OrgID, ID: stale.ID}, reason, "")
		if err != nil {
			return err
		}
		for _, delegation := range revoked {
			if err := s.emitTx(ctx, principalID, "system", EventSourceDelegationRevoked, "datasource",
				delegation.SourceID, delegation.OrgID, sourceDelegationRevokedPayload(delegation, reason)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) authorizeSourceDelegation(
	ctx context.Context, principalID string, grant ModulePrincipalGrant, ref SourceDelegationRef,
) (SourceOperationContextAuthority, error) {
	if (ref.DelegationID == "") == (ref.SourceID == "") {
		return SourceOperationContextAuthority{}, ErrSourceDelegationInvalid
	}

	var (
		authority SourceOperationContextAuthority
		revokeAs  string
		stale     *SourceDelegation
	)
	err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var delegation *SourceDelegation
		var err error
		if ref.DelegationID != "" {
			delegation, err = s.store.GetSourceDelegation(ctx, "", ref.DelegationID)
			if err != nil {
				return err
			}
			if delegation == nil || delegation.ModulePrefix != grant.Prefix {
				return ErrSourceDelegationInvalid
			}
			if !delegation.Active() {
				return ErrSourceDelegationRevoked
			}
		} else {
			delegation, err = s.store.ActiveSourceDelegation(ctx, ref.SourceID, grant.Prefix)
			if err != nil {
				return err
			}
			if delegation == nil {
				return ErrSourceDelegationMissing
			}
		}
		binding, current := grant.currentSourceDelegationBinding(delegation)
		if !current {
			revokeAs, stale = SourceDelegationBindingChanged, delegation
			return nil
		}
		facts, err := s.store.SourceDelegationFacts(ctx, delegation.OrgID, delegation.PrincipalID, delegation.SourceID)
		if err != nil {
			return err
		}
		if reason := facts.revocation(); reason != "" {
			revokeAs, stale = reason, delegation
			return nil
		}
		personRevision := facts.EffectiveRevision()
		if personRevision == 0 {
			// A member with no authorization revision is a database that broke
			// its own invariant; nothing can be sealed that a consumer could
			// later confirm.
			return ErrSourceDelegationInvalid
		}
		authority = SourceOperationContextAuthority{
			ModuleWorkContextAuthority: ModuleWorkContextAuthority{PrincipalID: principalID, Tenant: delegation.OrgID},
			ModulePrefix:               grant.Prefix,
			OwnerPrincipalID:           delegation.PrincipalID,
			Delegation:                 *delegation,
			Audience:                   binding.Audience,
			Scopes:                     cloneOperationScopes(binding.SourceDelegationScopes),
			Revision:                   SourceDelegationContextRevision(delegation.ID, delegation.BindingDigest, personRevision),
		}
		return nil
	})
	if err != nil {
		return SourceOperationContextAuthority{}, err
	}
	if stale != nil {
		if err := s.revokeStaleDelegation(ctx, principalID, stale, revokeAs); err != nil {
			return SourceOperationContextAuthority{}, err
		}
		return SourceOperationContextAuthority{}, ErrSourceDelegationRevoked
	}
	return authority, nil
}

// RecordSourceOperationContextMint commits the durable record of one use of a
// delegation, once the capability exists; the caller withholds the capability
// when it cannot. It re-reads the delegation in the same transaction and
// refuses (ErrSourceDelegationRevoked) when it was revoked while the capability
// was being signed, so a revocation that raced the mint is never outrun.
func (s *Service) RecordSourceOperationContextMint(ctx context.Context, authority SourceOperationContextAuthority) error {
	return s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		current, err := s.store.GetSourceDelegation(ctx, authority.Delegation.OrgID, authority.Delegation.ID)
		if err != nil {
			return err
		}
		if !current.Active() {
			return ErrSourceDelegationRevoked
		}
		payload := sourceDelegationPayload(&authority.Delegation)
		payload["audience"] = authority.Audience
		payload["scopes"] = operationScopeGrants(authority.Scopes)
		payload["lookup"] = authority.Lookup
		return s.emitTx(ctx, authority.PrincipalID, "system", EventSourceDelegationUsed, "datasource",
			authority.Delegation.SourceID, authority.Delegation.OrgID, payload)
	})
}

func cloneOperationScopes(scopes []ModuleOperationScope) []ModuleOperationScope {
	out := make([]ModuleOperationScope, 0, len(scopes))
	for _, scope := range scopes {
		out = append(out, ModuleOperationScope{
			ResourceKind: scope.ResourceKind,
			Actions:      slices.Clone(scope.Actions),
			ResourceIDs:  slices.Clone(scope.ResourceIDs),
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Revision check and parent re-check
// ---------------------------------------------------------------------------

// confirmSourceDelegationTx reports whether one delegation, as the database and
// the declaration say now, still confirms a context sealed with revision for
// ownerID in orgID whose subjects are the given ones. It is the one rule the
// revision check and the exchange of a delegation-bearing parent share: the
// delegation is active, in orgID, from ownerID, to the module grant names; its
// binding is unchanged and its delegation scopes cover every subject; the
// source exists and the person is still an active owner or admin; and the
// revision recomputes to the sealed one at the person's current authorization
// revision. Runs inside the caller's control-plane transaction.
func (s *Service) confirmSourceDelegationTx(
	ctx context.Context, grant ModulePrincipalGrant, delegation *SourceDelegation,
	orgID, ownerPrincipalID string, revision uint64, subjects []ModuleOperationRevisionSubject,
) (bool, error) {
	if !delegation.Active() || delegation.OrgID != orgID || delegation.PrincipalID != ownerPrincipalID ||
		delegation.ModulePrefix != grant.Prefix {
		return false, nil
	}
	binding, current := grant.currentSourceDelegationBinding(delegation)
	if !current {
		return false, nil
	}
	for _, subject := range subjects {
		if !operationScopesSubset(subject.Scopes, binding.SourceDelegationScopes) {
			return false, nil
		}
	}
	facts, err := s.store.SourceDelegationFacts(ctx, orgID, ownerPrincipalID, delegation.SourceID)
	if err != nil {
		return false, err
	}
	if facts.revocation() != "" || facts.EffectiveRevision() == 0 {
		return false, nil
	}
	return SourceDelegationContextRevision(delegation.ID, delegation.BindingDigest, facts.EffectiveRevision()) == revision, nil
}

// sourceDelegationShape classifies a context by its owner and actors. It is a
// source-delegation context when the owner is not a declared module principal
// and some actor is; the module grant returned is that actor's. Every other
// context is not one (ok false) and takes the checks it always took.
func (s *Service) sourceDelegationShape(ownerPrincipalID string, actorPrincipalIDs []string) (ModulePrincipalGrant, bool) {
	if _, ownerIsModule := s.declaredModules()[ownerPrincipalID]; ownerIsModule {
		return ModulePrincipalGrant{}, false
	}
	for _, actor := range actorPrincipalIDs {
		if grant, isModule := s.declaredModules()[actor]; isModule {
			return grant, true
		}
	}
	return ModulePrincipalGrant{}, false
}

// CheckSourceDelegationContextRevision confirms a Work Context minted from a
// source delegation — owned by a person, with a declared module principal as
// its actor — or exchanged from one. It reports handled=false for any other
// shape (the owner is itself a module principal, or no actor is one), so the
// caller falls through to the checks every other context takes.
//
// A handled context is confirmed only when exactly two subjects are presented,
// the owner then one declared module principal, and some ACTIVE delegation from
// the owner in orgID to that module passes confirmSourceDelegationTx. Anything
// else is ErrSourceDelegationContextStale. The module's declared tenancy is not
// consulted: the delegation is what authorizes orgID.
func (s *Service) CheckSourceDelegationContextRevision(
	ctx context.Context, orgID, ownerPrincipalID string, revision uint64, subjects []ModuleOperationRevisionSubject,
) (handled bool, err error) {
	actors := make([]string, 0, len(subjects))
	for _, subject := range subjects[min(1, len(subjects)):] {
		actors = append(actors, subject.PrincipalID)
	}
	grant, ok := s.sourceDelegationShape(ownerPrincipalID, actors)
	if !ok {
		return false, nil
	}
	if len(subjects) != 2 || subjects[0].PrincipalID != ownerPrincipalID ||
		subjects[1].PrincipalID != ModulePrincipalID(grant.Prefix) {
		return true, ErrSourceDelegationContextStale
	}
	confirmed := false
	err = s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		delegations, err := s.store.ActiveSourceDelegationsForPrincipal(ctx, orgID, ownerPrincipalID, grant.Prefix)
		if err != nil {
			return err
		}
		for _, delegation := range delegations {
			ok, err := s.confirmSourceDelegationTx(ctx, grant, delegation, orgID, ownerPrincipalID, revision, subjects)
			if err != nil {
				return err
			}
			if ok {
				confirmed = true
				return nil
			}
		}
		return nil
	})
	if err != nil {
		return true, err
	}
	if !confirmed {
		return true, ErrSourceDelegationContextStale
	}
	return true, nil
}

// SourceDelegationHop is one actor hop of a verified Work Context, as the
// parent re-check reads it.
type SourceDelegationHop struct {
	PrincipalID  string
	DelegationID string
	Scopes       []ModuleOperationScope
}

// ConfirmSourceDelegationParent re-checks a verified parent Work Context that
// carries a source delegation — the shape MintSourceOperationContext signs, or
// an exchange of it — and returns the delegation that authorizes its tenant.
//
// It reports handled=false for any other shape, so a parent without a module
// actor keeps exactly the checks it always had. A handled parent must have one
// actor hop, a declared module principal, whose delegation id names a
// delegation that confirmSourceDelegationTx accepts now for the parent's
// tenant, owner, revision and scopes; anything else is
// ErrSourceDelegationContextStale. The id in the token is only a pointer: the
// row, the source, the person and the binding are re-read, never trusted.
func (s *Service) ConfirmSourceDelegationParent(
	ctx context.Context, orgID, ownerPrincipalID string, revision uint64,
	ownerScopes []ModuleOperationScope, hops []SourceDelegationHop,
) (handled bool, delegation *SourceDelegation, err error) {
	actors := make([]string, 0, len(hops))
	for _, hop := range hops {
		actors = append(actors, hop.PrincipalID)
	}
	grant, ok := s.sourceDelegationShape(ownerPrincipalID, actors)
	if !ok {
		return false, nil, nil
	}
	if len(hops) != 1 || hops[0].PrincipalID != ModulePrincipalID(grant.Prefix) || hops[0].DelegationID == "" {
		return true, nil, ErrSourceDelegationContextStale
	}
	subjects := []ModuleOperationRevisionSubject{
		{PrincipalID: ownerPrincipalID, Scopes: ownerScopes},
		{PrincipalID: hops[0].PrincipalID, Scopes: hops[0].Scopes},
	}
	err = s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		found, err := s.store.GetSourceDelegation(ctx, orgID, hops[0].DelegationID)
		if err != nil || found == nil {
			return err
		}
		ok, err := s.confirmSourceDelegationTx(ctx, grant, found, orgID, ownerPrincipalID, revision, subjects)
		if ok {
			delegation = found
		}
		return err
	})
	if err != nil {
		return true, nil, err
	}
	if delegation == nil {
		return true, nil, ErrSourceDelegationContextStale
	}
	return true, delegation, nil
}
