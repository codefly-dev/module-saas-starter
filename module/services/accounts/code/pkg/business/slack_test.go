package business_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// TestSlackNotifierBlocksSSRF proves Send goes through the hardened webhook
// client: a webhook URL pointed at a private/loopback address is refused at
// dial time instead of being used to reach an internal service.
func TestSlackNotifierBlocksSSRF(t *testing.T) {
	for _, url := range []string{
		"https://127.0.0.1/webhook",
		"https://169.254.169.254/latest/meta-data",
		"https://10.0.0.5/webhook",
	} {
		notifier := business.NewSlackNotifier(url)
		err := notifier.Send(t.Context(), "", "hello")
		require.Error(t, err, "expected %s to be refused", url)
	}
}

func TestSlackNotifierEmptyURLIsNoop(t *testing.T) {
	notifier := business.NewSlackNotifier("")
	require.NoError(t, notifier.Send(t.Context(), "", "hello"))
}
