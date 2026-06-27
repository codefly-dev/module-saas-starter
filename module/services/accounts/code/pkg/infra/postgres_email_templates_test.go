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
	require.NotEmpty(t, tmpl.SubjectTemplate)
}

func TestGetTemplate_NotFound(t *testing.T) {
	tmplStore := infra.NewPostgresTemplateStore(testStore)

	_, err := tmplStore.GetTemplate(testCtx, "nonexistent-template")
	require.Error(t, err)
	var storeErr *business.StoreError
	require.ErrorAs(t, err, &storeErr)
	require.Equal(t, business.ErrTypeNotFound, storeErr.StoreErrorType)
}
