package adapters

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"api/pkg/business"
)

// rpcQuotaMap maps gRPC method suffixes to the entitlement feature they consume.
var rpcQuotaMap = map[string]string{
	"APIKeyService/CreateAPIKey":                     "api_keys",
	"InvitationService/CreateInvitation":             "seats",
	"TeamService/CreateTeam":                         "teams",
	"WebhookService/CreateSubscription":              "webhooks",
	"OrganizationService/CreateOrganization":         "organizations",
}

// QuotaInterceptor is a gRPC unary interceptor that checks org entitlements
// before allowing the request to proceed.
func QuotaInterceptor(checker business.EntitlementChecker) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		if checker == nil {
			return handler(ctx, req)
		}

		// Check if this RPC requires a quota check
		feature := matchRPCFeature(info.FullMethod)
		if feature == "" {
			return handler(ctx, req)
		}

		// Extract org_id from metadata (x-org-id header set by gateway)
		orgID := extractOrgID(ctx)
		if orgID == "" {
			// No org context — pass through (unauthenticated or org-less calls)
			return handler(ctx, req)
		}

		ok, err := checker.CheckQuota(ctx, orgID, feature)
		if err != nil {
			// Don't block requests on entitlement check failures
			return handler(ctx, req)
		}
		if !ok {
			return nil, status.Errorf(codes.ResourceExhausted, "quota exceeded: %s", feature)
		}

		return handler(ctx, req)
	}
}

// matchRPCFeature returns the entitlement feature for the given gRPC method,
// or "" if no quota check is needed.
func matchRPCFeature(fullMethod string) string {
	for suffix, feature := range rpcQuotaMap {
		if strings.HasSuffix(fullMethod, suffix) {
			return feature
		}
	}
	return ""
}

// extractOrgID reads the org ID from gRPC metadata headers.
func extractOrgID(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get("x-org-id")
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}
