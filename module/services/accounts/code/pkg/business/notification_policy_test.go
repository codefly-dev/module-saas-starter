package business_test

import (
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/usersettings"

	"github.com/stretchr/testify/require"
)

func TestEvaluateNotificationDeliveryPreferenceMatrix(t *testing.T) {
	settings := &gen.UserSettings{}
	require.NoError(t, usersettings.Fields.Notifications.InApp.Set(settings, false))
	require.NoError(t, usersettings.Fields.Email.Product.Set(settings, false))
	require.NoError(t, usersettings.Fields.Email.Marketing.Set(settings, true))
	require.NoError(t, usersettings.Fields.Email.WeeklyDigest.Set(settings, false))

	tests := []struct {
		name      string
		category  business.NotificationCategory
		channel   business.NotificationChannel
		deliver   bool
		mandatory bool
		reason    string
	}{
		{
			name: "optional in-app opt-out", category: business.NotificationCategoryProduct,
			channel: business.NotificationChannelInApp, reason: "user_opt_out",
		},
		{
			name: "product email opt-out", category: business.NotificationCategoryProduct,
			channel: business.NotificationChannelEmail, reason: "user_opt_out",
		},
		{
			name: "marketing email opt-in", category: business.NotificationCategoryMarketing,
			channel: business.NotificationChannelEmail, deliver: true, reason: "enabled",
		},
		{
			name: "digest email opt-out", category: business.NotificationCategoryDigest,
			channel: business.NotificationChannelEmail, reason: "user_opt_out",
		},
		{
			name: "security is mandatory", category: business.NotificationCategorySecurity,
			channel: business.NotificationChannelEmail, deliver: true, mandatory: true, reason: "mandatory",
		},
		{
			name: "billing is mandatory", category: business.NotificationCategoryBilling,
			channel: business.NotificationChannelInApp, deliver: true, mandatory: true, reason: "mandatory",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, err := business.EvaluateNotificationDelivery(
				settings,
				test.category,
				test.channel,
			)
			require.NoError(t, err)
			require.Equal(t, test.deliver, decision.Deliver)
			require.Equal(t, test.mandatory, decision.Mandatory)
			require.Equal(t, test.reason, decision.Reason)
		})
	}
}

func TestEvaluateNotificationDeliveryRejectsUnknownPolicyInputs(t *testing.T) {
	_, err := business.EvaluateNotificationDelivery(
		&gen.UserSettings{},
		business.NotificationCategory("unknown"),
		business.NotificationChannelEmail,
	)
	require.Error(t, err)

	_, err = business.EvaluateNotificationDelivery(
		&gen.UserSettings{},
		business.NotificationCategoryProduct,
		business.NotificationChannel("push"),
	)
	require.Error(t, err)
}
