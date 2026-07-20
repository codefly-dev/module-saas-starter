package infra_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	"accounts/pkg/infra"
)

// ============================================================================
// Email Template tests
// ============================================================================

func TestGetTemplate_SeededWelcome(t *testing.T) {
	tmplStore := infra.NewPostgresTemplateStore(testStore)

	tmpl, err := tmplStore.GetTemplate(testCtx, "welcome")
	require.NoError(t, err)
	require.Equal(t, "welcome", tmpl.Name)
	require.Positive(t, tmpl.Version)
	require.NotEmpty(t, tmpl.SubjectTemplate)
}

func TestGetTemplate_BillingCatalogUsesOnlySuppliedManagementVariable(t *testing.T) {
	tmplStore := infra.NewPostgresTemplateStore(testStore)

	for _, name := range []string{"payment_failed", "invoice_ready", "trial_ending"} {
		t.Run(name, func(t *testing.T) {
			tmpl, err := tmplStore.GetTemplate(testCtx, name)
			require.NoError(t, err)
			require.Positive(t, tmpl.Version)
			combined := tmpl.SubjectTemplate + tmpl.HTMLTemplate + tmpl.TextTemplate
			require.Contains(t, combined, "{{billing_url}}")
			require.NotContains(t, combined, "{{pricing_url}}")
			require.NotContains(t, combined, "{{org_name}}")
			require.NotContains(t, combined, "{{amount}}")
			require.NotContains(t, combined, "{{period}}")
			require.NotContains(t, combined, "{{invoice_url}}")
			require.NotContains(t, combined, "{{days_left}}")
		})
	}
}

func TestGetTemplate_NotFound(t *testing.T) {
	tmplStore := infra.NewPostgresTemplateStore(testStore)

	_, err := tmplStore.GetTemplate(testCtx, "nonexistent-template")
	require.Error(t, err)
	var storeErr *business.StoreError
	require.ErrorAs(t, err, &storeErr)
	require.Equal(t, business.ErrTypeNotFound, storeErr.StoreErrorType)
}
