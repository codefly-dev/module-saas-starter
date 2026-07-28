package business

import (
	"fmt"

	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/usersettings"
)

type NotificationCategory string

const (
	NotificationCategoryProduct   NotificationCategory = "product"
	NotificationCategoryMarketing NotificationCategory = "marketing"
	NotificationCategoryDigest    NotificationCategory = "digest"
	NotificationCategorySecurity  NotificationCategory = "security"
	NotificationCategoryBilling   NotificationCategory = "billing"
)

type NotificationChannel string

const (
	NotificationChannelInApp NotificationChannel = "in_app"
	NotificationChannelEmail NotificationChannel = "email"
)

type DeliveryDecision struct {
	Deliver   bool
	Mandatory bool
	Reason    string
}

func EvaluateNotificationDelivery(
	settings *gen.UserSettings,
	category NotificationCategory,
	channel NotificationChannel,
) (DeliveryDecision, error) {
	switch channel {
	case NotificationChannelInApp, NotificationChannelEmail:
	default:
		return DeliveryDecision{}, fmt.Errorf("notification channel %q is invalid", channel)
	}

	switch category {
	case NotificationCategorySecurity, NotificationCategoryBilling:
		return DeliveryDecision{
			Deliver:   true,
			Mandatory: true,
			Reason:    "mandatory",
		}, nil
	case NotificationCategoryProduct,
		NotificationCategoryMarketing,
		NotificationCategoryDigest:
	default:
		return DeliveryDecision{}, fmt.Errorf("notification category %q is invalid", category)
	}

	var (
		enabled bool
		err     error
	)
	switch channel {
	case NotificationChannelInApp:
		enabled, err = usersettings.Fields.Notifications.InApp.Get(settings)
	case NotificationChannelEmail:
		switch category {
		case NotificationCategoryProduct:
			enabled, err = usersettings.Fields.Email.Product.Get(settings)
		case NotificationCategoryMarketing:
			enabled, err = usersettings.Fields.Email.Marketing.Get(settings)
		case NotificationCategoryDigest:
			enabled, err = usersettings.Fields.Email.WeeklyDigest.Get(settings)
		}
	}
	if err != nil {
		return DeliveryDecision{}, err
	}
	if !enabled {
		return DeliveryDecision{Reason: "user_opt_out"}, nil
	}
	return DeliveryDecision{Deliver: true, Reason: "enabled"}, nil
}
