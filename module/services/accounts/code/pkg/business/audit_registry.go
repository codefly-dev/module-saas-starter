package business

import (
	"encoding/json"
	"fmt"
	"sort"
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
)

// FieldKind is the declared type of one payload field. Payloads are validated
// against these at the emit choke point; the JSON Schema projection stored in
// audit_event_types.payload_schema is generated from the same fields.
type FieldKind string

const (
	FieldString      FieldKind = "string"
	FieldUUID        FieldKind = "uuid"
	FieldInt         FieldKind = "int"
	FieldBool        FieldKind = "bool"
	FieldEnum        FieldKind = "enum"
	FieldStringArray FieldKind = "string_array"
)

// PayloadField declares one field of a typed audit payload. PII marks a field
// as personally identifying: it is stripped from every export path so audit
// destinations (the customer's S3 bucket, CSV/JSON downloads) never receive it.
type PayloadField struct {
	Name     string
	Kind     FieldKind
	Required bool
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

// AuditEventDefinition is one registered event type. Namespace is the collision
// key (always the leading segment of Type); Owner names the service that emits
// it, which is a different axis entirely.
type AuditEventDefinition struct {
	Type        EventType
	Namespace   string
	Version     int
	Category    AuditCategory
	Owner       string
	Description string
	Fields      []PayloadField
}

// obj is a terse constructor for a definition with a v1 payload schema owned by
// accounts. Almost every event today carries no structured payload; the fields
// declared here are the contract producers fill in as payloads are enriched.
func def(t EventType, cat AuditCategory, desc string, fields ...PayloadField) AuditEventDefinition {
	return AuditEventDefinition{
		Type: t, Namespace: AuditNamespace, Version: 1, Category: cat,
		Owner: "accounts", Description: desc, Fields: fields,
	}
}

func str(name string) PayloadField { return PayloadField{Name: name, Kind: FieldString} }
func uid(name string) PayloadField { return PayloadField{Name: name, Kind: FieldUUID} }
func enum(name string, values ...string) PayloadField {
	return PayloadField{Name: name, Kind: FieldEnum, Enum: values}
}
func pii(f PayloadField) PayloadField { f.PII = true; return f }

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

	EventAPIKeyCreated          EventType = "saas.api_key.created"
	EventModuleRegistrationMint EventType = "saas.module.registration_minted"
	EventAPIKeyRevoked          EventType = "saas.api_key.revoked"
	EventRoleCreated            EventType = "saas.role.created"
	EventRoleUpdated            EventType = "saas.role.updated"
	EventRoleDeleted            EventType = "saas.role.deleted"
	EventRoleAssigned           EventType = "saas.role.assigned"
	EventRoleRevoked            EventType = "saas.role.revoked"
	EventSessionRevoked         EventType = "saas.session.revoked"
	EventInvitationCreated      EventType = "saas.invitation.created"
	EventInvitationAccepted     EventType = "saas.invitation.accepted"
	EventInvitationRevoked      EventType = "saas.invitation.revoked"
	EventInvitationResent       EventType = "saas.invitation.resent"
	EventDelegationRequested    EventType = "saas.delegation.requested"
	EventDelegationApproved     EventType = "saas.delegation.approved"
	EventDelegationDenied       EventType = "saas.delegation.denied"
	EventDelegationAutoApproved EventType = "saas.delegation.auto_approved"
	EventApprovalAsked          EventType = "saas.approval.asked"
	EventApprovalApproved       EventType = "saas.approval.approved"
	EventApprovalDenied         EventType = "saas.approval.denied"
	EventApprovalTimeout        EventType = "saas.approval.timeout"
	EventApprovalEscalated      EventType = "saas.approval.escalated"
	EventApprovalCancelled      EventType = "saas.approval.cancelled"
	EventPrincipalCreated       EventType = "saas.principal.created"
	EventPrincipalRevoked       EventType = "saas.principal.revoked"
	EventPrincipalDisabled      EventType = "saas.principal.disabled"
	EventPrincipalEnabled       EventType = "saas.principal.enabled"

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

	EventAuthLogin             EventType = "saas.auth.login"
	EventAuthMagicLinkLogin    EventType = "saas.auth.magic_link_login"
	EventAuthSSOJitProvisioned EventType = "saas.auth.sso_jit_provisioned"
	EventAuthOrgSwitched       EventType = "saas.auth.organization_switched"
	EventAuthMFAChallengeStart EventType = "saas.auth.mfa_challenge_started"
	EventAuthMFAChallengeDone  EventType = "saas.auth.mfa_challenge_completed"
	EventMFATOTPSetupStarted   EventType = "saas.mfa.totp_setup_started"
	EventMFATOTPVerified       EventType = "saas.mfa.totp_verified"
	EventMFAWebAuthnRegStarted EventType = "saas.mfa.webauthn_registration_started"
	EventMFAWebAuthnRegistered EventType = "saas.mfa.webauthn_registered"
	EventMFAWebAuthnUsed       EventType = "saas.mfa.webauthn_used"
	EventMFABackupGenerated    EventType = "saas.mfa.backup_codes_generated"
	EventMFABackupUsed         EventType = "saas.mfa.backup_code_used"
	EventMFADeviceRevoked      EventType = "saas.mfa.device_revoked"
	EventPlatformRoleGranted   EventType = "saas.platform.role_granted"
	EventPlatformRoleRevoked   EventType = "saas.platform.role_revoked"
	EventPlatformImpersonated  EventType = "saas.platform.user_impersonated"

	EventBillingCheckoutStarted EventType = "saas.billing.checkout_started"
	EventBillingPortalOpened    EventType = "saas.billing.portal_opened"
	EventBillingFreePlan        EventType = "saas.billing.free_plan_selected"
	EventEntitlementOverride    EventType = "saas.entitlement.override"

	EventOrgCreated                EventType = "saas.org.created"
	EventOrgMemberAdded            EventType = "saas.org.member_added"
	EventOrgMemberRemoved          EventType = "saas.org.member_removed"
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

	EventDatasourceSourceAdded         EventType = "saas.datasource.source.added"
	EventDatasourceSourceSynced        EventType = "saas.datasource.source.synced"
	EventDatasourceSourceRemoved       EventType = "saas.datasource.source.removed"
	EventDatasourceChangeSetCompiled   EventType = "saas.datasource.change_set_compiled"
	EventDatasourceForcePushReconciled EventType = "saas.datasource.force_push_reconciled"
	EventDatasourceBranchDeleted       EventType = "saas.datasource.branch_deleted"
	EventDatasourceSnapshotTooLarge    EventType = "saas.datasource.snapshot_too_large"
	EventDatasourceSourceRecovered     EventType = "saas.datasource.source.recovered"
	EventDatasourceBlobFetched         EventType = "saas.datasource.blob_fetched"
	EventFeatureFlagUpdated            EventType = "saas.feature_flag.updated"

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
	EventDocumentVersionMinted      EventType = "saas.document.version_minted"
	EventDocumentRenamed            EventType = "saas.document.renamed"
	EventDocumentDeleted            EventType = "saas.document.deleted"
	EventDocumentQuarantined        EventType = "saas.document.quarantined"
	EventDocumentQuarantineReleased EventType = "saas.document.quarantine_released"
	EventDocumentSubscribed         EventType = "saas.document.subscribed"
	EventDocumentUnsubscribed       EventType = "saas.document.unsubscribed"
)

var auditEventCatalog = []AuditEventDefinition{
	def(EventUserRegistered, CategoryIdentity, "A new user account was registered.",
		enum("signup_method", "password", "sso", "magic_link"), pii(str("email"))),
	def(EventUserCreated, CategoryIdentity, "A user was provisioned by an administrator.", pii(str("email"))),
	def(EventUserUpdated, CategoryIdentity, "A user profile was updated."),
	def(EventUserDeleted, CategoryIdentity, "A user account was deleted."),
	def(EventUserSuspended, CategoryIdentity, "A user account was suspended."),
	def(EventUserUnsuspended, CategoryIdentity, "A user account was reinstated."),
	def(EventUserIdentityAdd, CategoryIdentity, "An external identity was linked to a user.", str("provider")),
	def(EventSettingsUpdated, CategoryIdentity, "A user's personal settings changed."),
	def(EventConsentTerms, CategoryIdentity, "A user accepted the terms of service.", str("version")),
	def(EventConsentPrefs, CategoryIdentity, "A user updated their consent preferences."),

	def(EventAPIKeyCreated, CategoryAccess, "An API key was minted.", uid("key_id"), PayloadField{Name: "scopes", Kind: FieldStringArray}),
	def(EventModuleRegistrationMint, CategoryAccess, "A composed module was issued a gateway registration credential.", str("prefix")),
	def(EventAPIKeyRevoked, CategoryAccess, "An API key was revoked.", uid("key_id")),
	def(EventRoleCreated, CategoryAccess, "A role was created.", str("name")),
	def(EventRoleUpdated, CategoryAccess, "A role was updated."),
	def(EventRoleDeleted, CategoryAccess, "A role was deleted."),
	def(EventRoleAssigned, CategoryAccess, "A role was assigned to a principal.", uid("role_id"), uid("subject_id")),
	def(EventRoleRevoked, CategoryAccess, "A role assignment was revoked.", uid("role_id")),
	def(EventSessionRevoked, CategoryAccess, "A session was revoked."),
	def(EventInvitationCreated, CategoryAccess, "An organization invitation was created.", pii(str("email"))),
	def(EventInvitationAccepted, CategoryAccess, "An organization invitation was accepted."),
	def(EventInvitationRevoked, CategoryAccess, "An organization invitation was revoked."),
	def(EventInvitationResent, CategoryAccess, "An organization invitation was resent."),
	def(EventDelegationRequested, CategoryAccess, "A delegation grant was requested."),
	def(EventDelegationApproved, CategoryAccess, "A delegation grant was approved."),
	def(EventDelegationDenied, CategoryAccess, "A delegation grant was denied."),
	def(EventDelegationAutoApproved, CategoryAccess, "A delegation grant was auto-approved by policy."),
	def(EventApprovalAsked, CategoryAccess, "An approval request was opened for a gated action.", str("resource"), str("action")),
	def(EventApprovalApproved, CategoryAccess, "An approval request reached quorum and was approved.", str("resource"), str("action")),
	def(EventApprovalDenied, CategoryAccess, "An approval request was denied."),
	def(EventApprovalTimeout, CategoryAccess, "An approval request expired before reaching quorum."),
	def(EventApprovalEscalated, CategoryAccess, "An approval request was escalated to a wider approver set."),
	def(EventApprovalCancelled, CategoryAccess, "An approval request was cancelled before a decision.", str("reason")),
	def(EventPrincipalCreated, CategoryAccess, "An agent principal was created.", str("agent_identifier")),
	def(EventPrincipalRevoked, CategoryAccess, "A principal was revoked.", str("reason")),
	def(EventPrincipalDisabled, CategoryAccess, "An agent principal was disabled.", str("reason")),
	def(EventPrincipalEnabled, CategoryAccess, "An agent principal was re-enabled."),
	def(EventScopeNodeRegistered, CategoryAccess, "A scope node was registered.", str("scope_path"), str("kind")),
	def(EventScopeGranted, CategoryAccess, "A role was granted at a scope node.", uid("role_id"), uid("subject_id"), str("scope_path")),
	def(EventScopeRevoked, CategoryAccess, "A scope grant was revoked.", uid("role_id"), str("scope_path")),
	def(EventInstallationCreated, CategoryAccess, "A solution was installed: an agent principal, solution scope node, standing grant, and installation row were composed.",
		uid("agent_principal_id"), str("solution_identifier"), uid("role_id")),
	def(EventInstallationRevoked, CategoryAccess, "A solution was uninstalled: its agent principal and standing grant were revoked and its scope node soft-deleted.",
		str("solution_identifier")),
	def(EventInstallationOwnershipTransferred, CategoryAccess, "An installation's owner of record was reassigned.",
		uid("owner_principal_id")),
	def(EventRecordShared, CategoryAccess, "A record was shared with a principal or team.", uid("role_id"), uid("subject_id")),
	def(EventRecordShareRevoked, CategoryAccess, "A record share was revoked.", uid("role_id"), uid("subject_id")),
	def(EventWorkContextTaskStarted, CategoryAccess, "A signed Work Context was issued for a new agent task and root session."),
	def(EventWorkContextRootSession, CategoryAccess, "A new root agent session was started under an existing task."),
	def(EventWorkContextChildSession, CategoryAccess, "An attenuated child agent session was started."),
	def(EventWorkContextAudienceExch, CategoryAccess, "A Work Context task and session lineage was reissued for another audience."),
	def(EventWorkContextRenewed, CategoryAccess, "A delegated actor renewed its Work Context past the signing TTL cap."),

	def(EventAuthLogin, CategorySecurity, "A user authenticated.", str("method")),
	def(EventAuthMagicLinkLogin, CategorySecurity, "A user authenticated via magic link."),
	def(EventAuthSSOJitProvisioned, CategorySecurity, "A user was just-in-time provisioned via SSO.", str("provider")),
	def(EventAuthOrgSwitched, CategorySecurity, "A user switched active organization."),
	def(EventAuthMFAChallengeStart, CategorySecurity, "An MFA challenge was started."),
	def(EventAuthMFAChallengeDone, CategorySecurity, "An MFA challenge was completed.", enum("factor", "totp", "webauthn", "backup_code")),
	def(EventMFATOTPSetupStarted, CategorySecurity, "TOTP enrollment was started."),
	def(EventMFATOTPVerified, CategorySecurity, "A TOTP device was verified."),
	def(EventMFAWebAuthnRegStarted, CategorySecurity, "WebAuthn registration was started."),
	def(EventMFAWebAuthnRegistered, CategorySecurity, "A WebAuthn credential was registered."),
	def(EventMFAWebAuthnUsed, CategorySecurity, "A WebAuthn credential was used to authenticate."),
	def(EventMFABackupGenerated, CategorySecurity, "MFA backup codes were generated."),
	def(EventMFABackupUsed, CategorySecurity, "An MFA backup code was consumed."),
	def(EventMFADeviceRevoked, CategorySecurity, "An MFA device was revoked."),
	def(EventPlatformRoleGranted, CategorySecurity, "A platform role was granted."),
	def(EventPlatformRoleRevoked, CategorySecurity, "A platform role was revoked."),
	def(EventPlatformImpersonated, CategorySecurity, "A platform admin impersonated a user."),

	def(EventBillingCheckoutStarted, CategoryBilling, "A billing checkout session was started."),
	def(EventBillingPortalOpened, CategoryBilling, "The billing portal was opened."),
	def(EventBillingFreePlan, CategoryBilling, "The free plan was selected."),
	def(EventEntitlementOverride, CategoryBilling, "An entitlement override was set.", str("key")),

	def(EventOrgCreated, CategoryOrganization, "An organization was created.", str("name")),
	def(EventOrgMemberAdded, CategoryOrganization, "A member was added to an organization."),
	def(EventOrgMemberRemoved, CategoryOrganization, "A member was removed from an organization."),
	def(EventOrgSettingsUpdated, CategoryOrganization, "Organization branding settings were updated."),
	def(EventOrgGenericSettingsUpdated, CategoryOrganization, "Organization generic (typed) settings were updated."),
	def(EventTeamCreated, CategoryOrganization, "A team was created.", str("name")),
	def(EventTeamUpdated, CategoryOrganization, "A team was updated."),
	def(EventTeamDeleted, CategoryOrganization, "A team was deleted."),
	def(EventTeamMemberAdded, CategoryOrganization, "A member was added to a team."),
	def(EventTeamMemberRemoved, CategoryOrganization, "A member was removed from a team."),
	def(EventSSOSetupStarted, CategoryOrganization, "SSO configuration was started."),
	def(EventSSODisabled, CategoryOrganization, "SSO was disabled for an organization."),
	def(EventOnboardingStepDone, CategoryOrganization, "An onboarding step was completed.", str("step")),
	def(EventOnboardingStepSkip, CategoryOrganization, "An onboarding step was skipped.", str("step")),
	def(EventActivationAchieved, CategoryOrganization, "An organization reached activation."),

	def(EventDashboardCreated, CategoryOrganization, "A dashboard was created."),
	def(EventDashboardUpdated, CategoryOrganization, "A dashboard was updated."),
	def(EventDashboardDeleted, CategoryOrganization, "A dashboard was deleted."),
	def(EventDashboardShared, CategoryOrganization, "A dashboard's visibility was changed."),

	def(EventWaitlistJoined, CategoryLifecycle, "A prospect joined the waitlist.", pii(str("email"))),
	def(EventWaitlistPending, CategoryLifecycle, "A waitlist entry moved to pending."),
	def(EventWaitlistVerified, CategoryLifecycle, "A waitlist entry was verified."),
	def(EventWaitlistReviewed, CategoryLifecycle, "A waitlist entry was reviewed by an administrator."),
	def(EventWaitlistApproved, CategoryLifecycle, "A waitlist entry was approved."),
	def(EventWaitlistInvited, CategoryLifecycle, "A waitlist entry was invited."),
	def(EventWaitlistConverted, CategoryLifecycle, "A waitlist entry converted to a user."),
	def(EventWaitlistRejected, CategoryLifecycle, "A waitlist entry was rejected."),
	def(EventGDPRExportReq, CategoryLifecycle, "A GDPR data export was requested."),
	def(EventGDPRDeletionReq, CategoryLifecycle, "A GDPR deletion was requested."),
	def(EventGDPRDeletionDone, CategoryLifecycle, "A GDPR deletion completed."),

	def(EventWebhookCreated, CategorySystem, "A webhook subscription was created."),
	def(EventWebhookDeleted, CategorySystem, "A webhook subscription was deleted."),
	def(EventWebhookReplayed, CategorySystem, "A webhook delivery was replayed."),
	def(EventDatasourceSourceAdded, CategorySystem, "A GitHub datasource was connected.", str("repo")),
	def(EventDatasourceSourceSynced, CategorySystem, "A datasource sync was requested."),
	def(EventDatasourceSourceRemoved, CategorySystem, "A datasource was removed."),
	def(EventDatasourceChangeSetCompiled, CategorySystem, "A GitHub delivery was compiled into a change set.",
		str("base"), str("head"), PayloadField{Name: "ops", Kind: FieldInt}, enum("mode", "compare", "snapshot"), str("delivery_id")),
	def(EventDatasourceForcePushReconciled, CategorySystem, "A GitHub force push or divergence was reconciled with a snapshot.",
		str("head"), str("delivery_id")),
	def(EventDatasourceBranchDeleted, CategorySystem, "A GitHub branch-deletion delivery was acknowledged without removing documents.",
		str("ref"), str("delivery_id")),
	def(EventDatasourceSnapshotTooLarge, CategorySystem, "A datasource snapshot manifest exceeded the ingest payload limit; the source was degraded pending operator reset.",
		str("head"), PayloadField{Name: "bytes", Kind: FieldInt}, PayloadField{Name: "limit", Kind: FieldInt}, str("delivery_id")),
	def(EventDatasourceSourceRecovered, CategorySystem, "A degraded datasource source snapshotted within the ingest limit again and was returned to active.",
		str("head"), str("delivery_id")),
	def(EventDatasourceBlobFetched, CategorySystem, "A module fetched a datasource blob's bytes over FetchDatasourceBlob.",
		str("repo"), str("blob_sha"), PayloadField{Name: "bytes", Kind: FieldInt}),
	def(EventWebhookSecretRotated, CategorySystem, "A webhook signing secret was rotated."),
	def(EventJobReplayed, CategorySystem, "A background job was replayed."),
	def(EventFeatureFlagUpdated, CategorySystem, "A legacy feature flag was updated."),
	def(EventDocumentIngested, CategoryLifecycle, "A document was ingested into a solution.", documentFields...),
	def(EventDocumentVersionMinted, CategoryLifecycle, "A new document version was minted.", documentFields...),
	def(EventDocumentRenamed, CategoryLifecycle, "A document was renamed.", documentFields...),
	def(EventDocumentDeleted, CategoryLifecycle, "A document was deleted.", documentFields...),
	def(EventDocumentQuarantined, CategoryLifecycle, "A document was quarantined.", documentFields...),
	def(EventDocumentQuarantineReleased, CategoryLifecycle, "A document was released from quarantine.", documentFields...),
	def(EventDocumentSubscribed, CategoryLifecycle, "A subscription to a document was created.", documentFields...),
	def(EventDocumentUnsubscribed, CategoryLifecycle, "A subscription to a document was removed.", documentFields...),
}

// documentFields is the shared payload of every document.* event. `solution`
// and `version` name the write; `boundary` is the data boundary (scope node) it
// landed in; actor/owner principal ids and `initiator` (a provenance string,
// e.g. a webhook delivery id) make a solution-owned write attributable (#473).
var documentFields = []PayloadField{
	str("solution"),
	str("version"),
	str("boundary"),
	uid("actor_principal_id"),
	uid("owner_principal_id"),
	str("initiator"),
}

// auditEventIndex resolves an event type to its definition. Built once.
var auditEventIndex = func() map[EventType]AuditEventDefinition {
	m := make(map[EventType]AuditEventDefinition, len(auditEventCatalog))
	for _, d := range auditEventCatalog {
		if _, dup := m[d.Type]; dup {
			panic(fmt.Sprintf("audit registry: duplicate event type %q", d.Type))
		}
		m[d.Type] = d
	}
	return m
}()

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
	fields := make(map[string]PayloadField, len(d.Fields))
	for _, f := range d.Fields {
		fields[f.Name] = f
	}
	for name := range payload {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("audit: event %q has no registered field %q", t, name)
		}
	}
	for _, f := range d.Fields {
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
		switch v.(type) {
		case int, int32, int64, float64:
		default:
			return fmt.Errorf("audit: event %q field %q expects an int", t, f.Name)
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

// RedactPayload returns a copy of payload with every field the registry marks
// PII removed. Used on every export path so downstream audit sinks never
// receive personally identifying fields. An unregistered type is redacted
// whole (fail closed): without a schema we cannot tell which fields are safe.
func RedactPayload(t EventType, payload map[string]any) map[string]any {
	if len(payload) == 0 {
		return payload
	}
	d, ok := auditEventIndex[t]
	if !ok {
		return map[string]any{}
	}
	piiFields := make(map[string]struct{})
	for _, f := range d.Fields {
		if f.PII {
			piiFields[f.Name] = struct{}{}
		}
	}
	if len(piiFields) == 0 {
		return payload
	}
	out := make(map[string]any, len(payload))
	for k, v := range payload {
		if _, redacted := piiFields[k]; redacted {
			continue
		}
		out[k] = v
	}
	return out
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
