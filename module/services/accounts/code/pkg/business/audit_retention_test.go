package business

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Every registered event type declares a retention class (ADR 0009), the way
// it declares a durability: a type without one cannot be registered.
func TestAuditCatalog_EveryTypeDeclaresARetentionClass(t *testing.T) {
	for _, d := range auditEventCatalog {
		require.Contains(t, []AuditRetentionClass{RetentionSecurity, RetentionContent}, d.Retention,
			"event %q must be wrapped in securityRetained or contentRetained", d.Type)
	}
	for _, d := range AuditEventCatalog() {
		resolved, err := NewAuditEventResolver(nil).Resolve(t.Context(), d.Type)
		require.NoError(t, err)
		require.Equal(t, d.Retention, resolved.RetentionClass(), "the resolver answers the declared class for %q", d.Type)
	}
}

// The security category is sign-in and MFA; every one of its types is
// security-retained, though the class reaches well beyond the category.
func TestAuditCatalog_SecurityCategoryIsSecurityRetained(t *testing.T) {
	securityRetainedOutsideCategory := 0
	for _, d := range auditEventCatalog {
		if d.Category == CategorySecurity {
			require.Equal(t, RetentionSecurity, d.Retention, "event %q", d.Type)
		} else if d.Retention == RetentionSecurity {
			securityRetainedOutsideCategory++
		}
	}
	require.Positive(t, securityRetainedOutsideCategory, "the class is its own field, not the category renamed")
}

// The ADR's examples of each class, held to their declared class.
func TestAuditCatalog_RetentionClassExamples(t *testing.T) {
	for eventType, want := range map[EventType]AuditRetentionClass{
		EventAuthLogin:                 RetentionSecurity, // authentication
		EventDelegatedAudienceExchange: RetentionSecurity, // includes refused exchanges
		EventRoleAssigned:              RetentionSecurity, // permission and role changes
		EventPlatformImpersonated:      RetentionSecurity, // admin actions
		EventGDPRExportReq:             RetentionSecurity, // data exports
		EventWebhookSecretRotated:      RetentionSecurity, // configuration changes
		EventOrgGenericSettingsUpdated: RetentionSecurity,
		EventUserUpdated:               RetentionContent,
		EventDocumentRead:              RetentionContent,
		EventDatasourceSyncCompleted:   RetentionContent,
		EventOnboardingStepDone:        RetentionContent,
		EventBillingPortalOpened:       RetentionContent,
	} {
		d, ok := LookupAuditEvent(eventType)
		require.True(t, ok, eventType)
		require.Equal(t, want, d.Retention, eventType)
	}
}

func TestDeclaredAuditEventTypesAreContentRetained(t *testing.T) {
	declared := declaredType("acme", AuditVisibilityExternal)
	require.Equal(t, RetentionContent, declared.Definition().Retention)

	resolved, err := NewAuditEventResolver(declaredTypes{declared: declared}).Resolve(t.Context(), declared.Type)
	require.NoError(t, err)
	require.Equal(t, RetentionContent, resolved.RetentionClass())
}

func TestUnregisteredAuditEventTypesKeepTheirDetails(t *testing.T) {
	resolved, err := NewAuditEventResolver(nil).Resolve(t.Context(), "saas.retired.event_type")
	require.NoError(t, err)
	require.False(t, resolved.Registered)
	require.Equal(t, RetentionSecurity, resolved.RetentionClass())
}
