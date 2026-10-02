package business

import (
	"context"
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

func TestDeclaredAuditEventTypesThatSayNothingAreContent(t *testing.T) {
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

// A solution's manifest and a module's declaration may each state a retention
// class; saying nothing is content, and anything outside the vocabulary is
// refused rather than defaulted.
func TestDeclaredAuditEventTypesDeclareTheirRetentionClass(t *testing.T) {
	declared, err := ParseDeclaredAuditEventTypes("acme", declaringManifest("acme",
		`{"name":"granted","type":"acme.access.granted","retention":"security","fields":[]},`+
			`{"name":"viewed","type":"acme.page.viewed","retention":"content","fields":[]},`+
			`{"name":"opened","type":"acme.page.opened","fields":[]}`))
	require.NoError(t, err)
	classes := map[EventType]AuditRetentionClass{}
	for _, d := range declared {
		classes[d.Type] = d.Retention
		require.Equal(t, d.Retention, d.Definition().Retention)
	}
	require.Equal(t, map[EventType]AuditRetentionClass{
		"acme.access.granted": RetentionSecurity,
		"acme.page.viewed":    RetentionContent,
		"acme.page.opened":    RetentionContent,
	}, classes)

	for _, retention := range []string{"Security", "permanent", "compliance"} {
		_, err := ParseDeclaredAuditEventTypes("acme", declaringManifest("acme",
			`{"name":"granted","type":"acme.access.granted","retention":"`+retention+`","fields":[]}`))
		require.ErrorIs(t, err, ErrSolutionAuditDeclarationRejected, retention)
		require.ErrorContains(t, err, "retention", retention)
	}

	module, err := ValidateAuditEventTypeDeclarations("acme", []AuditEventTypeDeclaration{
		{Type: "acme.access.granted", Retention: "security"},
		{Type: "acme.page.viewed"},
	})
	require.NoError(t, err)
	require.Equal(t, RetentionSecurity, module[0].Retention)
	require.Equal(t, RetentionContent, module[1].Retention)
}

// The class only grows, like pii: a re-declaration may raise content to
// security, and may not lower security — not even by leaving the class out.
func TestDeclaredAuditRetentionClassOnlyGrows(t *testing.T) {
	store := newDeclaredAuditStore()
	svc, _ := newModuleDeclaringService(t, store, moduleGrants("acme", "acme"))
	caller := ModuleCaller{PrincipalID: ModulePrincipalID("acme")}
	declare := func(retention string) error {
		_, _, err := svc.ModuleDeclareAuditEventTypes(context.Background(), caller, "acme",
			[]AuditEventTypeDeclaration{{Type: "acme.access.granted", Retention: retention}})
		return err
	}
	stored := func() AuditRetentionClass { return store.rows["acme.access.granted"].declared.Retention }

	require.NoError(t, declare(""))
	require.Equal(t, RetentionContent, stored())

	require.NoError(t, declare("security"), "raising the class is allowed")
	require.Equal(t, RetentionSecurity, stored())

	require.Error(t, declare("content"), "lowering it is refused")
	require.Error(t, declare(""), "and so is lowering it by omission")
	require.Equal(t, RetentionSecurity, stored())

	puts := store.puts
	require.NoError(t, declare("security"))
	require.Equal(t, puts, store.puts, "re-declaring the admitted class writes nothing")
}
