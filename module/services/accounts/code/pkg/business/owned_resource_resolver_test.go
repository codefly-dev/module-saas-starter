package business

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

type ownedResourceFakeStore struct {
	invitationOrg map[string]string
	subscriptions map[string]*WebhookSubscription
	deliveries    map[string]*WebhookDelivery
	err           error
}

func (f *ownedResourceFakeStore) GetInvitationOrgID(_ context.Context, id string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.invitationOrg[id], nil
}

func (f *ownedResourceFakeStore) GetWebhookSubscription(_ context.Context, id string) (*WebhookSubscription, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.subscriptions[id], nil
}

func (f *ownedResourceFakeStore) GetWebhookDelivery(_ context.Context, id string) (*WebhookDelivery, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.deliveries[id], nil
}

func ownedResourcePolicy(t *testing.T, fullMethod string) RPCPolicy {
	t.Helper()
	policy, ok := LookupRPCPolicy(fullMethod)
	require.Truef(t, ok, "%s is not in the policy catalog", fullMethod)
	require.Truef(t, PolicyBindsOwnedResource(policy), "%s does not bind an owned resource", fullMethod)
	return policy
}

func fullyResolvingStore() *ownedResourceFakeStore {
	return &ownedResourceFakeStore{
		invitationOrg: map[string]string{"inv-1": "org-inv"},
		subscriptions: map[string]*WebhookSubscription{"sub-1": {ID: "sub-1", OrgID: "org-sub"}},
		deliveries:    map[string]*WebhookDelivery{"del-1": {ID: "del-1", SubscriptionID: "sub-1"}},
	}
}

// Each owned-resource method resolves its owning org from the field the policy
// binds, using the kind-specific store lookup: invitation id, subscription id
// (directly or via a delivery's subscription), and the "subscription_id" field
// on ListDeliveries rather than "id".
func TestResolveOwnedResourceOrgPerKind(t *testing.T) {
	store := fullyResolvingStore()
	cases := []struct {
		method string
		req    proto.Message
		want   string
	}{
		{"/saas.accounts.v1.InvitationService/RevokeInvitation", &gen.RevokeInvitationRequest{Id: "inv-1"}, "org-inv"},
		{"/saas.accounts.v1.InvitationService/ResendInvitation", &gen.ResendInvitationRequest{Id: "inv-1"}, "org-inv"},
		{"/saas.accounts.v1.WebhookService/DeleteSubscription", &gen.DeleteWebhookSubscriptionRequest{Id: "sub-1"}, "org-sub"},
		{"/saas.accounts.v1.WebhookService/RotateSecret", &gen.RotateWebhookSecretRequest{Id: "sub-1"}, "org-sub"},
		{"/saas.accounts.v1.WebhookService/TestWebhook", &gen.TestWebhookRequest{Id: "sub-1"}, "org-sub"},
		{"/saas.accounts.v1.WebhookService/GetDelivery", &gen.GetWebhookDeliveryRequest{Id: "del-1"}, "org-sub"},
		{"/saas.accounts.v1.WebhookService/ReplayDelivery", &gen.ReplayWebhookDeliveryRequest{Id: "del-1"}, "org-sub"},
		{"/saas.accounts.v1.WebhookService/ListDeliveries", &gen.ListWebhookDeliveriesRequest{SubscriptionId: "sub-1"}, "org-sub"},
	}
	for _, tc := range cases {
		policy := ownedResourcePolicy(t, tc.method)
		orgID, ok := ResolveOwnedResourceOrg(context.Background(), store, policy, tc.req)
		require.Truef(t, ok, "%s should resolve", tc.method)
		require.Equalf(t, tc.want, orgID, "owning org for %s", tc.method)
	}
}

// Resolution fails closed: an unknown resource id (no store row), an empty
// bound field, and a store error each return ok=false so the caller denies.
func TestResolveOwnedResourceOrgFailsClosed(t *testing.T) {
	policy := ownedResourcePolicy(t, "/saas.accounts.v1.InvitationService/RevokeInvitation")

	_, ok := ResolveOwnedResourceOrg(context.Background(), fullyResolvingStore(), policy, &gen.RevokeInvitationRequest{Id: "missing"})
	require.False(t, ok, "unknown invitation must not resolve")

	_, ok = ResolveOwnedResourceOrg(context.Background(), fullyResolvingStore(), policy, &gen.RevokeInvitationRequest{})
	require.False(t, ok, "empty resource id must not resolve")

	failing := &ownedResourceFakeStore{err: errors.New("db down")}
	_, ok = ResolveOwnedResourceOrg(context.Background(), failing, policy, &gen.RevokeInvitationRequest{Id: "inv-1"})
	require.False(t, ok, "a lookup error must fail closed")
}

// A delivery whose subscription no longer exists cannot resolve an org even
// though the delivery row itself is found.
func TestResolveOwnedResourceOrgDanglingDelivery(t *testing.T) {
	store := &ownedResourceFakeStore{
		deliveries: map[string]*WebhookDelivery{"del-1": {ID: "del-1", SubscriptionID: "sub-gone"}},
	}
	policy := ownedResourcePolicy(t, "/saas.accounts.v1.WebhookService/GetDelivery")
	_, ok := ResolveOwnedResourceOrg(context.Background(), store, policy, &gen.GetWebhookDeliveryRequest{Id: "del-1"})
	require.False(t, ok, "a delivery with no live subscription must fail closed")
}

// Every method the registry claims to resolve is a real catalog method that
// binds an owned resource, and no owned-resource catalog method is left without
// a resolver — otherwise a method would silently stay unsupported.
func TestOwnedResourceRegistryMatchesCatalog(t *testing.T) {
	for method := range ownedResourceResolvers {
		policy, ok := LookupRPCPolicy(method)
		require.Truef(t, ok, "%s is registered but absent from the catalog", method)
		require.Truef(t, PolicyBindsOwnedResource(policy), "%s is registered but binds no owned resource", method)
	}
	for _, policy := range RPCPolicies() {
		if !policy.Tier.Valid() || policy.PolicyError != "" {
			continue
		}
		if PolicyBindsOwnedResource(policy) {
			require.Truef(t, ownedResourceResolvable(policy.FullMethod),
				"%s binds an owned resource but has no resolver", policy.FullMethod)
		}
	}
}
