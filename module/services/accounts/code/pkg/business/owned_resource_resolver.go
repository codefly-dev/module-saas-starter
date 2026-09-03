package business

import (
	"context"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	policyv1 "accounts/pkg/gen/saas/policy/v1"
)

// OwnedResourceStore is the slice of Store the ownership resolvers use to map an
// OWNED_RESOURCE request's resource id to the organization that owns it. Every
// lookup is by primary id and must run under a control-plane bypass so the true
// owner resolves regardless of the caller's RLS scope.
type OwnedResourceStore interface {
	GetInvitationOrgID(ctx context.Context, id string) (string, error)
	GetWebhookSubscription(ctx context.Context, id string) (*WebhookSubscription, error)
	GetWebhookDelivery(ctx context.Context, id string) (*WebhookDelivery, error)
}

// ownedResourceResolver maps one owned-resource id to its owning organization.
// It returns an empty org for a resource that does not exist so the caller can
// fail closed.
type ownedResourceResolver func(ctx context.Context, store OwnedResourceStore, id string) (string, error)

// ownedResourceResolvers binds each OWNED_RESOURCE method to the store lookup
// that resolves its owning org. The request field to read is taken from the
// method's resource binding; this map only selects the kind-specific lookup.
// A method absent here has no resolver and stays unsupported.
var ownedResourceResolvers = map[string]ownedResourceResolver{
	"/saas.accounts.v1.InvitationService/ResendInvitation": resolveInvitationOrg,
	"/saas.accounts.v1.InvitationService/RevokeInvitation": resolveInvitationOrg,
	"/saas.accounts.v1.WebhookService/DeleteSubscription":  resolveSubscriptionOrg,
	"/saas.accounts.v1.WebhookService/RotateSecret":        resolveSubscriptionOrg,
	"/saas.accounts.v1.WebhookService/TestWebhook":         resolveSubscriptionOrg,
	"/saas.accounts.v1.WebhookService/ListDeliveries":      resolveSubscriptionOrg,
	"/saas.accounts.v1.WebhookService/GetDelivery":         resolveDeliveryOrg,
	"/saas.accounts.v1.WebhookService/ReplayDelivery":      resolveDeliveryOrg,
}

func resolveInvitationOrg(ctx context.Context, store OwnedResourceStore, id string) (string, error) {
	return store.GetInvitationOrgID(ctx, id)
}

func resolveSubscriptionOrg(ctx context.Context, store OwnedResourceStore, id string) (string, error) {
	sub, err := store.GetWebhookSubscription(ctx, id)
	if err != nil || sub == nil {
		return "", err
	}
	return sub.OrgID, nil
}

func resolveDeliveryOrg(ctx context.Context, store OwnedResourceStore, id string) (string, error) {
	delivery, err := store.GetWebhookDelivery(ctx, id)
	if err != nil || delivery == nil {
		return "", err
	}
	return resolveSubscriptionOrg(ctx, store, delivery.SubscriptionID)
}

// ownedResourceResolvable reports whether an OWNED_RESOURCE binding on this
// method can be resolved to an owning org. It is what lets the coverage
// classifier distinguish a resolvable owned-resource method from one that is
// genuinely unsupported.
func ownedResourceResolvable(fullMethod string) bool {
	_, ok := ownedResourceResolvers[fullMethod]
	return ok
}

// policyBindsOwnedResource reports whether the policy carries an OWNED_RESOURCE
// binding, i.e. a method whose owning org must be resolved from a resource id
// before the central floor can be evaluated.
func policyBindsOwnedResource(policy RPCPolicy) bool {
	return ownedResourceBinding(policy.MethodPolicy) != nil
}

func ownedResourceBinding(p *policyv1.MethodPolicy) *policyv1.ResourceBinding {
	for _, binding := range p.GetResourceBindings() {
		if binding.GetTarget() == policyv1.ResourceTarget_RESOURCE_TARGET_OWNED_RESOURCE {
			return binding
		}
	}
	return nil
}

// ResolveOwnedResourceOrg maps an OWNED_RESOURCE request to the organization
// that owns the resource it names. It reads the resource id from the request
// field the policy binds, then runs the kind-specific store lookup. It fails
// closed: a method with no resolver, a missing or empty resource id, a lookup
// error, or a resource that resolves to no org all return ok=false.
func ResolveOwnedResourceOrg(ctx context.Context, store OwnedResourceStore, policy RPCPolicy, req proto.Message) (string, bool) {
	resolver, ok := ownedResourceResolvers[policy.FullMethod]
	if !ok {
		return "", false
	}
	binding := ownedResourceBinding(policy.MethodPolicy)
	if binding == nil {
		return "", false
	}
	id := stringFieldValue(req, binding.GetRequestField())
	if id == "" {
		return "", false
	}
	orgID, err := resolver(ctx, store, id)
	if err != nil || orgID == "" {
		return "", false
	}
	return orgID, true
}

// stringFieldValue reads the string value at a dotted protobuf field path,
// returning "" when any segment is missing or not a message on the way down.
func stringFieldValue(req proto.Message, fieldPath string) string {
	if req == nil || fieldPath == "" {
		return ""
	}
	message := req.ProtoReflect()
	parts := strings.Split(fieldPath, ".")
	for i, part := range parts {
		field := message.Descriptor().Fields().ByName(protoreflect.Name(part))
		if field == nil {
			return ""
		}
		value := message.Get(field)
		if i == len(parts)-1 {
			if field.Kind() != protoreflect.StringKind {
				return ""
			}
			return value.String()
		}
		if field.Message() == nil {
			return ""
		}
		message = value.Message()
	}
	return ""
}
