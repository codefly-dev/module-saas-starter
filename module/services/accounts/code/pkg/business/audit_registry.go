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
	Version    int
	Category   string
	Owner      string
	Deprecated bool
}

// AuditEventDefinition is one registered event type.
type AuditEventDefinition struct {
	Type        EventType
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
	return AuditEventDefinition{Type: t, Version: 1, Category: cat, Owner: "accounts", Description: desc, Fields: fields}
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
	EventUserRegistered  EventType = "user.registered.v1"
	EventUserCreated     EventType = "user.created.v1"
	EventUserUpdated     EventType = "user.updated.v1"
	EventUserDeleted     EventType = "user.deleted.v1"
	EventUserSuspended   EventType = "user.suspended.v1"
	EventUserUnsuspended EventType = "user.unsuspended.v1"
	EventUserIdentityAdd EventType = "user.identity_added.v1"
	EventSettingsUpdated EventType = "settings.updated.v1"
	EventConsentTerms    EventType = "consent.terms_accepted.v1"
	EventConsentPrefs    EventType = "consent.preferences_updated.v1"

	EventAPIKeyCreated          EventType = "api_key.created.v1"
	EventAPIKeyRevoked          EventType = "api_key.revoked.v1"
	EventRoleCreated            EventType = "role.created.v1"
	EventRoleUpdated            EventType = "role.updated.v1"
	EventRoleDeleted            EventType = "role.deleted.v1"
	EventRoleAssigned           EventType = "role.assigned.v1"
	EventRoleRevoked            EventType = "role.revoked.v1"
	EventSessionRevoked         EventType = "session.revoked.v1"
	EventInvitationCreated      EventType = "invitation.created.v1"
	EventInvitationAccepted     EventType = "invitation.accepted.v1"
	EventInvitationRevoked      EventType = "invitation.revoked.v1"
	EventInvitationResent       EventType = "invitation.resent.v1"
	EventDelegationRequested    EventType = "delegation.requested.v1"
	EventDelegationApproved     EventType = "delegation.approved.v1"
	EventDelegationDenied       EventType = "delegation.denied.v1"
	EventDelegationAutoApproved EventType = "delegation.auto_approved.v1"
	EventApprovalAsked          EventType = "approval.asked.v1"
	EventApprovalApproved       EventType = "approval.approved.v1"
	EventApprovalDenied         EventType = "approval.denied.v1"
	EventApprovalTimeout        EventType = "approval.timeout.v1"
	EventApprovalEscalated      EventType = "approval.escalated.v1"
	EventApprovalCancelled      EventType = "approval.cancelled.v1"
	EventPrincipalCreated       EventType = "principal.created.v1"
	EventPrincipalRevoked       EventType = "principal.revoked.v1"

	EventScopeNodeRegistered EventType = "scope.node_registered.v1"
	EventScopeGranted        EventType = "scope.granted.v1"
	EventScopeRevoked        EventType = "scope.revoked.v1"
	EventRecordShared        EventType = "record.shared.v1"
	EventRecordShareRevoked  EventType = "record.share_revoked.v1"

	EventWorkContextTaskStarted  EventType = "work_context.task_started.v1"
	EventWorkContextRootSession  EventType = "work_context.root_session_started.v1"
	EventWorkContextChildSession EventType = "work_context.child_session_started.v1"
	EventWorkContextAudienceExch EventType = "work_context.audience_exchanged.v1"

	EventAuthLogin             EventType = "auth.login.v1"
	EventAuthMagicLinkLogin    EventType = "auth.magic_link_login.v1"
	EventAuthSSOJitProvisioned EventType = "auth.sso_jit_provisioned.v1"
	EventAuthOrgSwitched       EventType = "auth.organization_switched.v1"
	EventAuthMFAChallengeStart EventType = "auth.mfa_challenge_started.v1"
	EventAuthMFAChallengeDone  EventType = "auth.mfa_challenge_completed.v1"
	EventMFATOTPSetupStarted   EventType = "mfa.totp_setup_started.v1"
	EventMFATOTPVerified       EventType = "mfa.totp_verified.v1"
	EventMFAWebAuthnRegStarted EventType = "mfa.webauthn_registration_started.v1"
	EventMFAWebAuthnRegistered EventType = "mfa.webauthn_registered.v1"
	EventMFAWebAuthnUsed       EventType = "mfa.webauthn_used.v1"
	EventMFABackupGenerated    EventType = "mfa.backup_codes_generated.v1"
	EventMFABackupUsed         EventType = "mfa.backup_code_used.v1"
	EventMFADeviceRevoked      EventType = "mfa.device_revoked.v1"
	EventPlatformRoleGranted   EventType = "platform.role_granted.v1"
	EventPlatformRoleRevoked   EventType = "platform.role_revoked.v1"
	EventPlatformImpersonated  EventType = "platform.user_impersonated.v1"

	EventBillingCheckoutStarted EventType = "billing.checkout_started.v1"
	EventBillingPortalOpened    EventType = "billing.portal_opened.v1"
	EventBillingFreePlan        EventType = "billing.free_plan_selected.v1"
	EventEntitlementOverride    EventType = "entitlement.override.v1"

	EventOrgCreated                EventType = "org.created.v1"
	EventOrgMemberAdded            EventType = "org.member_added.v1"
	EventOrgMemberRemoved          EventType = "org.member_removed.v1"
	EventOrgSettingsUpdated        EventType = "org.settings_updated.v1"
	EventOrgGenericSettingsUpdated EventType = "org.generic_settings_updated.v1"
	EventTeamCreated               EventType = "team.created.v1"
	EventTeamUpdated               EventType = "team.updated.v1"
	EventTeamDeleted               EventType = "team.deleted.v1"
	EventTeamMemberAdded           EventType = "team.member_added.v1"
	EventTeamMemberRemoved         EventType = "team.member_removed.v1"
	EventSSOSetupStarted           EventType = "sso.setup.started.v1"
	EventSSODisabled               EventType = "sso.disabled.v1"
	EventOnboardingStepDone        EventType = "onboarding.step_completed.v1"
	EventOnboardingStepSkip        EventType = "onboarding.step_skipped.v1"
	EventActivationAchieved        EventType = "activation.achieved.v1"

	EventWaitlistJoined    EventType = "waitlist.joined.v1"
	EventWaitlistPending   EventType = "waitlist.pending.v1"
	EventWaitlistVerified  EventType = "waitlist.verified.v1"
	EventWaitlistReviewed  EventType = "waitlist.reviewed.v1"
	EventWaitlistApproved  EventType = "waitlist.approved.v1"
	EventWaitlistInvited   EventType = "waitlist.invited.v1"
	EventWaitlistConverted EventType = "waitlist.converted.v1"
	EventWaitlistRejected  EventType = "waitlist.rejected.v1"
	EventGDPRExportReq     EventType = "gdpr.export_requested.v1"
	EventGDPRDeletionReq   EventType = "gdpr.deletion_requested.v1"
	EventGDPRDeletionDone  EventType = "gdpr.deletion_completed.v1"

	EventWebhookCreated       EventType = "webhook.created.v1"
	EventWebhookDeleted       EventType = "webhook.deleted.v1"
	EventWebhookReplayed      EventType = "webhook.replayed.v1"
	EventWebhookSecretRotated EventType = "webhook.secret_rotated.v1"
	EventJobReplayed          EventType = "job.replayed.v1"

	EventDatasourceSourceAdded   EventType = "datasource.source.added.v1"
	EventDatasourceSourceSynced  EventType = "datasource.source.synced.v1"
	EventDatasourceSourceRemoved EventType = "datasource.source.removed.v1"
	EventFeatureFlagUpdated      EventType = "feature_flag.updated.v1"
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
	def(EventScopeNodeRegistered, CategoryAccess, "A scope node was registered.", str("scope_path"), str("kind")),
	def(EventScopeGranted, CategoryAccess, "A role was granted at a scope node.", uid("role_id"), uid("subject_id"), str("scope_path")),
	def(EventScopeRevoked, CategoryAccess, "A scope grant was revoked.", uid("role_id"), str("scope_path")),
	def(EventRecordShared, CategoryAccess, "A record was shared with a principal or team.", uid("role_id"), uid("subject_id")),
	def(EventRecordShareRevoked, CategoryAccess, "A record share was revoked.", uid("role_id"), uid("subject_id")),
	def(EventWorkContextTaskStarted, CategoryAccess, "A signed Work Context was issued for a new agent task and root session."),
	def(EventWorkContextRootSession, CategoryAccess, "A new root agent session was started under an existing task."),
	def(EventWorkContextChildSession, CategoryAccess, "An attenuated child agent session was started."),
	def(EventWorkContextAudienceExch, CategoryAccess, "A Work Context task and session lineage was reissued for another audience."),

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
	def(EventWebhookSecretRotated, CategorySystem, "A webhook signing secret was rotated."),
	def(EventJobReplayed, CategorySystem, "A background job was replayed."),
	def(EventFeatureFlagUpdated, CategorySystem, "A legacy feature flag was updated."),
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
