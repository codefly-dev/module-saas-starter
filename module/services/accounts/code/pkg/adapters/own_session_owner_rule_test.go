package adapters

import (
	"context"
	"testing"

	corework "github.com/codefly-dev/core/workcontext"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// testOwnSessionOwnerRule drives accounts' own person Work Context mints in
// the conformance harness's composition: documents checks owners of
// example-records; model checks owners of its datasets and shares its
// profiles; records and org-directory check no owner. An administrator's
// organization RBAC holds every kind, so whatever is refused here is refused
// by the core's owner rule (RBAC-4a), named as the core names it.
func testOwnSessionOwnerRule(t *testing.T, conn grpc.ClientConnInterface, admin context.Context) {
	contexts := gen.NewWorkContextServiceClient(conn)
	scope := func(kind string, actions ...string) *gen.WorkContextScope {
		return &gen.WorkContextScope{ResourceKind: kind, Actions: actions}
	}
	start := func(audience string, scopes ...*gen.WorkContextScope) (*gen.IssuedWorkContext, error) {
		return contexts.StartTask(admin, &gen.StartTaskWorkContextRequest{OrgId: fixtureOrganization, TaskId: uuid.NewString(), Audience: audience, AuthorityScopes: scopes})
	}
	carried := func(t *testing.T, issued *gen.IssuedWorkContext) []string {
		t.Helper()
		decoded, err := corework.Decode(issued.GetToken())
		require.NoError(t, err)
		var kinds []string
		for _, sc := range decoded.Context().GetAuthorityScopes() {
			kinds = append(kinds, sc.GetResourceKind())
		}
		return kinds
	}
	refusedByRule := func(t *testing.T, err error, audience string) {
		t.Helper()
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
		require.Contains(t, status.Convert(err).Message(),
			audience+" checks owners, so an organization role reaches none of its owned data on a person's own session")
	}
	// A parent at records, which checks no owner, carrying an owned kind of
	// each module that checks owners and the model's shared profiles.
	parent, err := start("records", scope("example-entries", "read"), scope("example-records", "read"),
		scope("modelservice.datasets", "read"), scope("modelservice.profiles", "read"))
	require.NoError(t, err)

	t.Run("StartTask", func(t *testing.T) {
		_, err := start("documents", scope("example-records", "read"))
		refusedByRule(t, err, "documents")
		_, err = start("model", scope("modelservice.profiles", "read"), scope("modelservice.datasets", "read"))
		refusedByRule(t, err, "model")

		shared, err := start("model", scope("modelservice.profiles", "invoke", "read"))
		require.NoError(t, err, "a kind the module declares shared")
		require.Equal(t, []string{"modelservice.profiles"}, carried(t, shared))
		blind, err := start("records", scope("example-entries", "read", "write"))
		require.NoError(t, err, "a module that checks no owner")
		require.Equal(t, []string{"example-entries"}, carried(t, blind))
	})

	t.Run("StartTask through an operation binding", func(t *testing.T) {
		// documents' model binding invokes the profiles and reads the datasets;
		// through a module with no roles the person holds at the model only
		// what their own session holds there.
		issued, err := contexts.StartTask(admin, &gen.StartTaskWorkContextRequest{OrgId: fixtureOrganization, TaskId: uuid.NewString(),
			Audience: "documents", OperationBindings: []string{"model"}})
		require.NoError(t, err)
		require.Equal(t, []string{"modelservice.profiles"}, carried(t, issued))
	})

	t.Run("StartRootSession", func(t *testing.T) {
		root := func(token, audience string) (*gen.IssuedWorkContext, error) {
			return contexts.StartRootSession(admin, &gen.StartRootSessionWorkContextRequest{OrgId: fixtureOrganization,
				ParentWorkContextToken: token, SessionId: uuid.NewString(), Audience: audience})
		}
		_, err := root(parent.GetToken(), "documents")
		refusedByRule(t, err, "documents")
		_, err = root(parent.GetToken(), "model")
		refusedByRule(t, err, "model")

		profiles, err := start("records", scope("modelservice.profiles", "read"))
		require.NoError(t, err)
		_, err = root(profiles.GetToken(), "model")
		require.NoError(t, err, "a kind the module declares shared")
		_, err = root(parent.GetToken(), "org-directory")
		require.NoError(t, err, "a module that checks no owner")
	})

	t.Run("ExchangeAudience", func(t *testing.T) {
		exchange := func(audience string, scopes ...*gen.WorkContextScope) (*gen.IssuedWorkContext, error) {
			return contexts.ExchangeAudience(admin, &gen.ExchangeWorkContextAudienceRequest{OrgId: fixtureOrganization,
				ParentWorkContextToken: parent.GetToken(), Audience: audience, AttenuatedScopes: scopes})
		}
		_, err := exchange("documents", scope("example-records", "read"))
		refusedByRule(t, err, "documents")
		_, err = exchange("model", scope("modelservice.datasets", "read"))
		refusedByRule(t, err, "model")

		shared, err := exchange("model", scope("modelservice.profiles", "read"))
		require.NoError(t, err, "a kind the module declares shared")
		require.Equal(t, []string{"modelservice.profiles"}, carried(t, shared))
		blind, err := exchange("org-directory", scope("example-entries", "read"), scope("example-records", "read"),
			scope("modelservice.datasets", "read"), scope("modelservice.profiles", "read"))
		require.NoError(t, err, "a module that checks no owner")
		require.ElementsMatch(t, []string{"example-entries", "example-records", "modelservice.datasets", "modelservice.profiles"}, carried(t, blind))
	})
}
