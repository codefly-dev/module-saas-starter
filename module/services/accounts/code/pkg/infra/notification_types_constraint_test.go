//go:build !pure

package infra_test

import (
	"regexp"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// business.NotificationTypes is the set a notification's type is validated
// against before any write, and notifications_type_check is the set the store
// enforces. They must be the same set: a type the constraint admits but the
// validator refuses is an unusable feature, and one the validator admits but
// the constraint refuses surfaces as a failed insert — Internal — which is the
// failure the validator exists to prevent. The constraint is read from
// pg_catalog, not information_schema, which filters by privilege.
func TestNotificationTypesMatchTheStoreConstraint(t *testing.T) {
	var definition string
	require.NoError(t, testPool.QueryRow(testCtx, `
		SELECT pg_get_constraintdef(c.oid)
		  FROM pg_constraint c
		  JOIN pg_class r ON r.oid = c.conrelid
		  JOIN pg_namespace n ON n.oid = r.relnamespace
		 WHERE n.nspname = 'public' AND r.relname = 'notifications' AND c.conname = 'notifications_type_check'`,
	).Scan(&definition))

	var stored []string
	for _, match := range regexp.MustCompile(`'([^']*)'::text`).FindAllStringSubmatch(definition, -1) {
		stored = append(stored, match[1])
	}
	require.NotEmpty(t, stored, "notifications_type_check names no types: %s", definition)

	validated := append([]string(nil), business.NotificationTypes...)
	sort.Strings(stored)
	sort.Strings(validated)
	require.Equal(t, stored, validated, "business.NotificationTypes must be the notifications_type_check set")
}
