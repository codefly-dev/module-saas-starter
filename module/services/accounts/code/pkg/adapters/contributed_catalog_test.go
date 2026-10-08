package adapters

import (
	"accounts/pkg/business"
	catalogv1 "accounts/pkg/gen/saas/catalog/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"os"
	"sort"
	"testing"
)

// The catalog also carries permissions admitted by the module composer, whose
// namespace-qualified logical IDs are wider than built-in descriptor scopes.
func TestServiceCatalogContributedPermissionVocabulary(t *testing.T) {
	raw, err := os.ReadFile("../../../generated/service-catalog.json")
	require.NoError(t, err)
	baseline := &catalogv1.ServiceCatalog{}
	require.NoError(t, protojson.Unmarshal(raw, baseline))
	for _, tc := range []struct {
		resource, action string
		valid            bool
	}{
		{"example.rows", "read", true},
		{"example-suite.row_items", "update-item", true},
		{"example..rows", "read", false},
		{"Example.rows", "read", false},
		{"example.rows", "read/any", false},
		{"example.rows", "read:", false},
	} {
		t.Run(tc.resource+":"+tc.action, func(t *testing.T) {
			catalog := proto.Clone(baseline).(*catalogv1.ServiceCatalog)
			catalog.Permissions = append(catalog.Permissions, &catalogv1.PermissionDefinition{Permission: tc.resource + ":" + tc.action, Resource: tc.resource, Action: tc.action, Description: "Example contributed permission"})
			sort.Slice(catalog.Permissions, func(i, j int) bool { return catalog.Permissions[i].Permission < catalog.Permissions[j].Permission })
			err := business.ValidateServiceCatalog(catalog)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "not canonical")
			}
		})
	}
}
