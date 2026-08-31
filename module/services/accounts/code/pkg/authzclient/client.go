// Package authzclient is the small, documented client for the accounts
// authorization oracle — PermissionService.CheckPermission.
//
// Two ways exist to learn a caller's authority (see AUTHZ.md):
//
//   - The X-Scoped-Roles request header, forwarded by the auth-gateway from
//     the access token's `sr` claim. Zero round-trips, but only as fresh as
//     the token (bounded by one refresh cycle; role edits revoke sessions).
//   - This client, for callers that need an authoritative, current answer:
//     long-lived jobs whose token predates a role change, or high-sensitivity
//     operations that must not act on a stale claim.
//
// CheckPermission is an EXPOSURE_INTERNAL method: it is served only on the
// internal transport and requires a service credential (the shared
// X-Codefly-Internal-Token secret). A caller without it is refused before the
// handler runs — this client attaches it to every call.
package authzclient

import (
	"context"
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// InternalTokenHeader is the metadata key carrying the service credential that
// authorizes an EXPOSURE_INTERNAL call. It matches CODEFLY_INTERNAL_TOKEN as
// installed on the accounts api.
const InternalTokenHeader = "x-codefly-internal-token"

// Client calls the accounts authorization oracle with a service credential.
// Construct it once against a connection to the accounts internal endpoint and
// share it; it is safe for concurrent use.
type Client struct {
	perms      gen.PermissionServiceClient
	credential string
}

// New builds a Client over conn, attaching serviceCredential to every call.
// serviceCredential is the deployment's CODEFLY_INTERNAL_TOKEN.
func New(conn grpc.ClientConnInterface, serviceCredential string) *Client {
	return &Client{
		perms:      gen.NewPermissionServiceClient(conn),
		credential: serviceCredential,
	}
}

// CheckPermission reports whether the subject may perform action on resource in
// org, optionally narrowed to scope (set req.Scope; empty means "any scope").
// It returns the authoritative, current decision from accounts — unlike the
// X-Scoped-Roles header, it reflects role edits made since the caller's token
// was minted.
func (c *Client) CheckPermission(
	ctx context.Context,
	req *gen.CheckPermissionRequest,
	opts ...grpc.CallOption,
) (*gen.CheckPermissionResponse, error) {
	if c.credential == "" {
		return nil, errors.New("authzclient: service credential is required")
	}
	ctx = metadata.AppendToOutgoingContext(ctx, InternalTokenHeader, c.credential)
	return c.perms.CheckPermission(ctx, req, opts...)
}
