package business_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

func TestConsentRejectsStalePolicyVersions(t *testing.T) {
	store := newWaitlistStoreFake()
	service, err := business.NewService(store)
	require.NoError(t, err)

	err = service.AcceptTerms(context.Background(), "user-1", "stale", "test")
	require.Error(t, err)
	err = service.UpdateConsentPreferences(
		context.Background(), "user-1", "stale", true, true, "EU", "test",
	)
	require.Error(t, err)
}

type consentStoreFake struct {
	*waitlistStoreFake
	termsContext string
}

func (f *consentStoreFake) SetUserConsent(
	_ context.Context,
	_, _, consentContext string,
	_ time.Time,
) error {
	f.termsContext = consentContext
	return nil
}

func TestTermsAcceptancePersistsRequestContext(t *testing.T) {
	store := &consentStoreFake{waitlistStoreFake: newWaitlistStoreFake()}
	service, err := business.NewService(store)
	require.NoError(t, err)

	err = service.AcceptTerms(
		context.Background(),
		"user-1",
		business.CurrentTermsVersion,
		"consent_banner",
	)

	require.NoError(t, err)
	require.Equal(t, "consent_banner", store.termsContext)
}
