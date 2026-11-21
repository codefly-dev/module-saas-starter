package adapters

import (
	"context"
	"errors"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Webhooks and data sources are served by Connect-native handlers, which
// pkg/business's access matrix cannot reach. This is the same matrix for
// them: admission's central floor and then the real handler, called by Dana
// (an administrator of Acme), Carol (a member of Acme), Bob (an administrator
// of another organization, Globex), the platform administrator (a member of
// neither) and a signed-out visitor.
//
// The store answers who is a member of what and nothing else. A refusal is
// therefore the real one, returned before any row is read; an admitted call
// is one that got past every gate and reached for the domain.

const (
	connectMatrixAcme   = "019f6bf7-5b4b-74e5-8c17-092259bb1661"
	connectMatrixGlobex = "019f6bf7-5b4b-74e5-8c17-092259bb1662"
)

type connectMatrixPerson struct {
	name, id, sessionOrg string
}

var (
	connectMatrixOperator = connectMatrixPerson{"the platform administrator", "019f6bf7-5b1c-730d-9687-fe6d4aff3101", connectMatrixGlobex}
	connectMatrixDana     = connectMatrixPerson{"Dana", "019f6bf7-5b1c-730d-9687-fe6d4aff3102", connectMatrixAcme}
	connectMatrixCarol    = connectMatrixPerson{"Carol", "019f6bf7-5b1c-730d-9687-fe6d4aff3103", connectMatrixAcme}
	connectMatrixBob      = connectMatrixPerson{"Bob", "019f6bf7-5b1c-730d-9687-fe6d4aff3104", connectMatrixGlobex}
	connectMatrixVisitor  = connectMatrixPerson{name: "a signed-out visitor"}
)

// connectMatrixStore is the two organizations' membership, and a record of
// which organizations a call asked about.
type connectMatrixStore struct {
	*permissionStore
	asked map[string]bool
}

func newConnectMatrixStore() *connectMatrixStore {
	return &connectMatrixStore{
		permissionStore: &permissionStore{
			enforceStore: &enforceStore{members: map[string][]*gen.OrgMembership{
				connectMatrixAcme: {
					{UserId: connectMatrixDana.id, Role: gen.OrgRole_ORG_ROLE_ADMIN},
					{UserId: connectMatrixCarol.id, Role: gen.OrgRole_ORG_ROLE_MEMBER},
				},
				connectMatrixGlobex: {{UserId: connectMatrixBob.id, Role: gen.OrgRole_ORG_ROLE_ADMIN}},
			}},
			granted: map[string]bool{},
		},
		asked: map[string]bool{},
	}
}

func (s *connectMatrixStore) GetOrgMembership(ctx context.Context, orgID, userID string) (*gen.OrgMembership, error) {
	s.asked[orgID] = true
	return s.permissionStore.GetOrgMembership(ctx, orgID, userID)
}

func (s *connectMatrixStore) GetPlatformRole(_ context.Context, userID string) (string, error) {
	if userID == connectMatrixOperator.id {
		return "super_admin", nil
	}
	return "", nil
}

type connectMatrixOutcome int

const (
	connectMatrixAdmitted connectMatrixOutcome = iota
	connectMatrixRefused
	connectMatrixUnauthenticated
)

// callConnectMatrix runs admission's central floor and then the handler, as
// the transport does, and classifies what came back.
func callConnectMatrix(t *testing.T, person connectMatrixPerson, procedure string, handler func(context.Context) error) (outcome connectMatrixOutcome, err error) {
	t.Helper()
	ctx := context.Background()
	if person.id != "" {
		ctx = withCredentialKind(stampVerifiedIdentity(ctx, person.id, person.sessionOrg, auth.Assurance{}), credentialKindSession)
	}
	classify := func(err error) connectMatrixOutcome {
		code := status.Code(err)
		var connectErr *connect.Error
		if errors.As(err, &connectErr) {
			code = codes.Code(connectErr.Code())
		}
		switch code {
		case codes.PermissionDenied:
			return connectMatrixRefused
		case codes.Unauthenticated:
			return connectMatrixUnauthenticated
		}
		return connectMatrixAdmitted
	}
	if err := enforceCentralPolicy(ctx, procedure); err != nil {
		return classify(err), err
	}
	defer func() {
		// The store holds no rows: reaching for one is the proof the call was
		// admitted past every gate.
		if recovered := recover(); recovered != nil {
			outcome, err = connectMatrixAdmitted, nil
		}
	}()
	err = handler(ctx)
	return classify(err), err
}

func TestConnectAccessMatrix(t *testing.T) {
	webhooks := &webhookConnectHandler{}
	sources := &datasourceConnectHandler{}
	const prefix = "/saas.accounts.v1."

	rows := []struct {
		operation, procedure, decidedBy string
		// sessionScoped: the request names no organization, so the call acts
		// in the caller's own and can never reach another.
		sessionScoped bool
		admits        []connectMatrixPerson
		call          func(context.Context) error
	}{
		{operation: "create a webhook subscription in Acme", procedure: "WebhookService/CreateSubscription",
			decidedBy: "admission: webhooks:write; webhookConnectHandler.CreateSubscription: requireOrgAdmin",
			admits:    []connectMatrixPerson{connectMatrixDana},
			call: func(ctx context.Context) error {
				_, err := webhooks.CreateSubscription(ctx, connect.NewRequest(&gen.CreateWebhookSubscriptionRequest{
					OrgId: connectMatrixAcme, Url: "https://hooks.example.com/acme", Events: []string{"saas.user.created"},
				}))
				return err
			}},
		{operation: "list Acme's webhook subscriptions", procedure: "WebhookService/ListSubscriptions",
			decidedBy: "admission: webhooks:read; webhookConnectHandler.ListSubscriptions: requireOrgMember",
			admits:    []connectMatrixPerson{connectMatrixDana},
			call: func(ctx context.Context) error {
				_, err := webhooks.ListSubscriptions(ctx, connect.NewRequest(&gen.ListWebhookSubscriptionsRequest{OrgId: connectMatrixAcme}))
				return err
			}},
		{operation: "delete a webhook subscription", procedure: "WebhookService/DeleteSubscription", sessionScoped: true,
			decidedBy: "admission: webhooks:write; webhookConnectHandler.DeleteSubscription: requireOrgAdmin(callerOrg)",
			admits:    []connectMatrixPerson{connectMatrixDana, connectMatrixBob},
			call: func(ctx context.Context) error {
				_, err := webhooks.DeleteSubscription(ctx, connect.NewRequest(&gen.DeleteWebhookSubscriptionRequest{Id: "019f6bf7-5b1c-730d-9687-fe6d4aff3201"}))
				return err
			}},
		{operation: "list webhook deliveries", procedure: "WebhookService/ListDeliveries", sessionScoped: true,
			decidedBy: "admission: webhooks:read; webhookConnectHandler.ListDeliveries: requireOrgMember(callerOrg)",
			admits:    []connectMatrixPerson{connectMatrixDana, connectMatrixBob},
			call: func(ctx context.Context) error {
				_, err := webhooks.ListDeliveries(ctx, connect.NewRequest(&gen.ListWebhookDeliveriesRequest{SubscriptionId: "019f6bf7-5b1c-730d-9687-fe6d4aff3201"}))
				return err
			}},
		{operation: "list Acme's data sources", procedure: "DatasourceService/ListSources",
			decidedBy: "datasourceConnectHandler.ListSources: requireOrgMember",
			admits:    []connectMatrixPerson{connectMatrixDana, connectMatrixCarol},
			call: func(ctx context.Context) error {
				_, err := sources.ListSources(ctx, connect.NewRequest(&gen.ListSourcesRequest{OrgId: connectMatrixAcme}))
				return err
			}},
		{operation: "read Acme's data source directory", procedure: "DatasourceService/GetDatasourceDirectory",
			decidedBy: "datasourceConnectHandler.GetDatasourceDirectory: requireOrgAdmin",
			admits:    []connectMatrixPerson{connectMatrixDana},
			call: func(ctx context.Context) error {
				_, err := sources.GetDatasourceDirectory(ctx, connect.NewRequest(&gen.GetDatasourceDirectoryRequest{OrgId: connectMatrixAcme}))
				return err
			}},
		{operation: "bind a directory group to a data source in Acme", procedure: "DatasourceService/BindDatasourceGroup",
			decidedBy: "datasourceConnectHandler.BindDatasourceGroup: requireOrgAdmin",
			admits:    []connectMatrixPerson{connectMatrixDana},
			call: func(ctx context.Context) error {
				_, err := sources.BindDatasourceGroup(ctx, connect.NewRequest(&gen.BindDatasourceGroupRequest{OrgId: connectMatrixAcme}))
				return err
			}},
		{operation: "set up the GitHub App for Acme", procedure: "DatasourceService/BeginGitHubAppSetup",
			decidedBy: "datasourceConnectHandler.BeginGitHubAppSetup: requireOrgAdmin",
			admits:    []connectMatrixPerson{connectMatrixDana},
			call: func(ctx context.Context) error {
				_, err := sources.BeginGitHubAppSetup(ctx, connect.NewRequest(&gen.BeginGitHubAppSetupRequest{OrgId: connectMatrixAcme}))
				return err
			}},
	}

	people := []connectMatrixPerson{connectMatrixOperator, connectMatrixDana, connectMatrixCarol, connectMatrixBob, connectMatrixVisitor}
	for _, row := range rows {
		t.Run(row.operation, func(t *testing.T) {
			_, classified := business.LookupRPCPolicy(prefix + row.procedure)
			require.True(t, classified, "the row names a declared procedure")
			for _, person := range people {
				store := newConnectMatrixStore()
				installEnforceService(t, store)
				outcome, err := callConnectMatrix(t, person, prefix+row.procedure, row.call)

				want := connectMatrixRefused
				for _, admitted := range row.admits {
					if admitted == person {
						want = connectMatrixAdmitted
					}
				}
				if person == connectMatrixVisitor {
					want = connectMatrixUnauthenticated
				}
				require.Equalf(t, want, outcome, "%s: %s (%s): %v", row.operation, person.name, row.decidedBy, err)
				if person.sessionOrg == connectMatrixGlobex {
					require.Falsef(t, outcome == connectMatrixAdmitted && store.asked[connectMatrixAcme],
						"%s: %s was admitted on a decision about Acme", row.operation, person.name)
					if row.sessionScoped {
						require.Falsef(t, store.asked[connectMatrixAcme], "%s: %s's call was decided in Acme", row.operation, person.name)
					}
				}
			}
		})
	}
}
