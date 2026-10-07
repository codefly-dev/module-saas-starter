package business

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"accounts/pkg/eventcatalog"
)

// The typed audit-event registry. This Go catalog is the single source of
// truth for audit event types, mirroring the permission/entitlement catalog
// pattern in service_vocabulary.go: the audit_event_types database table and
// the generated TypeScript facet are projections of this list, never parallel
// inventories. See docs/adr/0003-typed-audit-event-registry.md.

// EventType is the audit-event discriminator (Single Table Inheritance). Every
// audit row carries one, and producers reference the registered names below.
type EventType string

// AuditCategory groups event types for search facets and analytics roll-ups.
type AuditCategory string

const (
	CategoryIdentity     AuditCategory = "identity"
	CategoryAccess       AuditCategory = "access"
	CategorySecurity     AuditCategory = "security"
	CategoryBilling      AuditCategory = "billing"
	CategoryOrganization AuditCategory = "organization"
	CategoryLifecycle    AuditCategory = "lifecycle"
	CategorySystem       AuditCategory = "system"
	// CategorySolution groups the event types a registered solution declared in
	// its own registration manifest (solution_audit_events.go). No code-owned
	// type carries it.
	CategorySolution AuditCategory = "solution"
)

// FieldKind is the declared type of one payload field. Payloads are validated
// against these at the emit choke point; the JSON Schema projection stored in
// audit_event_types.payload_schema is generated from the same fields.
//
// FieldInt is a whole number. A Go int of any width is one; a float64 — the
// form every number takes after a protobuf Struct or JSON — is one only when it
// is whole and within ±maxExactFloatInt.
//
// FieldNumber is a finite IEEE-754 double — a fraction, a score, a ratio — and
// is the kind to declare for any value that is not a count. NaN and ±Inf are
// rejected: no aggregate over them means anything, and jsonb cannot store them.
type FieldKind string

const (
	FieldString      FieldKind = "string"
	FieldUUID        FieldKind = "uuid"
	FieldInt         FieldKind = "int"
	FieldNumber      FieldKind = "number"
	FieldBool        FieldKind = "bool"
	FieldEnum        FieldKind = "enum"
	FieldStringArray FieldKind = "string_array"
)

// maxExactFloatInt is 2^53-1, the largest magnitude at which every int has its
// own float64. Beyond it a float64 stands for more than one int, so a FieldInt
// that arrived as a float64 cannot be trusted to be the value that was sent.
const maxExactFloatInt = 1<<53 - 1

// PayloadField declares one field of a typed audit payload. PII marks a field
// as personally identifying: it is stripped from every export path so audit
// destinations (the customer's S3 bucket, CSV/JSON downloads) never receive it.
//
// Required on a string-valued kind (string, uuid, enum) means a nonempty,
// non-blank value, not merely a present key — see validateField. Declaring it
// is therefore the whole of the contract: there is no second flag to remember,
// because the hole this closes was created by exactly that kind of forgetting.
//
// MaxLen bounds a string-valued field's length; zero leaves it unbounded. It is
// what keeps a field declared to carry a short identifier from carrying prose:
// the audit spine is fanned out to external destinations, so a field with no
// bound is a field an emitter can post a failure transcript through.
type PayloadField struct {
	Name     string
	Kind     FieldKind
	Required bool
	MaxLen   int
	Enum     []string
	PII      bool
}

// AuditEventTypeRow is a row of the audit_event_types projection table, read
// back by the parity test and the query/UI facet.
type AuditEventTypeRow struct {
	Name       string
	Namespace  string
	Version    int
	Category   string
	Owner      string
	Deprecated bool
}

// AuditNamespace is this module's event namespace: the first segment of every
// event type it mints. A composed workspace hosts several modules against one
// audit spine, so the namespace — not the owning service — is what keeps two
// modules from minting the same event_type. It matches the saas.* protobuf
// package family and the domain-event naming law in EVENTS.md.
const AuditNamespace = "saas"

// AuditDurability declares how an event type's record must reach the log. It is
// the classification the emit choke points and the durability gate enforce, and
// every registered definition carries one — a new event type cannot be added
// without deciding which it is.
type AuditDurability string

const (
	// DurabilityTransactional marks a privileged write: a change to who can do
	// what, or the issue of a credential that grants it. Its audit row and
	// webhook fan-out are written on a transaction the caller's success depends
	// on (Service.emitTx / emitEntryTx), so the record and the change it
	// describes commit together — and a failed audit write fails the operation
	// rather than returning success with no record.
	DurabilityTransactional AuditDurability = "transactional"
	// DurabilityObservational marks a record of something no domain transaction
	// owns: an authentication outcome, a denial, a read, or an outcome produced
	// by an external provider. It is emitted on the emitter's own transaction
	// (Service.emit) precisely so it survives a rolled-back domain write.
	DurabilityObservational AuditDurability = "observational"
)

// AuditRetentionClass declares how long an event type's full details must be
// kept once a warehouse is the audit store of record (ADR 0009). The envelope —
// who, which organization, what type, which target, when — and a hash of the
// details are kept for the compliance window whatever the class; the class
// decides where the details themselves go.
//
// It is its own field, deliberately not derived from Category: CategorySecurity
// names a narrower set (sign-in and MFA), and most security-class events —
// role changes, credential issuance, configuration — sit in other categories.
type AuditRetentionClass string

const (
	// RetentionSecurity is the record of who could get in and what they were
	// allowed to do: authentication (including its failures), credentials issued
	// and revoked, permission, role, membership, sharing and delegation changes,
	// administrative and operator actions, data leaving the platform (exports,
	// replays), and configuration changes. Its full details are kept for the
	// compliance window, in the events table and the locked archive alike.
	RetentionSecurity AuditRetentionClass = "security"
	// RetentionContent is everything else: what happened to a tenant's own
	// content and operations. Its full details are kept for the shorter,
	// configured content window; the archive holds only its envelope and hash.
	RetentionContent AuditRetentionClass = "content"
)

// AuditEventDefinition is one registered event type. Namespace is the collision
// key (always the leading segment of Type); Owner names the service that emits
// it, which is a different axis entirely. Durability says how the record must
// be committed.
//
// RequiresEntry marks a type whose record is meaningless without the resource it
// happened to, and RequiresIdempotencyKey one whose emitter must name the
// operation it is recording so a retried emit collapses. The emit surface
// refuses either when it is missing. Both are properties of the event rather
// than of the transport because the transport is shared —
// ModuleEmitAuditEventRequest carries one optional entry_id and one optional
// idempotency_key for every type, and the types that legitimately have neither
// (an observation about a whole solution; an event that is genuinely distinct on
// every emit) go through the same two fields.
type AuditEventDefinition struct {
	Type        EventType
	Namespace   string
	Version     int
	Category    AuditCategory
	Owner       string
	Description string
	Durability  AuditDurability
	// Retention is the type's retention class, required the way Durability is:
	// every catalog entry is wrapped in securityRetained or contentRetained, and
	// the index refuses one that is not.
	Retention              AuditRetentionClass
	RequiresEntry          bool
	RequiresIdempotencyKey bool
	Fields                 []PayloadField
	// MarksUserJoined says this type records a person joining the tenant for
	// the first time. It is the registry's answer to "who is a new user",
	// served over ListAuditEventTypes so no client has to keep its own list of
	// names in step with this one.
	MarksUserJoined bool
	// Visibility is how far an event of this type may travel, for a type whose
	// producer declares it: AuditVisibilityTenant or AuditVisibilityExternal.
	// A code-owned definition leaves it empty and is answered by the composed
	// event catalog instead (eventcatalog.IsExternalPublished), which is where
	// compose records the same fact for every published type. Read it through
	// ResolvedAuditEvent.ExternallyDeliverable, never directly, so the two
	// halves of the registry are never consulted separately.
	Visibility string
}

// EffectiveVisibility is how far an event of this type may travel, over the
// WHOLE registry. A declared type states it on the definition; a code-owned
// type leaves it empty and is answered by the composed event catalog, where
// module-compose recorded the same fact at compose time. Nothing else may read
// only one of the two halves — a gate that did is exactly how a declared type
// came to be subscribable and undeliverable.
func (d AuditEventDefinition) EffectiveVisibility() string {
	if d.Visibility != "" {
		return d.Visibility
	}
	if eventcatalog.IsExternalPublished(string(d.Type)) {
		return AuditVisibilityExternal
	}
	return AuditVisibilityTenant
}

// ExternallyDeliverable reports whether an event of this type may be delivered
// to a tenant's outbound webhook endpoint.
func (d AuditEventDefinition) ExternallyDeliverable() bool {
	return d.EffectiveVisibility() == AuditVisibilityExternal
}

// mutation registers a privileged write (DurabilityTransactional); observation
// registers a record no domain transaction owns (DurabilityObservational).
// There is deliberately no durability-less constructor: the classification is
// what the durability gate reads, so a new event type has to state it.
func mutation(t EventType, cat AuditCategory, desc string, fields ...PayloadField) AuditEventDefinition {
	return def(t, DurabilityTransactional, cat, desc, fields...)
}

func observation(t EventType, cat AuditCategory, desc string, fields ...PayloadField) AuditEventDefinition {
	return def(t, DurabilityObservational, cat, desc, fields...)
}

// def is the terse constructor for a definition with a v1 payload schema owned
// by accounts. Almost every event today carries no structured payload; the
// fields declared here are the contract producers fill in as payloads are
// enriched.
func def(t EventType, dur AuditDurability, cat AuditCategory, desc string, fields ...PayloadField) AuditEventDefinition {
	return AuditEventDefinition{
		Type: t, Namespace: AuditNamespace, Version: 1, Category: cat,
		Owner: "accounts", Description: desc, Durability: dur, Fields: fields,
	}
}

// revised marks a definition whose payload or field meaning changed after the
// type was published. `type` is immutable (see EVENTS.md), so the version is
// the only signal that separates rows written under the old contract from rows
// written under the new one — and it is the signal
// TestAuditCatalog_TypesCarryNoVersionSuffix points producers at. Bumping it
// costs no migration: SyncAuditEventTypes upserts the projection at boot and
// normalize stamps schema_version from here.
func revised(d AuditEventDefinition, version int) AuditEventDefinition {
	d.Version = version
	return d
}

// requiresEntry marks a definition whose record names a specific resource, and
// requiresIdempotencyKey one whose emitter must name the operation being
// recorded. Wrappers rather than constructor arguments for the same reason
// revised is one: they read at the declaration, and every type that does not say
// this keeps today's behaviour.
func requiresEntry(d AuditEventDefinition) AuditEventDefinition {
	d.RequiresEntry = true
	return d
}

// An empty idempotency_key deduplicates nothing, so for a type whose emitter
// retries — a queue operation reporting per item — accepting one is accepting a
// double-counted row on every replayed response. Refusing is safe in a way that
// deriving a key host-side would not be: the emitter, not the host, knows
// whether two emits are one operation retried or two real operations, and a key
// the host guessed from the payload would silently suppress the second.
func requiresIdempotencyKey(d AuditEventDefinition) AuditEventDefinition {
	d.RequiresIdempotencyKey = true
	return d
}

// securityRetained and contentRetained declare a definition's retention class.
// Every catalog entry carries exactly one of them, outermost, so the class is
// read at the declaration and a new type cannot be added without choosing.
func securityRetained(d AuditEventDefinition) AuditEventDefinition {
	d.Retention = RetentionSecurity
	return d
}

func contentRetained(d AuditEventDefinition) AuditEventDefinition {
	d.Retention = RetentionContent
	return d
}

func str(name string) PayloadField { return PayloadField{Name: name, Kind: FieldString} }

// catalogCount is a count the built-in role catalog import records, carried as
// the decimal string the importer writes, so a row written before this field was
// declared reads the same as one written after.
func catalogCount(name string) PayloadField {
	return PayloadField{Name: name, Kind: FieldString, MaxLen: 10}
}

// withCatalogProvenance adds the provenance the role catalog import stamps onto
// every event it records — the catalog's fingerprint and, when the operator
// named one, where it came from — to a role event's own fields. A role changed
// by a person carries neither.
func withCatalogProvenance(fields ...PayloadField) []PayloadField {
	return append(fields,
		PayloadField{Name: "catalog_sha256", Kind: FieldString, MaxLen: 64},
		PayloadField{Name: "catalog_source", Kind: FieldString, MaxLen: 1024},
	)
}
func strs(name string) PayloadField {
	return PayloadField{Name: name, Kind: FieldStringArray}
}
func uid(name string) PayloadField     { return PayloadField{Name: name, Kind: FieldUUID} }
func boolean(name string) PayloadField { return PayloadField{Name: name, Kind: FieldBool} }
func enum(name string, values ...string) PayloadField {
	return PayloadField{Name: name, Kind: FieldEnum, Enum: values}
}
func pii(f PayloadField) PayloadField { f.PII = true; return f }

// userJoined marks a definition as recording a person joining the tenant for
// the first time — the fact a "new users" figure counts. A membership being
// provisioned is not it: saas.auth.sso_jit_provisioned fires again whenever a
// locally-removed member is re-provisioned from a still-valid IdP assertion,
// so marking it would count one person once per removal.
func userJoined(d AuditEventDefinition) AuditEventDefinition {
	d.MarksUserJoined = true
	return d
}

// isStringKind reports whether a kind's values are carried as JSON strings, and
// so whether the nonempty and length rules can apply to it at all.
func isStringKind(k FieldKind) bool {
	return k == FieldString || k == FieldUUID || k == FieldEnum
}

// required returns a copy of the named fields with Required set, so a definition
// can tighten a shared field group without restating it or mutating the group
// every other event shares. documentReadFields does the same thing by hand for
// one field; this is that, named.
func required(fields []PayloadField, names ...string) []PayloadField {
	want := make(map[string]bool, len(names))
	for _, name := range names {
		want[name] = true
	}
	out := append([]PayloadField(nil), fields...)
	found := 0
	for i := range out {
		if want[out[i].Name] {
			out[i].Required = true
			found++
		}
	}
	if found != len(want) {
		panic(fmt.Sprintf("audit registry: required() named %v, but only %d of them exist in the field group", names, found))
	}
	return out
}

// Registered event types. The constants are the typed vocabulary producers use;
// grouping mirrors the categories.
const (
	EventUserRegistered  EventType = "saas.user.registered"
	EventUserCreated     EventType = "saas.user.created"
	EventUserUpdated     EventType = "saas.user.updated"
	EventUserDeleted     EventType = "saas.user.deleted"
	EventUserSuspended   EventType = "saas.user.suspended"
	EventUserUnsuspended EventType = "saas.user.unsuspended"
	EventUserIdentityAdd EventType = "saas.user.identity_added"
	EventSettingsUpdated EventType = "saas.settings.updated"
	EventConsentTerms    EventType = "saas.consent.terms_accepted"
	EventConsentPrefs    EventType = "saas.consent.preferences_updated"

	EventAPIKeyCreated              EventType = "saas.api_key.created"
	EventModuleRegistrationMint     EventType = "saas.module.registration_minted"
	EventModuleWorkContextMint      EventType = "saas.module.work_context_minted"
	EventModuleOperationContextMint EventType = "saas.module.operation_context_minted"
	EventExecutableArtifactDecision EventType = "saas.module.executable_artifact_decision"
	EventDelegatedAudienceExchange  EventType = "saas.module.delegated_audience_exchange"
	// A composed module declared audit event types of its own (DeclareAuditEventTypes).
	EventModuleAuditTypesDeclared EventType = "saas.module.audit_types_declared"
	// A composed module notified a tenant's administrators (NotifyOrgAdmins).
	EventModuleOrgAdminsNotified     EventType = "saas.module.org_admins_notified"
	EventSolutionRegistrationMint    EventType = "saas.solution.registration_minted"
	EventSolutionRegistrationUpdated EventType = "saas.solution.registration_updated"
	EventSolutionRegistrationDeleted EventType = "saas.solution.registration_deleted"
	EventAPIKeyRevoked               EventType = "saas.api_key.revoked"
	EventRoleCreated                 EventType = "saas.role.created"
	EventRoleUpdated                 EventType = "saas.role.updated"
	EventRoleDeleted                 EventType = "saas.role.deleted"
	EventRoleAssigned                EventType = "saas.role.assigned"
	EventRoleRevoked                 EventType = "saas.role.revoked"
	EventSessionRevoked              EventType = "saas.session.revoked"
	EventInvitationCreated           EventType = "saas.invitation.created"
	EventInvitationAccepted          EventType = "saas.invitation.accepted"
	EventInvitationRevoked           EventType = "saas.invitation.revoked"
	EventInvitationResent            EventType = "saas.invitation.resent"
	EventInvitationLinkIssued        EventType = "saas.invitation.link_issued"
	EventDelegationRequested         EventType = "saas.delegation.requested"
	EventDelegationApproved          EventType = "saas.delegation.approved"
	EventDelegationDenied            EventType = "saas.delegation.denied"
	EventDelegationAutoApproved      EventType = "saas.delegation.auto_approved"
	EventApprovalAsked               EventType = "saas.approval.asked"
	EventApprovalApproved            EventType = "saas.approval.approved"
	EventApprovalDenied              EventType = "saas.approval.denied"
	EventApprovalTimeout             EventType = "saas.approval.timeout"
	EventApprovalEscalated           EventType = "saas.approval.escalated"
	EventApprovalCancelled           EventType = "saas.approval.cancelled"
	EventApprovalDecisionRecorded    EventType = "saas.approval.decision_recorded"
	EventPrincipalCreated            EventType = "saas.principal.created"
	EventPrincipalRevoked            EventType = "saas.principal.revoked"
	EventPrincipalDisabled           EventType = "saas.principal.disabled"
	EventPrincipalEnabled            EventType = "saas.principal.enabled"

	EventScopeNodeRegistered EventType = "saas.scope.node_registered"
	EventScopeGranted        EventType = "saas.scope.granted"
	EventScopeRevoked        EventType = "saas.scope.revoked"
	EventRecordShared        EventType = "saas.record.shared"
	EventRecordShareRevoked  EventType = "saas.record.share_revoked"

	EventInstallationCreated              EventType = "saas.installation.created"
	EventInstallationRevoked              EventType = "saas.installation.revoked"
	EventInstallationOwnershipTransferred EventType = "saas.installation.ownership_transferred"

	EventWorkContextTaskStarted  EventType = "saas.work_context.task_started"
	EventWorkContextRootSession  EventType = "saas.work_context.root_session_started"
	EventWorkContextChildSession EventType = "saas.work_context.child_session_started"
	EventWorkContextAudienceExch EventType = "saas.work_context.audience_exchanged"
	EventWorkContextRenewed      EventType = "saas.work_context.renewed"

	EventAuthLogin                  EventType = "saas.auth.login"
	EventAuthMagicLinkLogin         EventType = "saas.auth.magic_link_login"
	EventAuthSSOJitProvisioned      EventType = "saas.auth.sso_jit_provisioned"
	EventAuthOrgSwitched            EventType = "saas.auth.organization_switched"
	EventAuthMFAChallengeStart      EventType = "saas.auth.mfa_challenge_started"
	EventAuthMFAChallengeDone       EventType = "saas.auth.mfa_challenge_completed"
	EventAuthClientAuthorized       EventType = "saas.auth.client_authorized"
	EventMFATOTPSetupStarted        EventType = "saas.mfa.totp_setup_started"
	EventMFATOTPVerified            EventType = "saas.mfa.totp_verified"
	EventMFAWebAuthnRegStarted      EventType = "saas.mfa.webauthn_registration_started"
	EventMFAWebAuthnRegistered      EventType = "saas.mfa.webauthn_registered"
	EventMFAWebAuthnUsed            EventType = "saas.mfa.webauthn_used"
	EventMFABackupGenerated         EventType = "saas.mfa.backup_codes_generated"
	EventMFABackupUsed              EventType = "saas.mfa.backup_code_used"
	EventMFADeviceRevoked           EventType = "saas.mfa.device_revoked"
	EventPlatformRoleGranted        EventType = "saas.platform.role_granted"
	EventPlatformRoleRevoked        EventType = "saas.platform.role_revoked"
	EventPlatformImpersonated       EventType = "saas.platform.user_impersonated"
	EventPlatformImpersonationEnded EventType = "saas.platform.user_impersonation_ended"

	EventBillingCheckoutStarted EventType = "saas.billing.checkout_started"
	EventBillingPortalOpened    EventType = "saas.billing.portal_opened"
	EventBillingFreePlan        EventType = "saas.billing.free_plan_selected"
	EventEntitlementOverride    EventType = "saas.entitlement.override"

	EventOrgCreated                EventType = "saas.org.created"
	EventOrgMemberAdded            EventType = "saas.org.member_added"
	EventOrgMemberRemoved          EventType = "saas.org.member_removed"
	EventOrgMemberLeft             EventType = "saas.org.member_left"
	EventOrgUpdated                EventType = "saas.org.updated"
	EventOrgDeleted                EventType = "saas.org.deleted"
	EventOrgSettingsUpdated        EventType = "saas.org.settings_updated"
	EventOrgGenericSettingsUpdated EventType = "saas.org.generic_settings_updated"
	EventTeamCreated               EventType = "saas.team.created"
	EventTeamUpdated               EventType = "saas.team.updated"
	EventTeamDeleted               EventType = "saas.team.deleted"
	EventTeamMemberAdded           EventType = "saas.team.member_added"
	EventTeamMemberRemoved         EventType = "saas.team.member_removed"
	EventSSOSetupStarted           EventType = "saas.sso.setup.started"
	EventSSODisabled               EventType = "saas.sso.disabled"
	EventOnboardingStepDone        EventType = "saas.onboarding.step_completed"
	EventOnboardingStepSkip        EventType = "saas.onboarding.step_skipped"
	EventActivationAchieved        EventType = "saas.activation.achieved"

	EventWaitlistJoined    EventType = "saas.waitlist.joined"
	EventWaitlistPending   EventType = "saas.waitlist.pending"
	EventWaitlistVerified  EventType = "saas.waitlist.verified"
	EventWaitlistReviewed  EventType = "saas.waitlist.reviewed"
	EventWaitlistApproved  EventType = "saas.waitlist.approved"
	EventWaitlistInvited   EventType = "saas.waitlist.invited"
	EventWaitlistConverted EventType = "saas.waitlist.converted"
	EventWaitlistRejected  EventType = "saas.waitlist.rejected"
	EventGDPRExportReq     EventType = "saas.gdpr.export_requested"
	EventGDPRDeletionReq   EventType = "saas.gdpr.deletion_requested"
	EventGDPRDeletionDone  EventType = "saas.gdpr.deletion_completed"

	EventWebhookCreated       EventType = "saas.webhook.created"
	EventWebhookDeleted       EventType = "saas.webhook.deleted"
	EventWebhookReplayed      EventType = "saas.webhook.replayed"
	EventWebhookSecretRotated EventType = "saas.webhook.secret_rotated"
	EventJobReplayed          EventType = "saas.job.replayed"

	EventDatasourceSourceAdded          EventType = "saas.datasource.source.added"
	EventDatasourceSyncCompleted        EventType = "saas.datasource.sync.completed"
	EventDatasourceCredentialUpdated    EventType = "saas.datasource.credential.updated"
	EventDatasourceSyncFailed           EventType = "saas.datasource.sync.failed"
	EventDatasourceSourceSynced         EventType = "saas.datasource.source.synced"
	EventDatasourceSourceRemoved        EventType = "saas.datasource.source.removed"
	EventDatasourceChangeSetCompiled    EventType = "saas.datasource.change_set_compiled"
	EventDatasourceForcePushReconciled  EventType = "saas.datasource.force_push_reconciled"
	EventDatasourceBranchDeleted        EventType = "saas.datasource.branch_deleted"
	EventDatasourceSnapshotTooLarge     EventType = "saas.datasource.snapshot_too_large"
	EventDatasourceSourceRecovered      EventType = "saas.datasource.source.recovered"
	EventDatasourceSourceAccessLost     EventType = "saas.datasource.source.access_lost"
	EventDatasourceSourceAccessRestored EventType = "saas.datasource.source.access_restored"
	EventDatasourceBlobFetched          EventType = "saas.datasource.blob_fetched"
	EventDatasourceFilesFetched         EventType = "saas.datasource.files_fetched"
	EventDatasourceAccountLinkStarted   EventType = "saas.datasource.account_link_started"
	EventDatasourceAccountLinked        EventType = "saas.datasource.account_linked"
	EventDatasourceAccountUnlinked      EventType = "saas.datasource.account_unlinked"
	EventDatasourceGroupBound           EventType = "saas.datasource.group_bound"
	EventDatasourceGroupUnbound         EventType = "saas.datasource.group_unbound"
	EventDatasourceDomainClaimed        EventType = "saas.datasource.domain_claimed"
	EventDatasourceDomainVerified       EventType = "saas.datasource.domain_verified"
	EventDatasourceDomainRemoved        EventType = "saas.datasource.domain_removed"

	EventDatasourceGitHubAppSetupStarted   EventType = "saas.datasource.github_app.setup_started"
	EventDatasourceGitHubAppSetupCompleted EventType = "saas.datasource.github_app.setup_completed"
	// A person's connect-time delegation of a source's sync to one module
	// binding (source_delegation.go): recorded, used for a mint, and revoked.
	EventSourceDelegationCreated EventType = "saas.datasource.delegation.created"
	EventSourceDelegationUsed    EventType = "saas.datasource.delegation.used"
	EventSourceDelegationRevoked EventType = "saas.datasource.delegation.revoked"
	EventFeatureFlagUpdated      EventType = "saas.feature_flag.updated"

	// Domain-event pub/sub (issue #493). A subscription is a standing grant of
	// delivery, so its create and revoke are audited on the tenant spine; a
	// replay is an operator action that re-delivers history. Per-publish is not
	// audited — the domain_events relation is itself the record of every publish.
	EventEventSubscriptionCreated EventType = "saas.event.subscription_created"
	EventEventSubscriptionRevoked EventType = "saas.event.subscription_revoked"
	EventEventReplayed            EventType = "saas.event.replayed"

	EventDashboardCreated EventType = "saas.dashboard.created"
	EventDashboardUpdated EventType = "saas.dashboard.updated"
	EventDashboardDeleted EventType = "saas.dashboard.deleted"
	EventDashboardShared  EventType = "saas.dashboard.shared"

	// Document lifecycle vocabulary emitted by a consuming solution through the
	// module-facing EmitAuditEvent (issue #463). Every event carries the tenant
	// (org_id), actor (actor_id), and entry (resource_id) columns plus a solution
	// scope and the entry version in its payload, so a solution keeps one audit
	// spine per tenant instead of a second trail.
	EventDocumentIngested           EventType = "saas.document.ingested"
	EventDocumentRead               EventType = "saas.document.read"
	EventDocumentSearch             EventType = "saas.document.search"
	EventDocumentVersionMinted      EventType = "saas.document.version_minted"
	EventDocumentRenamed            EventType = "saas.document.renamed"
	EventDocumentDeleted            EventType = "saas.document.deleted"
	EventDocumentArchived           EventType = "saas.document.archived"
	EventDocumentUnarchived         EventType = "saas.document.unarchived"
	EventDocumentQuarantined        EventType = "saas.document.quarantined"
	EventDocumentQuarantineReleased EventType = "saas.document.quarantine_released"
	EventDocumentSubscribed         EventType = "saas.document.subscribed"
	EventDocumentUnsubscribed       EventType = "saas.document.unsubscribed"
	// Entry-level facts of the documents store's own mutations, each on the
	// entry (resource_id) like the lifecycle vocabulary above. A governance
	// action — a subscription, a quarantine release, a transfer, a freeze or an
	// unfreeze — is audited on success and on refusal alike under one type, told
	// apart by the required `outcome`: a refusal is never its own type, so the
	// `payload:outcome` aggregation counts every refused action. A stale ingest
	// op is not a refusal but the store's ordering guard skipping an op already
	// superseded; it wrote nothing.
	EventDocumentOwnershipTransferred EventType = "saas.document.ownership_transferred"
	EventDocumentFrozen               EventType = "saas.document.frozen"
	EventDocumentUnfrozen             EventType = "saas.document.unfrozen"
	EventDocumentIngestSkippedStale   EventType = "saas.document.ingest_skipped_stale"
	EventDocumentPayloadConflict      EventType = "saas.document.payload_conflict"
	// Receipts of the documents store's atomic effects. resource_id is the
	// effect key (the artifact id for a production), not an entry: each effect
	// commits many entries at once, and every entry it changes is also audited
	// on its own under the lifecycle vocabulary above, in the same transaction.
	EventDocumentSnapshotCommitted    EventType = "saas.document.snapshot.committed"
	EventDocumentSnapshotSkippedStale EventType = "saas.document.snapshot.skipped_stale"
	EventDocumentEffectCommitted      EventType = "saas.document.effect.committed"
	EventDocumentProductionCommitted  EventType = "saas.document.production.committed"
	EventDocumentKnowledgePublished   EventType = "saas.document.knowledge.published"

	// An operator re-queued a document's dead-lettered derivation — the
	// producer run a document module gave up on — once the cause was fixed. It
	// is who-did-what-to-which-resource (the entry is re-derived on an operator's
	// say-so), not pipeline bookkeeping, so it has a type of its own.
	EventDocumentDeadLetterRedriven EventType = "saas.document.dead_letter_redriven"
)

var auditEventCatalog = []AuditEventDefinition{
	securityRetained(userJoined(mutation(EventUserRegistered, CategoryIdentity, "A new user account was registered.",
		enum("signup_method", "password", "sso", "magic_link"), pii(str("email"))))),
	securityRetained(userJoined(mutation(EventUserCreated, CategoryIdentity, "A user was provisioned by an administrator.", pii(str("email"))))),
	contentRetained(mutation(EventUserUpdated, CategoryIdentity, "A user profile was updated.")),
	securityRetained(mutation(EventUserDeleted, CategoryIdentity, "A user account was deleted.")),
	// A suspension is allowed to leave an organization with no administrator —
	// containing a compromised account outranks that — so the organizations it
	// did leave that way are part of the record rather than a reason to refuse.
	securityRetained(mutation(EventUserSuspended, CategoryIdentity, "A user account was suspended.",
		strs("organizations_without_administrator"))),
	securityRetained(mutation(EventUserUnsuspended, CategoryIdentity, "A user account was reinstated.")),
	securityRetained(mutation(EventUserIdentityAdd, CategoryIdentity, "An external identity was linked to a user.", str("provider"))),
	contentRetained(observation(EventSettingsUpdated, CategoryIdentity, "A user's personal settings changed.")),
	contentRetained(mutation(EventConsentTerms, CategoryIdentity, "A user accepted the terms of service.", str("version"))),
	contentRetained(mutation(EventConsentPrefs, CategoryIdentity, "A user updated their consent preferences.")),

	securityRetained(mutation(EventAPIKeyCreated, CategoryAccess, "An API key was minted.", uid("key_id"), PayloadField{Name: "scopes", Kind: FieldStringArray})),
	securityRetained(mutation(EventModuleRegistrationMint, CategoryAccess, "A composed module was issued a gateway registration credential.", str("prefix"))),
	contentRetained(observation(EventModuleOrgAdminsNotified, CategorySystem, "A composed module notified a tenant's administrators; the host resolved the recipients.",
		PayloadField{Name: "prefix", Kind: FieldString, Required: true}, str("category"), str("type"),
		PayloadField{Name: "recipients", Kind: FieldInt, Required: true}, PayloadField{Name: "delivered", Kind: FieldInt, Required: true},
		str("idempotency_key"))),
	securityRetained(mutation(EventModuleAuditTypesDeclared, CategorySystem, "A composed module declared audit event types of its own, or took a namespace over from the producer the operator unbound.",
		PayloadField{Name: "prefix", Kind: FieldString, Required: true}, strs("event_types"), strs("namespaces_taken_over"))),
	securityRetained(mutation(EventModuleWorkContextMint, CategoryAccess, "A composed module was issued a Work Context for its service principal.", str("prefix"), str("tenant"))),
	securityRetained(mutation(EventModuleOperationContextMint, CategoryAccess, "A composed module was issued, with no person present, a Work Context for one of its installed operation audiences.",
		str("prefix"), str("tenant"), str("binding_id"), str("audience"), strs("scopes"))),
	// v2 adds `delegation_id` and stops requiring `owner_principal_id`.
	//
	// The exchange now has two arms. One presents a live parent capability, and
	// a verified parent always names the person it acts for — that arm still
	// always writes `owner_principal_id`, and a test holds it to that. The
	// other presents only a reference to a revocable grant, and a refusal there
	// can happen before any person has been identified: an id that names no
	// delegation, or one belonging to another module, is refused
	// indistinguishably and truthfully identifies nobody.
	//
	// Requiring the field would have left exactly those refusals unrecorded,
	// which is the one kind of refusal an authority surface most needs to keep.
	// `delegation_id` is what identifies them instead, so a probe against a
	// grant reference is legible even when no owner was ever resolved.
	securityRetained(revised(observation(EventDelegatedAudienceExchange, CategoryAccess, "A composed module's installed delegated-audience exchange was issued or refused.",
		PayloadField{Name: "owner_principal_id", Kind: FieldUUID},
		uid("delegation_id"),
		PayloadField{Name: "actor_principal_id", Kind: FieldUUID, Required: true},
		PayloadField{Name: "module_principal_id", Kind: FieldString, Required: true},
		PayloadField{Name: "binding_kind", Kind: FieldEnum, Required: true, Enum: []string{"read", "operation"}},
		PayloadField{Name: "binding_id", Kind: FieldString, Required: true},
		str("audience"),
		PayloadField{Name: "lookup", Kind: FieldBool, Required: true},
		PayloadField{Name: "outcome", Kind: FieldEnum, Required: true, Enum: []string{DelegatedAudienceExchangeIssued, DelegatedAudienceExchangeRefused}},
		PayloadField{Name: "refusal_code", Kind: FieldEnum, Enum: []string{"InvalidArgument", "Unauthenticated", "PermissionDenied", "FailedPrecondition", "Unavailable", "Internal"}}), 2)),
	securityRetained(mutation(EventExecutableArtifactDecision, CategoryAccess, "An administrator approved or revoked an exact executable artifact identity.", str("action"), uid("installation_id"), str("policy_id"), str("subject_digest"), str("module_principal_id"))),
	securityRetained(mutation(EventSolutionRegistrationMint, CategoryAccess, "A solution was issued a gateway and frontend registration credential.", str("solution_id"))),
	securityRetained(mutation(EventSolutionRegistrationUpdated, CategoryAccess, "A solution registered or replaced one half of its runtime registration.", str("solution_id"), str("publisher"), str("half"), PayloadField{Name: "revision", Kind: FieldInt}, strs("audit_namespaces_taken_over"))),
	securityRetained(mutation(EventSolutionRegistrationDeleted, CategoryAccess, "A solution registration was removed and tombstoned.", str("solution_id"), str("publisher"), PayloadField{Name: "revision", Kind: FieldInt})),
	securityRetained(mutation(EventAPIKeyRevoked, CategoryAccess, "An API key was revoked.", uid("key_id"))),
	securityRetained(mutation(EventRoleCreated, CategoryAccess, "A role was created.",
		withCatalogProvenance(str("name"), catalogCount("permissions_added"))...)),
	securityRetained(mutation(EventRoleUpdated, CategoryAccess, "A role's description and permission set were replaced.",
		withCatalogProvenance(str("name"), strs("permissions"), catalogCount("permissions_added"), catalogCount("permissions_removed"))...)),
	securityRetained(mutation(EventRoleDeleted, CategoryAccess, "A role was deleted.",
		withCatalogProvenance(str("name"), catalogCount("assignments_removed"))...)),
	securityRetained(mutation(EventRoleAssigned, CategoryAccess, "A role was assigned to a principal.", uid("role_id"), uid("subject_id"))),
	securityRetained(mutation(EventRoleRevoked, CategoryAccess, "A role assignment was revoked.", uid("role_id"))),
	securityRetained(mutation(EventSessionRevoked, CategoryAccess, "A session was revoked.")),
	securityRetained(mutation(EventInvitationCreated, CategoryAccess, "An organization invitation was created.", pii(str("email")))),
	securityRetained(mutation(EventInvitationAccepted, CategoryAccess, "An organization invitation was accepted.")),
	securityRetained(mutation(EventInvitationRevoked, CategoryAccess, "An organization invitation was revoked.")),
	securityRetained(mutation(EventInvitationResent, CategoryAccess, "An organization invitation was resent.")),
	securityRetained(mutation(EventInvitationLinkIssued, CategoryAccess, "An organization invitation's accept link was issued to an administrator instead of emailed.")),
	securityRetained(mutation(EventDelegationRequested, CategoryAccess, "A delegation grant was requested.")),
	securityRetained(mutation(EventDelegationApproved, CategoryAccess, "A delegation grant was approved.")),
	securityRetained(mutation(EventDelegationDenied, CategoryAccess, "A delegation grant was denied.")),
	securityRetained(mutation(EventDelegationAutoApproved, CategoryAccess, "A delegation grant was auto-approved by policy.")),
	securityRetained(mutation(EventApprovalAsked, CategoryAccess, "An approval request was opened for a gated action.", str("resource"), str("action"))),
	securityRetained(mutation(EventApprovalApproved, CategoryAccess, "An approval request reached quorum and was approved.", str("resource"), str("action"))),
	securityRetained(mutation(EventApprovalDenied, CategoryAccess, "An approval request was denied.")),
	securityRetained(mutation(EventApprovalTimeout, CategoryAccess, "An approval request expired before reaching quorum.")),
	securityRetained(mutation(EventApprovalEscalated, CategoryAccess, "An approval request was escalated to a wider approver set.")),
	securityRetained(mutation(EventApprovalCancelled, CategoryAccess, "An approval request was cancelled before a decision.", str("reason"))),
	securityRetained(mutation(EventApprovalDecisionRecorded, CategoryAccess, "An approver recorded a decision on an approval request.", str("decision"))),
	securityRetained(mutation(EventPrincipalCreated, CategoryAccess, "An agent principal was created.", str("agent_identifier"))),
	securityRetained(mutation(EventPrincipalRevoked, CategoryAccess, "A principal was revoked.", str("reason"))),
	securityRetained(mutation(EventPrincipalDisabled, CategoryAccess, "An agent principal was disabled.", str("reason"))),
	securityRetained(mutation(EventPrincipalEnabled, CategoryAccess, "An agent principal was re-enabled.")),
	contentRetained(mutation(EventScopeNodeRegistered, CategoryAccess, "A scope node was registered.", str("scope_path"), str("kind"))),
	securityRetained(mutation(EventScopeGranted, CategoryAccess, "A role was granted at a scope node.", uid("role_id"), uid("subject_id"), str("scope_path"))),
	securityRetained(mutation(EventScopeRevoked, CategoryAccess, "A scope grant was revoked.", uid("role_id"), str("scope_path"))),
	securityRetained(mutation(EventInstallationCreated, CategoryAccess, "A solution was installed: an agent principal, solution scope node, standing grant, and installation row were composed.",
		uid("agent_principal_id"), str("solution_identifier"), uid("role_id"), uid("owner_principal_id"))),
	securityRetained(mutation(EventInstallationRevoked, CategoryAccess, "A solution was uninstalled: its agent principal and standing grant were revoked and its scope node soft-deleted.",
		str("solution_identifier"))),
	securityRetained(mutation(EventInstallationOwnershipTransferred, CategoryAccess, "An installation's owner of record was reassigned.",
		uid("owner_principal_id"))),
	securityRetained(mutation(EventRecordShared, CategoryAccess, "A record was shared with a principal or team.", uid("role_id"), uid("subject_id"))),
	securityRetained(mutation(EventRecordShareRevoked, CategoryAccess, "A record share was revoked.", uid("role_id"), uid("subject_id"))),
	securityRetained(mutation(EventWorkContextTaskStarted, CategoryAccess, "A signed Work Context was issued for a new agent task and root session.")),
	securityRetained(mutation(EventWorkContextRootSession, CategoryAccess, "A new root agent session was started under an existing task.")),
	securityRetained(mutation(EventWorkContextChildSession, CategoryAccess, "An attenuated child agent session was started.")),
	securityRetained(mutation(EventWorkContextAudienceExch, CategoryAccess, "A Work Context task and session lineage was reissued for another audience.")),
	securityRetained(mutation(EventWorkContextRenewed, CategoryAccess, "A delegated actor renewed its Work Context past the signing TTL cap.")),

	securityRetained(observation(EventAuthLogin, CategorySecurity, "A user authenticated.", str("method"), str("client_id"))),
	securityRetained(observation(EventAuthMagicLinkLogin, CategorySecurity, "A user authenticated via magic link.")),
	securityRetained(mutation(EventAuthSSOJitProvisioned, CategorySecurity, "A user was just-in-time provisioned via SSO.", str("provider"))),
	securityRetained(observation(EventAuthOrgSwitched, CategorySecurity, "A user switched active organization.")),
	securityRetained(observation(EventAuthMFAChallengeStart, CategorySecurity, "An MFA challenge was started.")),
	securityRetained(observation(EventAuthMFAChallengeDone, CategorySecurity, "An MFA challenge was completed.", enum("factor", "totp", "webauthn", "backup_code"))),
	securityRetained(observation(EventAuthClientAuthorized, CategorySecurity, "A person authorized a registered client to act for them.", str("client_id"), str("resource"), str("client_registration"))),
	securityRetained(mutation(EventMFATOTPSetupStarted, CategorySecurity, "TOTP enrollment was started.")),
	securityRetained(mutation(EventMFATOTPVerified, CategorySecurity, "A TOTP device was verified.")),
	securityRetained(mutation(EventMFAWebAuthnRegStarted, CategorySecurity, "WebAuthn registration was started.")),
	securityRetained(mutation(EventMFAWebAuthnRegistered, CategorySecurity, "A WebAuthn credential was registered.")),
	securityRetained(observation(EventMFAWebAuthnUsed, CategorySecurity, "A WebAuthn credential was used to authenticate.")),
	securityRetained(mutation(EventMFABackupGenerated, CategorySecurity, "MFA backup codes were generated.")),
	securityRetained(observation(EventMFABackupUsed, CategorySecurity, "An MFA backup code was consumed.")),
	securityRetained(mutation(EventMFADeviceRevoked, CategorySecurity, "An MFA device was revoked.")),
	securityRetained(mutation(EventPlatformRoleGranted, CategorySecurity, "A platform role was granted.")),
	securityRetained(mutation(EventPlatformRoleRevoked, CategorySecurity, "A platform role was revoked.")),
	// The operator's justification is part of the record, not an optional
	// enrichment: who and whom are already implied by the actor/resource pair,
	// and why is the only thing this event can carry that the pair cannot.
	// session_id pairs this record with the user_impersonation_ended that closes
	// the same window, so the two reconcile to each other rather than by
	// timestamp proximity.
	securityRetained(revised(mutation(EventPlatformImpersonated, CategorySecurity, "A platform admin impersonated a user.",
		PayloadField{Name: "reason", Kind: FieldString, Required: true}, uid("session_id")), 3)),
	// access_token_revoked records whether the window's access token was actually
	// killed. Without a revocation store wired there is no mechanism to kill one
	// early, and a close record that did not say so would overstate what the stop
	// achieved.
	securityRetained(mutation(EventPlatformImpersonationEnded, CategorySecurity, "A platform admin's impersonation session ended.",
		uid("session_id"), PayloadField{Name: "duration_seconds", Kind: FieldInt},
		PayloadField{Name: "access_token_revoked", Kind: FieldBool, Required: true})),

	contentRetained(observation(EventBillingCheckoutStarted, CategoryBilling, "A billing checkout session was started.")),
	contentRetained(observation(EventBillingPortalOpened, CategoryBilling, "The billing portal was opened.")),
	contentRetained(observation(EventBillingFreePlan, CategoryBilling, "The free plan was selected.")),
	securityRetained(mutation(EventEntitlementOverride, CategoryBilling, "An entitlement override was set.", str("key"))),

	securityRetained(mutation(EventOrgCreated, CategoryOrganization, "An organization was created.", str("name"))),
	securityRetained(mutation(EventOrgMemberAdded, CategoryOrganization, "A member was added to an organization.")),
	securityRetained(mutation(EventOrgMemberRemoved, CategoryOrganization, "A member was removed from an organization.")),
	securityRetained(mutation(EventOrgMemberLeft, CategoryOrganization, "A member left an organization.")),
	securityRetained(mutation(EventOrgUpdated, CategoryOrganization, "An organization was renamed or its slug changed.", str("name"), str("slug"))),
	securityRetained(mutation(EventOrgDeleted, CategoryOrganization, "An organization was deleted: archived, its members removed and its credentials revoked.", str("slug"))),
	securityRetained(mutation(EventOrgSettingsUpdated, CategoryOrganization, "Organization branding settings were updated.")),
	securityRetained(mutation(EventOrgGenericSettingsUpdated, CategoryOrganization, "Organization generic (typed) settings were updated.")),
	securityRetained(mutation(EventTeamCreated, CategoryOrganization, "A team was created.", str("name"))),
	securityRetained(mutation(EventTeamUpdated, CategoryOrganization, "A team was updated.")),
	securityRetained(mutation(EventTeamDeleted, CategoryOrganization, "A team was deleted.")),
	securityRetained(mutation(EventTeamMemberAdded, CategoryOrganization, "A member was added to a team.")),
	securityRetained(mutation(EventTeamMemberRemoved, CategoryOrganization, "A member was removed from a team.")),
	securityRetained(mutation(EventSSOSetupStarted, CategoryOrganization, "SSO configuration was started.")),
	securityRetained(mutation(EventSSODisabled, CategoryOrganization, "SSO was disabled for an organization.")),
	contentRetained(observation(EventOnboardingStepDone, CategoryOrganization, "An onboarding step was completed.", str("step"))),
	contentRetained(observation(EventOnboardingStepSkip, CategoryOrganization, "An onboarding step was skipped.", str("step"))),
	contentRetained(observation(EventActivationAchieved, CategoryOrganization, "An organization reached activation.")),

	contentRetained(observation(EventDashboardCreated, CategoryOrganization, "A dashboard was created.")),
	contentRetained(observation(EventDashboardUpdated, CategoryOrganization, "A dashboard was updated.")),
	contentRetained(observation(EventDashboardDeleted, CategoryOrganization, "A dashboard was deleted.")),
	securityRetained(observation(EventDashboardShared, CategoryOrganization, "A dashboard's visibility was changed.")),

	contentRetained(observation(EventWaitlistJoined, CategoryLifecycle, "A prospect joined the waitlist.", pii(str("email")))),
	contentRetained(observation(EventWaitlistPending, CategoryLifecycle, "A waitlist entry moved to pending.")),
	contentRetained(observation(EventWaitlistVerified, CategoryLifecycle, "A waitlist entry was verified.")),
	securityRetained(observation(EventWaitlistReviewed, CategoryLifecycle, "A waitlist entry was reviewed by an administrator.")),
	securityRetained(observation(EventWaitlistApproved, CategoryLifecycle, "A waitlist entry was approved.")),
	securityRetained(observation(EventWaitlistInvited, CategoryLifecycle, "A waitlist entry was invited.")),
	contentRetained(observation(EventWaitlistConverted, CategoryLifecycle, "A waitlist entry converted to a user.")),
	securityRetained(observation(EventWaitlistRejected, CategoryLifecycle, "A waitlist entry was rejected.")),
	securityRetained(mutation(EventGDPRExportReq, CategoryLifecycle, "A GDPR data export was requested.")),
	securityRetained(mutation(EventGDPRDeletionReq, CategoryLifecycle, "A GDPR deletion was requested.")),
	securityRetained(mutation(EventGDPRDeletionDone, CategoryLifecycle, "A GDPR deletion completed.")),

	securityRetained(revised(mutation(EventWebhookCreated, CategorySystem, "A webhook subscription was created.", webhookAdminFields...), webhookAdminVersion)),
	securityRetained(revised(mutation(EventWebhookDeleted, CategorySystem, "A webhook subscription was deleted.", webhookAdminFields...), webhookAdminVersion)),
	securityRetained(revised(mutation(EventWebhookReplayed, CategorySystem, "A webhook delivery was replayed.", webhookAdminFields...), webhookAdminVersion)),
	// v2 records how a GitHub source authenticates — including `public`, a
	// source connected with no credential at all — and declares the `provider`
	// the provider-agnostic connect has always written, which v1 dropped.
	securityRetained(revised(mutation(EventDatasourceSourceAdded, CategorySystem, "A datasource was connected.",
		str("repo"), str("provider"), enum("credential_kind", "pat", "app", "public")), 2)),
	securityRetained(mutation(EventDatasourceGitHubAppSetupStarted, CategorySystem, "GitHub App setup was started for an organization.")),
	contentRetained(observation(EventDatasourceAccountLinkStarted, CategorySystem, "A person started linking a provider account.", str("connector"))),
	securityRetained(mutation(EventDatasourceAccountLinked, CategorySystem, "A person linked a provider account they signed in as.",
		str("connector"), str("provider_account_id"))),
	securityRetained(mutation(EventDatasourceAccountUnlinked, CategorySystem, "A linked provider account was removed.",
		str("connector"), str("provider_account_id"), str("user_id"))),
	securityRetained(mutation(EventDatasourceGroupBound, CategorySystem, "An administrator bound a provider group to a team.",
		str("connector"), str("provider_group_id"), str("team_id"))),
	securityRetained(mutation(EventDatasourceGroupUnbound, CategorySystem, "A provider group binding was removed.",
		str("connector"), str("provider_group_id"), str("team_id"))),
	securityRetained(mutation(EventDatasourceDomainClaimed, CategorySystem, "An administrator claimed a domain for the organization.", str("domain"))),
	securityRetained(mutation(EventDatasourceDomainVerified, CategorySystem, "A claimed domain was verified by its DNS TXT record.", str("domain"))),
	securityRetained(mutation(EventDatasourceDomainRemoved, CategorySystem, "A claimed domain was removed.", str("domain"))),
	securityRetained(mutation(EventDatasourceGitHubAppSetupCompleted, CategorySystem, "A GitHub App installation was verified and bound to an organization.",
		str("installation_id"))),
	securityRetained(mutation(EventSourceDelegationCreated, CategoryAccess, "A person connecting a datasource delegated its sync to a module's installed operation binding.",
		sourceDelegationFields...)),
	// v2 records `lookup`: a delegation now mints for one call at a time, and a
	// capability narrowed to recovering a receipt carries strictly less than
	// one that may produce the effect. A trail that cannot tell the two apart
	// cannot answer what a module was actually let do.
	securityRetained(revised(mutation(EventSourceDelegationUsed, CategoryAccess, "A module was issued an operation context from a person's source delegation.",
		append(slices.Clone(sourceDelegationFields), str("audience"), strs("scopes"), boolean("lookup"))...), 2)),
	securityRetained(mutation(EventSourceDelegationRevoked, CategoryAccess, "A source delegation was revoked.",
		append(slices.Clone(sourceDelegationFields), enum("reason", SourceDelegationRevocationReasons...))...)),
	contentRetained(observation(EventDatasourceSourceSynced, CategorySystem, "A datasource sync was requested.", str("job_id"), str("repo"))),
	securityRetained(revised(mutation(EventDatasourceCredentialUpdated, CategorySystem, "A datasource credential was validated and replaced.",
		str("repo"), enum("credential_kind", "pat", "app", "public")), 2)),
	// v2 names what was removed — v1 recorded an empty payload, so the trail
	// could not say which repository or which collection lost its source.
	// `boundary` is the collection (boundary node) the source fed, spelled as
	// on the document events so one payload filter reads both — and so the
	// collection grant covers this event, which isCollectionBoundaryEvent reads
	// this declaration for.
	//
	// `provider` is an enum, not a free string: datasource_sources.provider is
	// NOT NULL under a CHECK restricting it to exactly these four values, so the
	// registry can check what the database already guarantees. A string field
	// would accept a provider that could never have been stored, and the
	// module-facing EmitAuditEvent — the one path where registry validation
	// rejects rather than warns — would pass it through.
	securityRetained(revised(mutation(EventDatasourceSourceRemoved, CategorySystem, "A datasource was removed.",
		enum("provider", DatasourceProviderGitHub, DatasourceProviderAPI, DatasourceProviderCrawler, DatasourceProviderUpload),
		str("repo"), str("boundary")), 2)),
	contentRetained(observation(EventDatasourceSyncCompleted, CategorySystem, "A datasource ingestion job completed.", sourceSyncFields...)),
	contentRetained(observation(EventDatasourceSyncFailed, CategorySystem, "A datasource ingestion attempt failed and may retry.", sourceSyncFields...)),
	contentRetained(observation(EventDatasourceChangeSetCompiled, CategorySystem, "A GitHub delivery was compiled into a change set.",
		str("base"), str("head"), PayloadField{Name: "ops", Kind: FieldInt}, enum("mode", "compare", "snapshot"), str("delivery_id"))),
	contentRetained(observation(EventDatasourceForcePushReconciled, CategorySystem, "A GitHub force push or divergence was reconciled with a snapshot.",
		str("head"), str("delivery_id"))),
	contentRetained(observation(EventDatasourceBranchDeleted, CategorySystem, "A GitHub branch-deletion delivery was acknowledged without removing documents.",
		str("ref"), str("delivery_id"))),
	contentRetained(observation(EventDatasourceSnapshotTooLarge, CategorySystem, "A datasource snapshot manifest exceeded the ingest payload limit; the source was degraded pending operator reset.",
		str("head"), PayloadField{Name: "bytes", Kind: FieldInt}, PayloadField{Name: "limit", Kind: FieldInt}, str("delivery_id"))),
	contentRetained(observation(EventDatasourceSourceRecovered, CategorySystem, "A degraded datasource source snapshotted within the ingest limit again and was returned to active.",
		str("head"), str("delivery_id"))),
	// v2 adds the third cause, public_repository_unreadable: a source connected
	// to a public repository that GitHub has stopped serving unauthenticated.
	// It has no installation, so installation_id is empty on those records —
	// which is also how a consumer tells the two families apart without reading
	// the reason.
	contentRetained(revised(observation(EventDatasourceSourceAccessLost, CategorySystem,
		"A datasource source lost access to its repository — a GitHub App installation stopped granting it, or a public repository stopped being readable without a credential; the source was degraded until access returns.",
		str("repo"), str("installation_id"), enum("reason",
			DatasourceAccessLostRepositoryUnavailable, DatasourceAccessLostSuspended,
			DatasourceAccessLostPublicRepositoryUnreadable)), 2)),
	contentRetained(revised(observation(EventDatasourceSourceAccessRestored, CategorySystem,
		"A datasource source could read its repository again and was returned to active.",
		str("repo"), str("installation_id"),
		enum("restored_from",
			DatasourceAccessLostRepositoryUnavailable, DatasourceAccessLostSuspended,
			DatasourceAccessLostPublicRepositoryUnreadable)), 2)),
	contentRetained(observation(EventDatasourceBlobFetched, CategorySystem, "A module fetched a datasource blob's bytes over FetchDatasourceBlob.",
		str("repo"), str("blob_sha"), PayloadField{Name: "bytes", Kind: FieldInt})),
	contentRetained(observation(EventDatasourceFilesFetched, CategorySystem, "A module fetched a batch of a datasource's files at one version over FetchDatasourceFiles.",
		str("repo"), str("version"), PayloadField{Name: "files", Kind: FieldInt}, PayloadField{Name: "bytes", Kind: FieldInt})),
	securityRetained(revised(mutation(EventWebhookSecretRotated, CategorySystem, "A webhook signing secret was rotated.", webhookAdminFields...), webhookAdminVersion)),
	securityRetained(observation(EventJobReplayed, CategorySystem, "A background job was replayed.")),
	securityRetained(mutation(EventFeatureFlagUpdated, CategorySystem, "A legacy feature flag was updated.")),
	securityRetained(mutation(EventEventSubscriptionCreated, CategorySystem, "A domain-event subscription was created.",
		uid("subscription_id"), uid("subscriber_principal_id"), str("type_pattern"), str("queue"))),
	securityRetained(mutation(EventEventSubscriptionRevoked, CategorySystem, "A domain-event subscription was revoked.", uid("subscription_id"))),
	securityRetained(mutation(EventEventReplayed, CategorySystem, "Domain events were replayed to a subscriber.",
		str("type"), PayloadField{Name: "redelivered", Kind: FieldInt})),
	contentRetained(observation(EventDocumentRead, CategoryAccess, "A document read returned evidence or an explicit outcome.", documentReadFields...)),
	contentRetained(observation(EventDocumentSearch, CategoryAccess, "A collection search returned evidence or an explicit outcome.", documentReadFields...)),
	contentRetained(mutation(EventDocumentIngested, CategoryLifecycle, "A document was ingested into a solution.", documentFields...)),
	contentRetained(mutation(EventDocumentVersionMinted, CategoryLifecycle, "A new document version was minted.", documentFields...)),
	contentRetained(mutation(EventDocumentRenamed, CategoryLifecycle, "A document was renamed.", documentFields...)),
	contentRetained(mutation(EventDocumentDeleted, CategoryLifecycle, "A document was deleted.", documentFields...)),
	contentRetained(mutation(EventDocumentArchived, CategoryLifecycle, "A document was archived: taken out of the listing, every version kept.", documentFields...)),
	contentRetained(mutation(EventDocumentUnarchived, CategoryLifecycle, "A document was unarchived: listed again.", documentFields...)),
	securityRetained(mutation(EventDocumentQuarantined, CategoryLifecycle, "A document was quarantined.", documentFields...)),
	// Version 2 of the release and the subscription pair: `outcome` became
	// required when a refusal stopped being its own type, so a v1 row (no
	// outcome) and a v2 row are told apart by schema_version, not guessed at.
	securityRetained(revised(mutation(EventDocumentQuarantineReleased, CategoryLifecycle, "A document was released from quarantine, or the release refused (outcome failure).",
		append(append([]PayloadField(nil), documentOutcomeFields...),
			PayloadField{Name: "tenant_mismatch", Kind: FieldBool}, PayloadField{Name: "solution_mismatch", Kind: FieldBool})...), 2)),
	contentRetained(revised(mutation(EventDocumentSubscribed, CategoryLifecycle, "A subscription to a document was created, or refused (outcome failure).", documentOutcomeFields...), 2)),
	contentRetained(revised(mutation(EventDocumentUnsubscribed, CategoryLifecycle, "A subscription to a document was removed, or the removal refused (outcome failure).", documentOutcomeFields...), 2)),
	securityRetained(mutation(EventDocumentOwnershipTransferred, CategoryLifecycle, "A document's owner was reassigned, or the transfer refused (outcome failure).",
		append(append([]PayloadField(nil), documentOutcomeFields...), str("new_owner_subject_id"))...)),
	securityRetained(mutation(EventDocumentFrozen, CategoryLifecycle, "A document was frozen into a boundary-governed record, or the freeze refused (outcome failure). A freeze lasts until a saas.document.unfrozen with outcome success.", documentOutcomeFields...)),
	securityRetained(mutation(EventDocumentUnfrozen, CategoryLifecycle, "A frozen document was released from its freeze, or the unfreeze refused (outcome failure).", documentOutcomeFields...)),
	contentRetained(observation(EventDocumentIngestSkippedStale, CategoryLifecycle, "A document ingest op was refused as behind the order already applied at its path; nothing was written.",
		append(append([]PayloadField(nil), documentFields...), str("path"), PayloadField{Name: "ordinal", Kind: FieldInt})...)),
	contentRetained(observation(EventDocumentPayloadConflict, CategorySystem, "A producer re-ran and offered different bytes for an artifact already stored; the stored bytes were kept.",
		str("solution"), str("producer"), str("producer_version"), str("entry"), str("entry_version"),
		PayloadField{Name: "stored_bytes", Kind: FieldInt}, PayloadField{Name: "offered_bytes", Kind: FieldInt})),
	contentRetained(mutation(EventDocumentSnapshotCommitted, CategoryLifecycle, "A complete source listing was reconciled into a document scope as one effect.", documentSnapshotFields...)),
	contentRetained(observation(EventDocumentSnapshotSkippedStale, CategoryLifecycle, "A source listing was refused as older than the order its scope already holds; nothing was written.", documentSnapshotFields...)),
	contentRetained(mutation(EventDocumentEffectCommitted, CategoryLifecycle, "A batch of document changes committed as one effect.",
		str("solution"), str("digest"), PayloadField{Name: "applied", Kind: FieldInt}, PayloadField{Name: "deleted", Kind: FieldInt})),
	contentRetained(mutation(EventDocumentProductionCommitted, CategoryLifecycle, "A producer's derived artifact for a document version committed as one effect.",
		str("solution"), str("effect_key"), str("task_id"), str("digest"), str("producer"), str("producer_version"))),
	contentRetained(mutation(EventDocumentKnowledgePublished, CategoryLifecycle, "A knowledge card was published into a collection as one effect.",
		append(append([]PayloadField(nil), documentFields...), str("effect_key"), str("run_id"), str("digest"))...)),
	// Everything this record exists to say is required, because a redrive row
	// that names no version, no stage or no operation is indistinguishable from a
	// complete one while answering none of the questions it was written to
	// answer. `version` is required here and optional on its siblings on purpose:
	// a redrive re-derives one specific version, so a redrive that cannot name
	// one is not a weaker record, it is a different event.
	//
	// correlation_id is the operator's whole redrive, repeated on every entry it
	// re-queued — the same role it plays on saas.document.read, and the reason a
	// partially-completed bulk redrive is legible afterwards rather than looking
	// like a smaller one that finished. It is also what makes a safe emission
	// idempotency key possible: keyed on the operation, a transport retry
	// collapses while a genuine second redrive of the same entry still records.
	//
	// error_class is a closed vocabulary because the spine's vocabulary is the
	// host's (as `outcome` is on the read events), and it enumerates the whole
	// space because a producer that cannot say something true picks something
	// false: `cancelled` and `unknown` exist so a run abandoned without a verdict
	// and a dead letter that carries no classification are not filed as unreadable
	// input. `producer` cannot be an enum — the host does not know a module's
	// stages — so it is bounded instead: short enough to name a stage, too short
	// to carry the failure text the spine deliberately does not hold.
	securityRetained(requiresIdempotencyKey(requiresEntry(mutation(EventDocumentDeadLetterRedriven, CategoryLifecycle, "An operator re-queued a document's dead-lettered derivation.",
		append(required(documentFields, "version"),
			PayloadField{Name: "correlation_id", Kind: FieldString, Required: true, MaxLen: 255},
			PayloadField{Name: "producer", Kind: FieldString, Required: true, MaxLen: 128},
			PayloadField{Name: "error_class", Kind: FieldEnum, Required: true,
				Enum: []string{"permanent", "exhausted", "cancelled", "unknown"}})...)))),
}

// webhookAdminVersion is version 2 of the webhook administration events: the
// version at which actor_id became the initiating user rather than the
// organization the change was made in, and `delegated_by` appeared. The value
// of actor_id changed meaning under a name that could not change, so the
// version is what tells a v1 row (actor_id is an org) from a v2 row (actor_id
// is a user) — in the audit table and in the webhook fan-out alike. Without it
// the release boundary exists only in prose, and an append-only trail cannot be
// re-dated later.
const webhookAdminVersion = 2

// webhookAdminFields is the shared payload of the webhook administration
// events. The initiator itself is the row's actor_id/actor_type; `delegated_by`
// records the RFC 8693 `act` parties that called on the initiator's behalf,
// immediate delegate first, and is absent on a direct call. It is a list
// because a delegation chain nests: recording only its head would discard every
// intermediary, and the chain lives nowhere but the request's token.
var webhookAdminFields = []PayloadField{
	strs("delegated_by"),
}

// documentFields is the shared payload of every document.* event. `solution`
// and `version` name the write; `boundary` is the data boundary (scope node) it
// landed in; actor/owner principal ids and `initiator` (a provenance string,
// e.g. a webhook delivery id) make a solution-owned write attributable (#473).
// sourceDelegationFields is what every source delegation event names: which
// delegation, of which source, by which person, to which module binding.
var sourceDelegationFields = []PayloadField{uid("delegation_id"), uid("source_id"), uid("principal_id"), str("module"), str("binding_id")}

var sourceSyncFields = []PayloadField{str("solution"), str("job_id"), str("repo"), str("commit"), PayloadField{Name: "processed", Kind: FieldInt}, PayloadField{Name: "new_versions", Kind: FieldInt}, PayloadField{Name: "deleted", Kind: FieldInt}, PayloadField{Name: "failed", Kind: FieldInt}, str("reason"), str("code"), str("trigger"), PayloadField{Name: "attempt", Kind: FieldInt}, PayloadField{Name: "retryable", Kind: FieldBool}}

var documentFields = []PayloadField{
	str("solution"),
	str("version"),
	str("boundary"),
	uid("actor_principal_id"),
	uid("owner_principal_id"),
	str("initiator"),
}

// documentOutcomeFields is documentFields plus the outcome of a governance
// action the documents store audits whether it committed or was refused.
// `outcome` is required: on an append-only trail a row with no outcome reads as
// a success that happens to carry a reason, and cannot be corrected later.
// `reason` says why a failure failed.
//
// A refusal records THAT a claim did not match, never whose claim it was: a
// release refused because its approval named another tenant or solution sets
// tenant_mismatch / solution_mismatch, and the other tenant's identifier is
// never written to this tenant's trail, whose rows reach the tenant's own
// webhook receiver, export destination and download.
var documentOutcomeFields = append(append([]PayloadField(nil), documentFields...),
	PayloadField{Name: "outcome", Kind: FieldEnum, Required: true, Enum: []string{"success", "failure"}}, str("reason"))

// documentSnapshotFields is the receipt of one reconciled source listing:
// the digest of the listing, how many entries it tombstoned and how many it
// confirmed unchanged, and the delivery ordinal it was ranked at.
var documentSnapshotFields = []PayloadField{
	str("solution"), str("digest"),
	PayloadField{Name: "deleted", Kind: FieldInt}, PayloadField{Name: "retained", Kind: FieldInt},
	PayloadField{Name: "ordinal", Kind: FieldInt},
}

// Observed read telemetry never carries query text, excerpts or credentials.
var documentReadFields = func() []PayloadField {
	fields := append([]PayloadField(nil), documentFields...)
	for i := range fields {
		if fields[i].Name == "boundary" {
			fields[i].Required = true
		}
	}
	return append(fields,
		PayloadField{Name: "correlation_id", Kind: FieldString, Required: true},
		PayloadField{Name: "outcome", Kind: FieldEnum, Required: true, Enum: []string{"returned", "empty", "denied", "failed"}},
		PayloadField{Name: "result_count", Kind: FieldInt}, PayloadField{Name: "duration_ms", Kind: FieldInt})
}()

// auditEventIndex resolves an event type to its definition. Built once.
var auditEventIndex = func() map[EventType]AuditEventDefinition {
	m := make(map[EventType]AuditEventDefinition, len(auditEventCatalog))
	for _, d := range auditEventCatalog {
		if _, dup := m[d.Type]; dup {
			panic(fmt.Sprintf("audit registry: duplicate event type %q", d.Type))
		}
		if d.Durability != DurabilityTransactional && d.Durability != DurabilityObservational {
			panic(fmt.Sprintf("audit registry: event type %q has no durability classification", d.Type))
		}
		if d.Retention != RetentionSecurity && d.Retention != RetentionContent {
			panic(fmt.Sprintf("audit registry: event type %q has no retention class", d.Type))
		}
		m[d.Type] = d
	}
	return m
}()

// IsTransactionalAuditEvent reports whether an event type must be written on a
// transaction the caller's success depends on. Unregistered types are not
// transactional: the module-facing surface accepts caller-supplied types and
// writes them through emitEntryTx regardless.
func IsTransactionalAuditEvent(t EventType) bool {
	d, ok := auditEventIndex[t]
	return ok && d.Durability == DurabilityTransactional
}

// AuditEventRequiresEntry reports whether an event type's record must name the
// resource it happened to. Unregistered types require nothing: the module-facing
// surface rejects them outright, so answering true here would only replace that
// refusal with a less accurate one.
func AuditEventRequiresEntry(t EventType) bool {
	d, ok := auditEventIndex[t]
	return ok && d.RequiresEntry
}

// AuditEventRequiresIdempotencyKey reports whether an emitter of this type must
// name the operation it is recording, so a retried emit collapses rather than
// writing the same fact twice. Unregistered types require nothing, for the same
// reason as above.
func AuditEventRequiresIdempotencyKey(t EventType) bool {
	d, ok := auditEventIndex[t]
	return ok && d.RequiresIdempotencyKey
}

// AuditEventCatalog returns the registered event definitions sorted by type,
// so DB seeding and the generated facet are deterministic.
func AuditEventCatalog() []AuditEventDefinition {
	out := append([]AuditEventDefinition(nil), auditEventCatalog...)
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// LookupAuditEvent returns the definition for an event type and whether it is
// registered.
func LookupAuditEvent(t EventType) (AuditEventDefinition, bool) {
	d, ok := auditEventIndex[t]
	return d, ok
}

// ValidatePayload checks a payload against the registered schema for the event
// type. It returns an error describing the first problem (unknown type, unknown
// field, missing required field, wrong kind, bad enum value).
//
// Callers treat the result as advisory: an audit record is never dropped
// because validation failed — the security event is more valuable than schema
// purity — but the error is logged so drift surfaces. See DurableAuditEmitter.
func ValidatePayload(t EventType, payload map[string]any) error {
	d, ok := auditEventIndex[t]
	if !ok {
		return fmt.Errorf("audit: unregistered event type %q", t)
	}
	return validatePayloadFields(t, d.Fields, payload)
}

// validatePayloadFields checks a payload against one declared field set. It is
// the single typed-field check both the code-owned catalog and a
// solution-declared type go through, so a kind means the same thing whichever
// registry declared it.
func validatePayloadFields(t EventType, declared []PayloadField, payload map[string]any) error {
	fields := make(map[string]PayloadField, len(declared))
	for _, f := range declared {
		fields[f.Name] = f
	}
	for name := range payload {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("audit: event %q has no registered field %q", t, name)
		}
	}
	for _, f := range declared {
		v, present := payload[f.Name]
		if !present {
			if f.Required {
				return fmt.Errorf("audit: event %q missing required field %q", t, f.Name)
			}
			continue
		}
		if err := validateField(t, f, v); err != nil {
			return err
		}
	}
	return nil
}

func validateField(t EventType, f PayloadField, v any) error {
	// A required string-valued field means a value, not a present key. This was
	// once a list of the two event types that had been found to need it, which
	// made every type nobody thought to add accept "" for a field the registry
	// and the docs both called required — a row that reads as a complete record
	// and identifies nothing. The rule belongs to the declaration, so it holds
	// for types that do not yet exist. Bool and int kinds are untouched: false
	// and 0 are values, and two registered events depend on recording them.
	if f.Required && isStringKind(f.Kind) {
		if value, ok := v.(string); !ok || strings.TrimSpace(value) == "" {
			return fmt.Errorf("audit: event %q field %q requires a nonempty string", t, f.Name)
		}
	}
	if f.MaxLen > 0 && isStringKind(f.Kind) {
		if value, ok := v.(string); ok && len(value) > f.MaxLen {
			return fmt.Errorf("audit: event %q field %q is %d bytes, over its %d-byte bound", t, f.Name, len(value), f.MaxLen)
		}
	}
	switch f.Kind {
	case FieldString, FieldUUID:
		if _, ok := v.(string); !ok {
			return fmt.Errorf("audit: event %q field %q expects a string", t, f.Name)
		}
	case FieldEnum:
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("audit: event %q field %q expects a string", t, f.Name)
		}
		for _, allowed := range f.Enum {
			if s == allowed {
				return nil
			}
		}
		return fmt.Errorf("audit: event %q field %q value %q not in enum %v", t, f.Name, s, f.Enum)
	case FieldInt:
		switch value := v.(type) {
		case int, int32, int64:
		case float64:
			// A protobuf Struct and decoded JSON carry every number as a float64,
			// so the value itself must be an int. A non-finite value cannot be
			// stored: jsonb has no NaN or ±Inf, and the audit insert would record
			// the event with its payload dropped.
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return fmt.Errorf("audit: event %q field %q expects a finite int", t, f.Name)
			}
			if value != math.Trunc(value) {
				return fmt.Errorf("audit: event %q field %q expects an int, not a fraction", t, f.Name)
			}
			if math.Abs(value) > maxExactFloatInt {
				return fmt.Errorf("audit: event %q field %q expects an int within ±%d", t, f.Name, int64(maxExactFloatInt))
			}
		default:
			return fmt.Errorf("audit: event %q field %q expects an int", t, f.Name)
		}
	case FieldNumber:
		var n float64
		switch value := v.(type) {
		case int:
			n = float64(value)
		case int32:
			n = float64(value)
		case int64:
			n = float64(value)
		case float32:
			n = float64(value)
		case float64:
			n = value
		default:
			return fmt.Errorf("audit: event %q field %q expects a number", t, f.Name)
		}
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return fmt.Errorf("audit: event %q field %q expects a finite number", t, f.Name)
		}
	case FieldBool:
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("audit: event %q field %q expects a bool", t, f.Name)
		}
	case FieldStringArray:
		if _, ok := v.([]string); ok {
			return nil
		}
		arr, ok := v.([]any)
		if !ok {
			return fmt.Errorf("audit: event %q field %q expects a string array", t, f.Name)
		}
		for _, e := range arr {
			if _, ok := e.(string); !ok {
				return fmt.Errorf("audit: event %q field %q expects a string array", t, f.Name)
			}
		}
	}
	return nil
}

// PayloadSchemaJSON is the marshaled JSON Schema stored in
// audit_event_types.payload_schema by the DB projection.
func (d AuditEventDefinition) PayloadSchemaJSON() []byte {
	b, err := json.Marshal(d.payloadJSONSchema())
	if err != nil {
		return []byte("{}")
	}
	return b
}

// payloadJSONSchema renders a definition's fields as a JSON Schema object, the
// portable form stored in audit_event_types.payload_schema.
func (d AuditEventDefinition) payloadJSONSchema() map[string]any {
	properties := make(map[string]any, len(d.Fields))
	var required []string
	for _, f := range d.Fields {
		prop := map[string]any{}
		switch f.Kind {
		case FieldUUID:
			prop["type"] = "string"
			prop["format"] = "uuid"
		case FieldEnum:
			prop["type"] = "string"
			prop["enum"] = f.Enum
		case FieldInt:
			prop["type"] = "integer"
		case FieldNumber:
			prop["type"] = "number"
		case FieldBool:
			prop["type"] = "boolean"
		case FieldStringArray:
			prop["type"] = "array"
			prop["items"] = map[string]any{"type": "string"}
		default:
			prop["type"] = "string"
		}
		if f.PII {
			prop["x-pii"] = true
		}
		properties[f.Name] = prop
		if f.Required {
			required = append(required, f.Name)
		}
	}
	schema := map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}
