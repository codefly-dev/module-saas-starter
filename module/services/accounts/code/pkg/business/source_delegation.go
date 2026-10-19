package business

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/wool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	// ConsentedAudience and ConsentedScopes are what the person actually
	// agreed to, recorded at connect time. Empty on a row written before they
	// existed, which keeps exactly the digest rule it was written under.
	ConsentedAudience string
	ConsentedScopes   []ModuleOperationScope
	CreatedAt         time.Time
	RevokedAt         *time.Time
	RevokedReason     string
	RevokedBy         string
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
	// ErrSourceDelegationBindingUnavailable: the delegation's binding is not
	// visible in the view this call resolved — the organisation's consent does
	// not carry it, or the registry was replaced mid-call. Transient and
	// RETRYABLE, and deliberately not a revocation: see
	// sourceDelegationBindingUnavailable.
	ErrSourceDelegationBindingUnavailable = errors.New("source delegation binding is not available right now")
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

// sourceDelegationBindingState is why a delegation's binding could not be used,
// because the two reasons have opposite consequences and conflating them
// destroys grants.
type sourceDelegationBindingState int

const (
	// sourceDelegationBindingOK: the binding is declared and unchanged.
	sourceDelegationBindingOK sourceDelegationBindingState = iota
	// sourceDelegationBindingChanged: the module still declares this binding
	// and it is DIFFERENT — the audience or the scopes the person delegated
	// have moved. That is a durable fact about the declaration, so the
	// delegation no longer describes anything and is revoked.
	sourceDelegationBindingChanged
	// sourceDelegationBindingUnavailable: the binding is not in THIS VIEW at
	// all. A view is narrowed by the organisation's installation consent and is
	// derived from a registry that is replaced on every reconcile pass, so its
	// absence says nothing durable about what the person delegated: an
	// administrator editing consent, a pass in flight, or a registry briefly
	// empty behind its envelope fence all produce it.
	//
	// A delegation is a fact about a PERSON and a SOURCE. Revoking it because a
	// derived view momentarily does not show the binding destroys that fact to
	// report a condition that may be gone a second later — and it did: on
	// a deployed cell, a delegation was revoked as "binding_changed" NINE SECONDS
	// after being recorded, with a binding digest identical to the one stored,
	// because the recording read the raw registry and the mint read the
	// consent-narrowed one. The call is refused; the row is left alone.
	sourceDelegationBindingUnavailable
)

// currentSourceDelegationBinding resolves the binding a delegation names as the
// module declares it now, and says which kind of failure it hit: a binding that
// CHANGED is durable and revokes, a binding this view cannot see is transient
// and only refuses.
func (g ModulePrincipalGrant) currentSourceDelegationBinding(delegation *SourceDelegation) (ModuleOperationAudience, sourceDelegationBindingState) {
	binding, declared := g.OperationAudiences[delegation.BindingID]
	if !declared || binding.SourceDelegationScopes == nil {
		// Not offered here. Either the module no longer asks for it, or this
		// organisation's consent does not carry it, or this view is empty —
		// and nothing at this point can tell those apart, so none of them may
		// revoke.
		return ModuleOperationAudience{}, sourceDelegationBindingUnavailable
	}
	if validateOperationAudiences(g.Prefix, map[string]ModuleOperationAudience{delegation.BindingID: binding}) != nil {
		// Declared and unusable: a deployment defect, not a statement about
		// the person's grant.
		return ModuleOperationAudience{}, sourceDelegationBindingUnavailable
	}
	if SourceDelegationBindingDigest(binding) == delegation.BindingDigest {
		return binding, sourceDelegationBindingOK
	}
	// The binding moved. That is not by itself a reason to revoke.
	//
	// The digest covers the WHOLE binding, so comparing it alone revoked on
	// every edit to what the module asks for — including a NARROWING, which
	// can never need consent because it is strictly less authority. A module
	// asking for one action more therefore re-authorized every person who had
	// ever connected a source, and the only remedy this product offers is a
	// reconnect. One module upgrade meant everybody reconnects.
	//
	// Consent is about one question: is the binding now asking for MORE than
	// this person agreed to? Same or narrower is honoured — at the NARROWER
	// set, which is the live binding, so the person never gets more than they
	// agreed to and never less than the module still asks for. Wider revokes,
	// exactly as before.
	if delegation.ConsentedAudience == "" && delegation.ConsentedScopes == nil {
		// Recorded before consent was kept. Nothing to compare against, so the
		// old rule stands rather than being guessed around.
		return ModuleOperationAudience{}, sourceDelegationBindingChanged
	}
	if binding.Audience != delegation.ConsentedAudience {
		// A different destination is a different delegation, whatever the
		// scopes say.
		return ModuleOperationAudience{}, sourceDelegationBindingChanged
	}
	if !operationScopesSubset(binding.SourceDelegationScopes, delegation.ConsentedScopes) {
		return ModuleOperationAudience{}, sourceDelegationBindingChanged
	}
	return binding, sourceDelegationBindingOK
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

// sourceDelegationRecordMode is how recordSourceDelegationsTx treats a target
// that already holds an active delegation of the source.
//
// Two modes and not two functions, deliberately: the registry read that answers
// "which modules accept a delegation here" is a declared-value lookup under a
// capability the caller already authorised, and there must be exactly ONE path
// doing it — a second copy is a second place for the two behaviours to drift,
// and a second entry on the unrouted-capability list a reviewer has to judge.
type sourceDelegationRecordMode int

const (
	// sourceDelegationReplace is a CONNECT or RECONNECT. Every target's active
	// delegation of the source is revoked as replaced and re-recorded under the
	// connecting person, in one transaction, so a reconnect can never leave a
	// source with two delegations or none. Correct here because that person has
	// just (re-)established the source and the authority occurrences borrow is
	// theirs.
	sourceDelegationReplace sourceDelegationRecordMode = iota
	// sourceDelegationBackfill is a reference recorded for a source that is
	// ALREADY connected (DelegateSourceTriggers). It revokes nothing: a target
	// already holding an active delegation is left exactly as it is, and only a
	// target with none gets one.
	//
	// The distinction is issue #1112, M2. The backfill used to run the replace
	// mode, which took every delegation on the source, marked it `replaced` and
	// re-inserted it owned by the calling administrator: a personal source's
	// owner lost the delegation their own integrations mint from, and on a
	// shared source every in-flight child of an unrelated module failed its
	// re-check and everything after it ran under the administrator's authority
	// instead of the connector's. A backfill has re-established nothing, so it
	// has no business re-owning anything.
	sourceDelegationBackfill
)

// recordSourceDelegationsTx records the delegations a connect, reconnect or
// backfill of sourceID by actorID makes, inside the caller's organization
// transaction, for every module binding that accepts delegations in this
// organization. What it does about an existing one is the mode's to say.
//
// A connect by someone who is not, in this organization, an owner or admin
// records nothing: the platform operator's bypass of the connect check is not a
// role a later mint could re-check. The source then reads as having no
// delegation until an administrator of the organization reconnects it.
//
// It returns how many delegations it recorded, so a backfill's caller can tell
// "nothing was missing" from "no module accepts a delegation here" — two very
// different answers for an operator reading the page.
func (s *Service) recordSourceDelegationsTx(
	ctx context.Context, actorID, orgID, sourceID string, mode sourceDelegationRecordMode,
) (int, error) {
	targets := s.declaredModules().SourceDelegationTargets()
	if len(targets) == 0 {
		// ZERO TARGETS IS TWO DIFFERENT ANSWERS, and reporting success for both
		// is issue #1165. "No composed module accepts a source delegation" is a
		// legitimate deployment and recording nothing is right. "The module that
		// does accept one is missing from the registry because the envelope
		// withheld its binding" is a deployment defect, and recording nothing
		// there is how a person who reconnects a source is told, by the very
		// next sync, to connect or reconnect it — the thing they just did.
		//
		// So the refusal is on the fact that distinguishes them: what the
		// applied manifests asked for and the pass could not grant.
		if err := s.refuseWithheldSourceDelegationAuthority(ctx); err != nil {
			return 0, err
		}
		return 0, nil
	}
	role, err := s.store.SourceDelegationMemberRole(ctx, orgID, actorID)
	if err != nil {
		return 0, fmt.Errorf("resolve connecting member role: %w", err)
	}
	if !sourceDelegationConnectRole(role) {
		return 0, nil
	}
	recorded := 0
	for _, target := range targets {
		if mode == sourceDelegationBackfill {
			existing, err := s.store.ActiveSourceDelegation(ctx, sourceID, target.Prefix)
			if err != nil {
				return recorded, fmt.Errorf("read active source delegation: %w", err)
			}
			if existing != nil {
				continue
			}
		} else {
			replaced, err := s.store.RevokeSourceDelegations(ctx, SourceDelegationFilter{
				OrgID: orgID, SourceID: sourceID, ModulePrefix: target.Prefix,
			}, SourceDelegationReplaced, actorID)
			if err != nil {
				return recorded, fmt.Errorf("revoke replaced source delegation: %w", err)
			}
			for _, previous := range replaced {
				if err := s.emitSourceDelegationRevokedTx(ctx, actorID, previous, SourceDelegationReplaced); err != nil {
					return recorded, err
				}
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
			// Kept beside the digest so a later narrowing is honoured.
			ConsentedAudience: target.Binding.Audience,
			ConsentedScopes:   cloneOperationScopes(target.Binding.SourceDelegationScopes),
		}
		if err := s.store.InsertSourceDelegation(ctx, delegation); err != nil {
			return recorded, fmt.Errorf("record source delegation: %w", err)
		}
		if err := s.emitTx(ctx, actorID, "user", EventSourceDelegationCreated, "datasource", sourceID, orgID,
			sourceDelegationPayload(delegation)); err != nil {
			return recorded, err
		}
		recorded++
	}
	return recorded, nil
}

// recordSourceDelegationsOnConnectTx is the connect and reconnect call, whose
// callers do not read the count: a connect that recorded nothing it should have
// recorded is refused inside recordSourceDelegationsTx, so the count is no
// longer the one fact a caller here would have to act on.
func (s *Service) recordSourceDelegationsOnConnectTx(ctx context.Context, actorID, orgID, sourceID string) error {
	_, err := s.recordSourceDelegationsTx(ctx, actorID, orgID, sourceID, sourceDelegationReplace)
	return err
}

// refuseWithheldSourceDelegationAuthority refuses a connect, reconnect or
// backfill that would record no delegation because the module registry is
// INCOMPLETE, rather than because this deployment composes no module that
// accepts one.
//
// Two facts make it incomplete, and both are read from the pass that derived
// the registry rather than re-derived here:
//
//   - the pass could not grant an operation binding an applied manifest asked
//     for. This host cannot tell whether that binding was the one carrying
//     `source_delegation_scopes` — it is the ENVELOPE that says so, and the
//     envelope is what withheld it — so the refusal names every withheld
//     binding and leaves the operator to fix the one that matters;
//   - the registry is fenced: a newer envelope revision is committed than the
//     one it was derived from, so it serves nothing and "no target" is not an
//     answer about this host's modules at all.
//
// It is a refusal and not a warning because the alternative is the transaction
// that shipped: the credential written, the success event emitted, no delegation
// recorded, and nobody told. Rolling the connect back leaves the person
// something to retry once an administrator has fixed the envelope.
func (s *Service) refuseWithheldSourceDelegationAuthority(ctx context.Context) error {
	withheld := s.withheldModuleBindings()
	if len(withheld) == 0 && !s.moduleRegistryFenced() {
		return nil
	}
	reason := "the module registry was derived from an envelope revision older than the one now committed, so it grants nothing"
	if len(withheld) > 0 {
		named := make([]string, 0, len(withheld))
		for _, omission := range withheld {
			named = append(named, fmt.Sprintf("%s/%s (%s)", omission.Prefix, omission.BindingID, omission.Reason))
		}
		reason = "the module envelope withheld operation bindings the applied module manifests ask for: " + strings.Join(named, ", ")
	}
	wool.Get(ctx).In("business.source_delegation").Error(
		"refusing a connect that would record no source delegation while module authority is withheld",
		wool.Field("reason", reason))
	return status.Error(codes.FailedPrecondition,
		"This source cannot be connected yet: no composed module can be delegated its sync, because "+reason+
			". Connecting would record no delegation and every sync would then refuse with \"the source has no active "+
			"delegation to this module; a person must connect or reconnect it\". A platform administrator fixes it by "+
			"allowing the withheld binding in the module envelope (PlatformAdminService.PutModuleEnvelope); then connect again.")
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
//
// A NARROWING — the module binding stops holding the owner's authority over
// that source — so it runs under the policy log: appended and receipted before
// the row is marked, with the revocation and the receipt's commit in one
// transaction, and a refusal rather than an unwitnessed revocation when the log
// cannot be reached.
//
// That transaction is the policy log's control-plane one, because the receipt
// relation is control-plane only and the two writes must be atomic. The
// organisation's own scoping is not lost: both statements below carry orgID in
// their filter — the revoke through SourceDelegationFilter, the fallback read
// through GetSourceDelegation — so a delegation of another organisation is
// still ErrSourceDelegationNotFound rather than revocable by id.
//
// Revoking an already-revoked delegation appends an entry and commits nothing
// else, which is the correct trade and not an oversight: the alternative is
// reading the row before the append to find out, and a read then cannot bind
// what the transaction will find. An over-recorded entry replays to the same
// answer (see newPolicyLogOperationID); a narrowing that skipped the log
// because a pre-read said it would be a no-op would not.
func (s *Service) RevokeSourceDelegation(ctx context.Context, actorID, orgID, id string) (*SourceDelegation, error) {
	orgID = strings.TrimSpace(orgID)
	id = strings.TrimSpace(id)
	if orgID == "" || id == "" {
		return nil, errors.New("org id and delegation id are required")
	}
	var out *SourceDelegation
	if err := s.WithPolicyLoggedNarrowing(ctx,
		revokeSourceDelegationPolicyLogEntry(actorID, orgID, id),
		func(ctx context.Context) error {
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
// ModuleWorkContextAuthority names the module that USES the capability (the
// actor) — the delegating module on a plain mint, the binding's audience module
// on an exchange — with Tenant set to the delegation's organization, its
// installation there, and the build its pod runs; the owner is the person.
type SourceOperationContextAuthority struct {
	ModuleWorkContextAuthority
	// ModulePrefix is the using module's prefix, which with the delegation names
	// the operation binding the capability exercises (BindingID).
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
	Lookup bool
}

// BindingID is the operation binding this capability exercises: the delegation,
// used by this module.
func (a SourceOperationContextAuthority) BindingID() string {
	return SourceDelegationBindingID(a.Delegation.ID, a.ModulePrefix)
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
//   - the module proves its identity exactly as for its own Work Context (its
//     pod's projected ServiceAccount token, bound to the running build) —
//     otherwise ErrModuleIdentityDenied, or ErrExecutionUnbound when the host
//     could not establish the execution;
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
// The tenant is the delegation's organization; the current envelope must offer
// the module to it and that organization must have an active installation.
func (s *Service) AuthorizeSourceOperationContext(ctx context.Context, prefix, serviceAccountToken string, ref SourceDelegationRef) (SourceOperationContextAuthority, error) {
	snapshot := s.moduleRegistrySnapshot()
	identity, _, err := s.authorizeModuleIdentity(ctx, snapshot, prefix, serviceAccountToken)
	if err != nil {
		return SourceOperationContextAuthority{}, err
	}
	authority, err := s.authorizeSourceDelegation(ctx, snapshot, identity.PrincipalID, ref)
	if err != nil {
		return SourceOperationContextAuthority{}, err
	}
	// The execution this mint was bound to travels with it, so its record
	// names the pod and build that obtained the delegated capability.
	authority.Execution = identity.Execution
	authority.snapshot = snapshot
	if err := s.FenceModuleMint(ctx, authority.ModuleWorkContextAuthority); err != nil {
		return SourceOperationContextAuthority{}, err
	}
	return authority, nil
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
// module that uses it (the caller) and the delegation id, the delegation sealed
// as its operation binding — so every verifier reads the same live binding and a
// consumer cannot tell which arm admitted the work. Its scopes are the caller's
// binding narrowed to what the person delegated, which is the ceiling the
// signer's attenuation would have applied had there been a parent.
func (s *Service) AuthorizeDelegationReferenceExchange(
	ctx context.Context, caller ModuleCaller, delegationID, bindingID string, lookup bool,
) (SourceOperationContextAuthority, error) {
	ctx, releaseAuthority, fenceErr := s.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return SourceOperationContextAuthority{}, fenceErr
	}
	defer releaseAuthority()

	// ONE FENCED SNAPSHOT for both principals: the caller's grant and the
	// delegator's are read from the same registry, so a registry installed
	// while this decision is in flight can never pair one revision's caller
	// with another revision's delegator.
	callerAuthority, snapshot, err := s.authorizeModuleCapability(ctx, caller)
	if err != nil {
		return SourceOperationContextAuthority{}, err
	}
	grant := callerAuthority.Grant
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
		if delegation.OrgID != caller.BoundOrg {
			return ErrSourceDelegationInvalid
		}
		// The delegating module is the one the person granted to, which is not
		// the caller: the caller is the module that grant authorizes calling.
		delegator, _, err := s.authorizeModuleCapabilityFromSnapshot(ctx, snapshot, ModuleCaller{PrincipalID: ModulePrincipalID(delegation.ModulePrefix), BoundOrg: delegation.OrgID})
		if err != nil {
			return err
		}
		delegating := delegator.Grant
		granted, state := delegating.currentSourceDelegationBinding(delegation)
		switch state {
		case sourceDelegationBindingChanged:
			revokeAs, stale = SourceDelegationBindingChanged, delegation
			return nil
		case sourceDelegationBindingUnavailable:
			return ErrSourceDelegationBindingUnavailable
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
		// A person delegated this work, and a headless-only binding was granted
		// nothing it may do as a person: refused by name, before any scope is read.
		if binding.headlessOnly() {
			return errOperationAudienceHeadlessOnly(bindingID)
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
		if facts.EffectiveRevision() == 0 {
			return ErrSourceDelegationInvalid
		}
		// The actor is the module that USES the capability — the caller, the
		// audience the delegation's binding names — so its hop attests the build
		// its own pod is running, and the delegation is the operation binding the
		// hop exercises (business.SourceDelegationBindingID), read live by every
		// verifier. The caller authenticated with its module Work Context.
		authority = SourceOperationContextAuthority{
			ModuleWorkContextAuthority: ModuleWorkContextAuthority{
				PrincipalID: caller.PrincipalID, Tenant: delegation.OrgID,
				InstallationID: callerAuthority.InstallationID, InstallationRevision: callerAuthority.InstallationRevision,
				ProducerEpoch: callerAuthority.ProducerEpoch,
			},
			ModulePrefix:     grant.Prefix,
			OwnerPrincipalID: delegation.PrincipalID,
			Delegation:       *delegation,
			Audience:         binding.Audience,
			Scopes:           cloneOperationScopes(granted.SourceDelegationScopes),
			ActorScopes:      cloneOperationScopes(actorScopes),
			Lookup:           lookup,
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
	ctx context.Context, snapshot *installedModuleRegistry, principalID string, ref SourceDelegationRef,
) (SourceOperationContextAuthority, error) {
	ctx, releaseAuthority, fenceErr := s.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return SourceOperationContextAuthority{}, fenceErr
	}
	defer releaseAuthority()

	grant := snapshot.registry[principalID]
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
		installed, _, err := s.authorizeModuleCapabilityFromSnapshot(ctx, snapshot, ModuleCaller{PrincipalID: principalID, BoundOrg: delegation.OrgID})
		if err != nil {
			return err
		}
		grant = installed.Grant
		binding, state := grant.currentSourceDelegationBinding(delegation)
		switch state {
		case sourceDelegationBindingChanged:
			revokeAs, stale = SourceDelegationBindingChanged, delegation
			return nil
		case sourceDelegationBindingUnavailable:
			return ErrSourceDelegationBindingUnavailable
		}
		facts, err := s.store.SourceDelegationFacts(ctx, delegation.OrgID, delegation.PrincipalID, delegation.SourceID)
		if err != nil {
			return err
		}
		if reason := facts.revocation(); reason != "" {
			revokeAs, stale = reason, delegation
			return nil
		}
		if facts.EffectiveRevision() == 0 {
			// A member with no authorization revision is a database that broke
			// its own invariant; nothing can be sealed that a consumer could
			// later confirm.
			return ErrSourceDelegationInvalid
		}
		authority = SourceOperationContextAuthority{
			ModuleWorkContextAuthority: ModuleWorkContextAuthority{PrincipalID: principalID, Tenant: delegation.OrgID, InstallationID: installed.InstallationID, InstallationRevision: installed.InstallationRevision, ProducerEpoch: installed.ProducerEpoch},
			ModulePrefix:               grant.Prefix,
			OwnerPrincipalID:           delegation.PrincipalID,
			Delegation:                 *delegation,
			Audience:                   binding.Audience,
			Scopes:                     cloneOperationScopes(binding.SourceDelegationScopes),
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
		// A mint names the execution it was bound to. The reference exchange
		// records the same event with none: its caller authenticated with a
		// module Work Context, so no execution was bound at that call.
		if authority.Execution.PodUID != "" {
			for key, value := range authority.Execution.auditFields() {
				payload[key] = value
			}
		}
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

// confirmSourceDelegationBinding reports the delegation a context minted from a
// source delegation exercises, when the database and the declaration still
// support it: the operation binding its last hop sealed (bindingID) names that
// delegation used by exactly that hop's module, and the issuer's records entitle
// that module to use it (WorkContextOperationBinding); the delegation is active,
// in orgID and from ownerPrincipalID; the delegation's scopes cover every
// subject; and the person's authorization revision is still the one the context
// carries. Anything else is ErrSourceDelegationContextStale.
//
// The binding id is the pointer and never the authority: every fact behind it is
// re-read here, and a module not entitled to the delegation resolves to nothing.
func (s *Service) confirmSourceDelegationBinding(
	ctx context.Context, orgID, ownerPrincipalID, actorPrincipalID, bindingID string,
	revision uint64, subjects []ModuleOperationRevisionSubject,
) (*SourceDelegation, error) {
	ctx, releaseAuthority, fenceErr := s.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return nil, fenceErr
	}
	defer releaseAuthority()

	delegationID, usedBy, ok := parseSourceDelegationBindingID(bindingID)
	if !ok {
		return nil, staleBecause(ctx, "the binding id is not a source delegation's")
	}
	if ModulePrincipalID(usedBy) != actorPrincipalID {
		return nil, staleBecause(ctx, "the binding id was sealed by another module than the one presenting it")
	}
	live, err := s.WorkContextOperationBinding(ctx, orgID, bindingID)
	switch {
	case errors.Is(err, ErrNoOperationBinding), errors.Is(err, ErrSourceDelegationContextStale),
		errors.Is(err, ErrModuleInstallationInactive):
		return nil, staleBecause(ctx, "the issuer's records no longer entitle this module to that binding: "+err.Error())
	case err != nil:
		return nil, err
	case live.Revoked:
		return nil, staleBecause(ctx, "the operation binding is revoked")
	}
	var delegation *SourceDelegation
	err = s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		found, err := s.store.GetSourceDelegation(ctx, orgID, delegationID)
		if err != nil {
			return err
		}
		switch {
		case found == nil:
			return staleBecause(ctx, "no delegation of that id exists in this tenant")
		case !found.Active():
			return staleBecause(ctx, "the delegation is revoked")
		case found.PrincipalID != ownerPrincipalID:
			return staleBecause(ctx, "the delegation belongs to a different person than the context names as owner")
		}
		snapshot := s.moduleRegistrySnapshot()
		delegator, _, err := s.authorizeModuleCapabilityFromSnapshot(ctx, snapshot,
			ModuleCaller{PrincipalID: ModulePrincipalID(found.ModulePrefix), BoundOrg: orgID})
		if err != nil {
			return err
		}
		granted, state := delegator.Grant.currentSourceDelegationBinding(found)
		switch state {
		case sourceDelegationBindingChanged:
			return staleBecause(ctx, "the declaration behind this delegation changed since it was recorded")
		case sourceDelegationBindingUnavailable:
			return ErrSourceDelegationBindingUnavailable
		}
		for _, subject := range subjects {
			if !operationScopesSubset(subject.Scopes, granted.SourceDelegationScopes) {
				return staleBecause(ctx, "a presented subject carries scopes the delegation's binding does not grant")
			}
		}
		facts, err := s.store.SourceDelegationFacts(ctx, orgID, ownerPrincipalID, found.SourceID)
		if err != nil {
			return err
		}
		if reason := facts.revocation(); reason != "" {
			return staleBecause(ctx, "the delegation's facts revoke it: "+reason)
		}
		if facts.EffectiveRevision() != revision {
			return staleBecause(ctx, fmt.Sprintf(
				"the person's authorization revision is %d and the context was sealed at %d",
				facts.EffectiveRevision(), revision))
		}
		delegation = found
		return nil
	})
	if err != nil {
		return nil, err
	}
	return delegation, nil
}

// sourceDelegationShape classifies a context by its owner and actors. It is a
// source-delegation context when the owner is not a declared module principal
// and some actor is; the module grant returned is that actor's. Every other
// context is not one (ok false) and takes the checks it always took.
//
// The shape and the grant come from ONE registry snapshot, and a shape that is
// a source-delegation context is fenced against the committed envelope like
// every other module authority decision. The actor's installation in this
// organization is intersected before any delegation binding is accepted.
func (s *Service) sourceDelegationShape(
	ctx context.Context, orgID, ownerPrincipalID string, actorPrincipalIDs []string,
) (ModulePrincipalGrant, bool, error) {
	ctx, releaseAuthority, fenceErr := s.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return ModulePrincipalGrant{}, false, fenceErr
	}
	defer releaseAuthority()

	snapshot := s.moduleRegistrySnapshot()
	if _, ownerIsModule := snapshot.registry[ownerPrincipalID]; ownerIsModule {
		return ModulePrincipalGrant{}, false, nil
	}
	for _, actor := range actorPrincipalIDs {
		if _, isModule := snapshot.registry[actor]; isModule {
			authority, _, err := s.authorizeModuleCapabilityFromSnapshot(ctx, snapshot, ModuleCaller{PrincipalID: actor, BoundOrg: orgID})
			if err != nil {
				return ModulePrincipalGrant{}, true, err
			}
			return authority.Grant, true, nil
		}
	}
	return ModulePrincipalGrant{}, false, nil
}

// CheckSourceDelegationContextRevision confirms a Work Context minted from a
// source delegation — owned by a person, with a declared module principal as its
// actor — or exchanged from one. It reports handled=false for any other shape
// (the owner is itself a module principal, or no actor is one), so the caller
// falls through to the checks every other context takes.
//
// A handled context is confirmed only when exactly two subjects are presented,
// the owner then the module that uses the capability, and bindingID — the
// operation binding the context sealed — passes confirmSourceDelegationBinding.
// Anything else, a missing binding id included, is ErrSourceDelegationContextStale.
func (s *Service) CheckSourceDelegationContextRevision(
	ctx context.Context, orgID, ownerPrincipalID string, revision uint64,
	subjects []ModuleOperationRevisionSubject, bindingID string,
) (handled bool, err error) {
	ctx, releaseAuthority, fenceErr := s.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return false, fenceErr
	}
	defer releaseAuthority()

	actors := make([]string, 0, len(subjects))
	for _, subject := range subjects[min(1, len(subjects)):] {
		actors = append(actors, subject.PrincipalID)
	}
	_, ok, err := s.sourceDelegationShape(ctx, orgID, ownerPrincipalID, actors)
	if err != nil {
		return true, err
	}
	if !ok {
		return false, nil
	}
	// A sealed binding that names some other operation is not this checker's
	// context: it is a module exercising its own installed binding, which the
	// ordinary module-operation checks re-read in full. An EMPTY binding id is
	// still refused here rather than passed on, because a context of this shape
	// that seals no binding at all is exactly what this check exists to catch.
	if bindingID != "" && !namesASourceDelegation(bindingID) {
		return false, nil
	}
	if len(subjects) != 2 || subjects[0].PrincipalID != ownerPrincipalID {
		return true, ErrSourceDelegationContextStale
	}
	if _, err := s.confirmSourceDelegationBinding(ctx, orgID, ownerPrincipalID, subjects[1].PrincipalID,
		bindingID, revision, subjects); err != nil {
		return true, err
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
// actor hop, a declared module principal, and the operation binding it sealed
// (bindingID) must pass confirmSourceDelegationBinding for the parent's tenant,
// owner, revision and scopes; anything else is ErrSourceDelegationContextStale.
// The binding id in the token is only a pointer: the row, the source, the person
// and the binding are re-read, never trusted.
func (s *Service) ConfirmSourceDelegationParent(
	ctx context.Context, orgID, ownerPrincipalID string, revision uint64,
	ownerScopes []ModuleOperationScope, hops []SourceDelegationHop, bindingID string,
) (handled bool, delegation *SourceDelegation, err error) {
	ctx, releaseAuthority, fenceErr := s.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return false, nil, fenceErr
	}
	defer releaseAuthority()

	actors := make([]string, 0, len(hops))
	for _, hop := range hops {
		actors = append(actors, hop.PrincipalID)
	}
	_, ok, err := s.sourceDelegationShape(ctx, orgID, ownerPrincipalID, actors)
	if err != nil {
		return true, nil, err
	}
	if !ok {
		return false, nil, nil
	}
	// As in CheckSourceDelegationContextRevision: a binding that names another
	// operation belongs to the ordinary module-operation checks, not here.
	if bindingID != "" && !namesASourceDelegation(bindingID) {
		return false, nil, nil
	}
	if len(hops) != 1 || hops[0].DelegationID == "" {
		return true, nil, ErrSourceDelegationContextStale
	}
	subjects := []ModuleOperationRevisionSubject{
		{PrincipalID: ownerPrincipalID, Scopes: ownerScopes},
		{PrincipalID: hops[0].PrincipalID, Scopes: hops[0].Scopes},
	}
	delegation, err = s.confirmSourceDelegationBinding(ctx, orgID, ownerPrincipalID, hops[0].PrincipalID,
		bindingID, revision, subjects)
	if err != nil {
		return true, nil, err
	}
	if delegation.ID != hops[0].DelegationID {
		return true, nil, ErrSourceDelegationContextStale
	}
	return true, delegation, nil
}

// staleBecause is ErrSourceDelegationContextStale with the branch that reached
// it named in the operator's log.
//
// Eight independent conditions answered with one sentinel, and a consumer that
// retried every one of them identically. On a cell this is the difference
// between an operator reconnecting a source, writing an envelope, or reading a
// contract — and nothing told them which. A staging night went into recovering
// one of these by elimination.
//
// The ERROR is unchanged, deliberately: the sentinel is what the wire contract
// and every errors.Is branch key on, and a consumer must keep reading a stale
// context as stale. Only the log gains the reason, and it names conditions
// rather than any person's identifiers.
func staleBecause(ctx context.Context, because string) error {
	wool.Get(ctx).In("confirmSourceDelegationBinding").Warn(
		"a source delegation context was refused", wool.Field("because", because))
	return ErrSourceDelegationContextStale
}
