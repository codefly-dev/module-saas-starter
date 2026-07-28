package adapters

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
	"accounts/pkg/billing"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

const (
	billingUserID = "019f6bf7-5b1c-730d-9687-fe6d4aff31ed"
	billingOrgID  = "019f6bf7-5b4b-74e5-8c17-092259bb1661"
)

type billingAuthorizationStore struct {
	business.Store
	members           []*gen.OrgMembership
	billingPermission bool
	platformRole      string
	customerID        string
	plan              *business.PlanFull
	publicPlans       []business.PublicPlan
}

func (f *billingAuthorizationStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (f *billingAuthorizationStore) GetOrgMembership(_ context.Context, _ string, userID string) (*gen.OrgMembership, error) {
	for _, member := range f.members {
		if member.UserId == userID {
			return member, nil
		}
	}
	return nil, nil
}

func (f *billingAuthorizationStore) GetPlatformRole(context.Context, string) (string, error) {
	return f.platformRole, nil
}

func (f *billingAuthorizationStore) CheckPermission(context.Context, string, gen.SubjectKind, string, string, string, string) (bool, string, error) {
	return f.billingPermission, "test policy", nil
}

func (f *billingAuthorizationStore) GetOrgStripeCustomerID(context.Context, string) (string, error) {
	return f.customerID, nil
}

func (f *billingAuthorizationStore) GetPlanByName(context.Context, string) (*business.PlanFull, error) {
	copy := *f.plan
	return &copy, nil
}

func (f *billingAuthorizationStore) ListPublicPlans(context.Context) ([]business.PublicPlan, error) {
	return append([]business.PublicPlan(nil), f.publicPlans...), nil
}

type billingAuthorizationClient struct {
	checkoutCalls int
	portalCalls   int
}

func (*billingAuthorizationClient) CreateCustomer(context.Context, string, string, string) (*billing.Customer, error) {
	return &billing.Customer{ID: "cus_created"}, nil
}

func (f *billingAuthorizationClient) CreateCheckoutSession(context.Context, billing.CheckoutParams) (*billing.CheckoutSession, error) {
	f.checkoutCalls++
	return &billing.CheckoutSession{ID: "cs_test", URL: "https://checkout.stripe.test/cs_test"}, nil
}

func (f *billingAuthorizationClient) CreateBillingPortalSession(context.Context, string, string, string) (*billing.BillingPortalSession, error) {
	f.portalCalls++
	return &billing.BillingPortalSession{ID: "bps_test", URL: "https://billing.stripe.test/bps_test"}, nil
}

func (*billingAuthorizationClient) ListInvoices(context.Context, string, int) ([]billing.Invoice, error) {
	return nil, nil
}

type fixedAccessMinter struct{ identity *auth.Identity }

func (*fixedAccessMinter) Mint(context.Context, *auth.Identity) (*auth.TokenPair, error) {
	return nil, nil
}

func (m *fixedAccessMinter) VerifyAccess(string) (*auth.Identity, error) { return m.identity, nil }

func (*fixedAccessMinter) VerifyRefresh(context.Context, string) (*auth.TokenPair, error) {
	return nil, nil
}

func (*fixedAccessMinter) SwitchOrganization(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (string, error) {
	return "", nil
}

func (*fixedAccessMinter) Revoke(context.Context, string) error       { return nil }
func (*fixedAccessMinter) RevokeAccess(context.Context, string) error { return nil }
func (*fixedAccessMinter) JWKS() (string, error)                      { return `{}`, nil }

func recentBillingIdentity() *auth.Identity {
	return &auth.Identity{
		UserID:                uuid.MustParse(billingUserID),
		OrgID:                 uuid.MustParse(billingOrgID),
		AuthenticationMethods: []string{auth.AuthenticationMethodOAuth, auth.AuthenticationMethodWebAuthn},
		AuthenticatedAt:       time.Now().Add(-time.Minute),
		AssuranceLevel:        auth.AssuranceLevelAAL2,
		MFAVerifiedAt:         time.Now().Add(-time.Minute),
	}
}

func installBillingAuthorizationService(t *testing.T, store *billingAuthorizationStore, client *billingAuthorizationClient) *business.Service {
	t.Helper()
	previousService := service
	previousCache := orgMembershipCache
	t.Cleanup(func() {
		service = previousService
		orgMembershipCache = previousCache
	})
	orgMembershipCache = nil

	svc, err := business.NewService(store)
	require.NoError(t, err)
	svc.SetBillingClient(client)
	svc.SetBillingRedirects(business.BillingRedirects{
		CheckoutSuccessURL: "https://app.example.com/admin/billing/success?session_id={CHECKOUT_SESSION_ID}",
		CheckoutCancelURL:  "https://app.example.com/admin/billing",
		PortalReturnURL:    "https://app.example.com/admin/billing",
	})
	WithService(svc)
	return svc
}

func TestRequireBillingAdminSupportsAdminAndDelegatedPermission(t *testing.T) {
	for _, tc := range []struct {
		name       string
		role       gen.OrgRole
		permission bool
		wantCode   connect.Code
	}{
		{name: "owner", role: gen.OrgRole_ORG_ROLE_OWNER},
		{name: "admin", role: gen.OrgRole_ORG_ROLE_ADMIN},
		{name: "delegated billing writer", role: gen.OrgRole_ORG_ROLE_MEMBER, permission: true},
		{name: "ordinary member denied", role: gen.OrgRole_ORG_ROLE_MEMBER, wantCode: connect.CodePermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &billingAuthorizationStore{
				members:           []*gen.OrgMembership{{UserId: billingUserID, Role: tc.role}},
				billingPermission: tc.permission,
			}
			installBillingAuthorizationService(t, store, &billingAuthorizationClient{})

			ctx := stampVerifiedIdentity(context.Background(), billingUserID, billingOrgID, auth.Assurance{})
			err := requireBillingAdmin(ctx, billingUserID, billingOrgID)
			if tc.wantCode == 0 {
				require.NoError(t, err)
			} else {
				require.Equal(t, tc.wantCode, connect.CodeOf(translateGRPCError(err)))
			}
		})
	}
}

func TestBillingPortalRequiresRecentMFA(t *testing.T) {
	store := &billingAuthorizationStore{
		members:    []*gen.OrgMembership{{UserId: billingUserID, Role: gen.OrgRole_ORG_ROLE_ADMIN}},
		customerID: "cus_existing",
	}
	client := &billingAuthorizationClient{}
	svc := installBillingAuthorizationService(t, store, client)
	handler := &billingConnectHandler{svc: svc}
	req := connect.NewRequest(&gen.OpenBillingPortalRequest{OrgId: billingOrgID})
	req.Header().Set("Idempotency-Key", "portal-operation")

	ctx := stampVerifiedIdentity(context.Background(), billingUserID, billingOrgID, auth.Assurance{})
	_, err := handler.OpenPortal(ctx, req)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.Zero(t, client.portalCalls)

	ctx = stampVerifiedIdentity(context.Background(), billingUserID, billingOrgID, recentBillingIdentity().Assurance())
	resp, err := handler.OpenPortal(ctx, req)
	require.NoError(t, err)
	require.Equal(t, "https://billing.stripe.test/bps_test", resp.Msg.Url)
	require.Equal(t, 1, client.portalCalls)
}

func TestListPublicPlansNeedsNoProductIdentity(t *testing.T) {
	store := &billingAuthorizationStore{publicPlans: []business.PublicPlan{{
		Key: "pro", Name: "Pro", Description: "Fixture plan", Currency: "USD",
		AmountMinor: 4900, Interval: "month", CheckoutEnabled: true,
		TrialDays: 14, TaxBehavior: "automatic", Fixture: true,
		Entitlements: []business.PlanFeatureLimit{{Feature: "seats", Limit: 50}},
	}}}
	svc := installBillingAuthorizationService(t, store, &billingAuthorizationClient{})
	handler := &billingConnectHandler{svc: svc}

	response, err := handler.ListPublicPlans(
		context.Background(),
		connect.NewRequest(&gen.ListPublicPlansRequest{}),
	)
	require.NoError(t, err)
	require.Regexp(t, `^sha256:[0-9a-f]{64}$`, response.Msg.Revision)
	require.Len(t, response.Msg.Plans, 1)
	require.Equal(t, int64(4900), response.Msg.Plans[0].AmountMinor)
	require.Equal(t, int64(50), response.Msg.Plans[0].Entitlements[0].Limit)
}

func TestBillingHTTPCheckoutRequiresPermissionRecentMFAAndIdempotency(t *testing.T) {
	store := &billingAuthorizationStore{
		members:    []*gen.OrgMembership{{UserId: billingUserID, Role: gen.OrgRole_ORG_ROLE_ADMIN}},
		customerID: "cus_existing",
		plan: &business.PlanFull{
			Plan: business.Plan{Name: "pro"}, StripePriceID: "price_pro", CheckoutEnabled: true,
		},
	}
	client := &billingAuthorizationClient{}
	svc := installBillingAuthorizationService(t, store, client)
	svc.SetJWTMinter(&fixedAccessMinter{identity: recentBillingIdentity()})
	handler := NewBillingHTTPHandler(svc)

	request := func(token, idempotencyKey string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/billing/checkout", strings.NewReader(`{"plan_name":"pro"}`))
		// Deliberately forged transport identity. A valid JWT below must win,
		// and these headers alone must never authenticate the request.
		req.Header.Set("X-User-ID", "attacker")
		req.Header.Set("X-Org-ID", "attacker-org")
		req.Header.Set("Authorization", "Bearer "+token)
		if idempotencyKey != "" {
			req.Header.Set("Idempotency-Key", idempotencyKey)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}

	unauthenticated := request("", "checkout-operation")
	require.Equal(t, http.StatusUnauthorized, unauthenticated.Code)
	require.Zero(t, client.checkoutCalls)

	staleIdentity := recentBillingIdentity()
	staleIdentity.AssuranceLevel = auth.AssuranceLevelAAL1
	staleIdentity.MFAVerifiedAt = time.Time{}
	svc.SetJWTMinter(&fixedAccessMinter{identity: staleIdentity})
	missingMFA := request("stale-token", "checkout-operation")
	require.Equal(t, http.StatusPreconditionFailed, missingMFA.Code)
	require.Zero(t, client.checkoutCalls)
	svc.SetJWTMinter(&fixedAccessMinter{identity: recentBillingIdentity()})

	missingKey := request("recent-token", "")
	require.Equal(t, http.StatusBadRequest, missingKey.Code)
	require.Zero(t, client.checkoutCalls)

	success := request("recent-token", "checkout-operation")
	require.Equal(t, http.StatusOK, success.Code)
	require.Equal(t, 1, client.checkoutCalls)
}
