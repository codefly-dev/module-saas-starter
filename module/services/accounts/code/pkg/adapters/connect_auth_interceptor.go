package adapters

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"

	"accounts/pkg/auth"
	"accounts/pkg/business"

	"github.com/codefly-dev/core/wool"
)

// forwardedIdentityHeaders are caller-controlled identity headers. Unless the
// API is explicitly configured behind the trusted auth sidecar, remove them
// before policy admission and handler execution.
var forwardedIdentityHeaders = []string{
	"X-User-Id", "X-Org-Id", "X-Org-Role", "X-Platform-Role", "X-Roles",
	"X-Scoped-Roles", "X-Scoped-Roles-Truncated", "X-Auth-Id", "X-User-Email", "X-User-Name", "X-Session-Id",
	"X-Acting-As-User-Id", "X-Act", "X-Scopes", "X-Credential-Kind", "X-MFA-Satisfied",
	"X-Authentication-Methods", "X-Auth-Time", "X-Assurance-Level", "X-MFA-Verified-At",
	"X-Client-Id",
	// Which registered solution a Work Context mint is for, and the publisher
	// its credential named (issue #1015). Neither names a person, but together
	// they select the runtime boundary the capability is sealed under, so they
	// are identity in every sense that matters here: left caller-settable, one
	// solution could mint under another's boundary and read its runs. They
	// belong in this list for the same reason X-Org-Id does — stripped unless
	// the gateway token is valid, refused if either arrives twice.
	"X-Codefly-Solution-Id",
	"X-Codefly-Solution-Publisher",
}

const publicOriginHeader = "X-Codefly-Public-Origin"

// errSolutionAttestationNotDelivered refuses a request that asserts a solution
// identity. Nothing on this host can prove such a claim: the gateway stamps
// neither header, and the attestation that would replace the deleted
// registration credential is not delivered yet. Named rather than generic so a
// composed solution's runtime reads why it cannot mint, instead of discovering
// that it silently received a capability for something else.
var errSolutionAttestationNotDelivered = errors.New(
	"solution attestation is not delivered on this host: a solution-scoped Work Context requires the delivered workload-certificate attestation at the mint hop, which is blocked on the core workcontext cutover (the Seals/Revisions sources both entrypoints require, and the JSON-versus-deterministic-protobuf token encoding)",
)

// solutionIdentityAsserted reports whether a request claims a solution identity
// at all. Either header alone is a claim: the publisher without the id still
// asserts something this host cannot prove, and refusing only the pair would
// make the refusal depend on how completely a caller lied.
func solutionIdentityAsserted(headers http.Header) bool {
	return headers.Get(solutionIdentityHeader) != "" || headers.Get(solutionPublisherHeader) != ""
}

// restIdentityHeaderMatcher forwards the caller's bearer credential and the
// canonical identity headers across the REST transcoding hop into gRPC
// metadata, so a request that arrives over REST reaches this interceptor
// carrying the same fields a direct Connect or gRPC request carries. Without it
// the hop keeps only the six wool header mappings, which drops the acting-as
// subject, the session and the assurance evidence — REST would authorize a
// support session as the actor instead of as the target.
//
// Forwarding is not trust: everything named here is still stripped before
// admission unless the request also carried a valid gateway token, exactly as on
// the direct transports.
//
// Identity crosses only under its own name. grpc-gateway's default matcher also
// forwards any Grpc-Metadata-<name> header as metadata <name>, which would let a
// caller add a second x-user-id (or x-org-id, x-codefly-public-origin, ...)
// beside the one the gateway stamped, on a request that genuinely carries the
// gateway token — and the value the interceptor reads first would then be
// whichever the transcoder happened to place first. Nothing on this REST
// surface is meant to receive caller-chosen metadata, so every prefixed
// spelling is declined, not only the identity names.
func restIdentityHeaderMatcher(header string) (string, bool) {
	if hasGRPCMetadataPrefix(header) {
		return "", false
	}
	if strings.EqualFold(header, "Authorization") {
		return "authorization", true
	}
	for _, forwarded := range forwardedIdentityHeaders {
		if strings.EqualFold(header, forwarded) {
			return strings.ToLower(forwarded), true
		}
	}
	return runtime.DefaultHeaderMatcher(header)
}

func hasGRPCMetadataPrefix(header string) bool {
	prefix := runtime.MetadataHeaderPrefix
	return len(header) >= len(prefix) && strings.EqualFold(header[:len(prefix)], prefix)
}

// forwardedIdentityAmbiguous reports whether a trusted forwarder's assertion
// names an identity field, or the verified public origin, more than once. The
// gateway stamps exactly one value per field; a second one reached the request
// some other way, and reading either index would let that path choose the
// identity. values looks a field up by its header name on either transport.
func forwardedIdentityAmbiguous(values func(name string) []string) bool {
	for _, header := range forwardedIdentityHeaders {
		if len(values(header)) > 1 {
			return true
		}
	}
	return len(values(publicOriginHeader)) > 1
}

// forwardedCredentialIncomplete reports whether a trusted forwarder's
// assertion lacks the credential kind or the scope list. The gateway stamps
// both on every request it admits: X-Credential-Kind as `session` or
// `api_key`, and X-Scopes as the key's scope list — empty for a session and for
// a key created without scopes. An assertion without either lost part of itself
// on the way (a caller's `Connection: X-Scopes` once did exactly that at the
// gateway): with no kind, accounts cannot tell a key from a session and so
// cannot hold the key to its scopes; with no scope list, it cannot bound the key
// at all. A kind the gateway never stamps is refused for the same reason.
// forwardedIdentityAmbiguous has already refused a second value of either.
// values looks a field up by its header name on any transport.
func forwardedCredentialIncomplete(values func(name string) []string) bool {
	kinds := values("X-Credential-Kind")
	if len(kinds) != 1 || (kinds[0] != credentialKindSession && kinds[0] != credentialKindAPIKey) {
		return true
	}
	return len(values("X-Scopes")) != 1
}

// errForwardedCredentialIncomplete is returned by the forwarded-identity
// projections when forwardedCredentialIncomplete holds.
var errForwardedCredentialIncomplete = errors.New("forwarded identity carries no credential kind or no scopes header")

// singleValidGatewayToken is the trust test both transports share: exactly one
// gateway credential, and it matches. Two are not one trusted assertion.
func singleValidGatewayToken(values []string) bool {
	return len(values) == 1 && validGatewayToken(values[0])
}

type connectPolicyInterceptor struct {
	getMinter func() auth.JWTMinter
}

// connectAuthInterceptor enforces the descriptor-derived policy for unary and
// streaming Connect handlers. Unknown methods are denied, public methods pass,
// internal methods are denied because Connect is tenant-facing, and every
// other tier requires a verified user identity.
func connectAuthInterceptor(getMinter func() auth.JWTMinter) connect.Interceptor {
	return &connectPolicyInterceptor{
		getMinter: getMinter,
	}
}

func (i *connectPolicyInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx, err := i.authorize(ctx, req.Spec().Procedure, req.Header())
		if err != nil {
			return nil, err
		}
		if err := enforceImpersonationPolicy(ctx, req.Spec().Procedure); err != nil {
			return nil, translateGRPCError(err)
		}
		if err := enforceAPIKeyScopePolicy(ctx, req.Spec().Procedure); err != nil {
			return nil, translateGRPCError(err)
		}
		if err := enforceCentralPolicy(ctx, req.Spec().Procedure); err != nil {
			return nil, translateGRPCError(err)
		}
		// The policy log's serving gate. After authorization, so the
		// refusal reaches only callers this host would have served.
		if err := enforcePolicyLogServing(ctx); err != nil {
			return nil, translateGRPCError(err)
		}
		shadowPolicyCoverage(ctx, req.Spec().Procedure)
		return next(ctx, req)
	}
}

func (i *connectPolicyInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *connectPolicyInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx, err := i.authorize(ctx, conn.Spec().Procedure, conn.RequestHeader())
		if err != nil {
			return err
		}
		if err := enforceImpersonationPolicy(ctx, conn.Spec().Procedure); err != nil {
			return translateGRPCError(err)
		}
		if err := enforceAPIKeyScopePolicy(ctx, conn.Spec().Procedure); err != nil {
			return translateGRPCError(err)
		}
		if err := enforceCentralPolicy(ctx, conn.Spec().Procedure); err != nil {
			return translateGRPCError(err)
		}
		if err := enforcePolicyLogServing(ctx); err != nil {
			return translateGRPCError(err)
		}
		shadowPolicyCoverage(ctx, conn.Spec().Procedure)
		return next(ctx, conn)
	}
}

func (i *connectPolicyInterceptor) authorize(ctx context.Context, procedure string, headers http.Header) (context.Context, error) {
	trustedForwarded := singleValidGatewayToken(headers.Values("X-Codefly-Gateway-Token"))
	if trustedForwarded && forwardedIdentityAmbiguous(headers.Values) {
		return ctx, connect.NewError(connect.CodePermissionDenied, errors.New("forwarded identity is ambiguous"))
	}
	// Refused here, and not inside stampForwardedHTTPIdentity, for two reasons:
	// that function's caller rewrites every error into "forwarded identity is
	// malformed", which would bury the one thing a miswired runtime needs to
	// read; and it only runs when X-User-Id is also present, while this claim
	// must be refused however little else arrives with it.
	//
	// Scoped to a trusted forward because that is the only way it can arrive
	// and be believed: without a valid gateway token these headers are deleted
	// a few lines below, which is the right answer for an anonymous prober.
	if trustedForwarded && solutionIdentityAsserted(headers) {
		return ctx, connect.NewError(connect.CodePermissionDenied, errSolutionAttestationNotDelivered)
	}
	forwardedPublicOrigin := headers.Get(publicOriginHeader)
	if !trustedForwarded {
		for _, header := range forwardedIdentityHeaders {
			headers.Del(header)
		}
	}
	headers.Del("X-Codefly-Gateway-Token")
	headers.Del(publicOriginHeader)
	if trustedForwarded && forwardedPublicOrigin != "" {
		var err error
		ctx, err = auth.WithVerifiedPublicOrigin(ctx, forwardedPublicOrigin)
		if err != nil {
			return ctx, connect.NewError(connect.CodePermissionDenied, errors.New("invalid trusted public origin"))
		}
	}

	policy, classified := business.LookupRPCPolicy(procedure)
	if !classified {
		return ctx, connect.NewError(connect.CodePermissionDenied, errors.New("RPC is not classified by the authorization policy"))
	}
	if policy.Tier == business.RPCPolicyPublic {
		return ctx, nil
	}
	if policy.Tier == business.RPCPolicyInternal {
		return ctx, connect.NewError(connect.CodePermissionDenied, errors.New("internal RPC is not exposed on the tenant listener"))
	}

	if trustedForwarded && headers.Get("X-User-Id") != "" {
		forwarded, err := stampForwardedHTTPIdentity(ctx, headers)
		if err != nil {
			return ctx, connect.NewError(connect.CodePermissionDenied, errors.New("forwarded identity is malformed"))
		}
		return forwarded, nil
	}
	var minter auth.JWTMinter
	if i.getMinter != nil {
		minter = i.getMinter()
	}
	if minter == nil {
		return ctx, connect.NewError(connect.CodeUnauthenticated, errors.New("authentication required"))
	}
	authz := headers.Get("Authorization")
	token, ok := strings.CutPrefix(authz, "Bearer ")
	if !ok || token == "" {
		return ctx, connect.NewError(connect.CodeUnauthenticated, errors.New("authentication required"))
	}
	identity, err := minter.VerifyAccess(token)
	if err != nil {
		if errors.Is(err, auth.ErrRevocationUnavailable) {
			wool.Get(ctx).In("connectPolicyInterceptor").Warn("revocation list unavailable, denying (fail-closed)", wool.ErrField(err))
			return ctx, connect.NewError(connect.CodeUnavailable, errors.New("authorization temporarily unavailable"))
		}
		wool.Get(ctx).In("connectPolicyInterceptor").Debug("VerifyAccess failed", wool.ErrField(err))
		return ctx, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid or expired access token"))
	}

	ctx = stampRequestIdentity(ctx, auth.RequestIdentityOf(identity), identity.Assurance())
	ctx = withScopedRoles(ctx, identity.ScopedRoles)
	ctx = withScopedRolesTruncated(ctx, identity.ScopedRolesTruncated)
	// Locally verified access token: an interactive session by construction.
	// API keys are exchanged at the perimeter through ValidateAPIKey and never
	// reach VerifyAccess, so this branch cannot be a machine credential.
	ctx = withCredentialKind(ctx, credentialKindSession)
	return ctx, nil
}

func stampForwardedHTTPIdentity(ctx context.Context, headers http.Header) (context.Context, error) {
	if forwardedCredentialIncomplete(headers.Values) {
		return ctx, errForwardedCredentialIncomplete
	}
	identity, err := auth.ParseRequestIdentity(
		headers.Get("X-User-Id"),
		headers.Get("X-Acting-As-User-Id"),
		headers.Get("X-Org-Id"),
		headers.Get("X-Session-Id"),
	)
	if err != nil {
		return ctx, err
	}
	identity.Delegation = auth.ParseActor(headers.Get("X-Act"))
	identity.ClientID, err = auth.ParseClientID(headers.Get("X-Client-Id"))
	if err != nil {
		return ctx, err
	}
	ctx = stampRequestIdentity(ctx, identity, assuranceFromTransport(
		headers.Get("X-Authentication-Methods"),
		headers.Get("X-Auth-Time"),
		headers.Get("X-Assurance-Level"),
		headers.Get("X-MFA-Verified-At"),
	))
	if scopes := headers.Get("X-Scopes"); scopes != "" {
		ctx = withScopes(ctx, parseScopes(scopes))
	}
	ctx = withCredentialKind(ctx, headers.Get("X-Credential-Kind"))
	if scopedRoles := headers.Get("X-Scoped-Roles"); scopedRoles != "" {
		ctx = withScopedRoles(ctx, parseScopedRoles(scopedRoles))
	}
	return withScopedRolesTruncated(ctx, headers.Get("X-Scoped-Roles-Truncated") == "true"), nil
}

// solutionIdentityHeader and solutionPublisherHeader are the registered
// solution and its publisher, both proved by the gateway from the solution's
// signed, solution-bound registration credential. Only the Work Context mint
// reads them, and only from a trusted forwarder.
const (
	solutionIdentityHeader  = "X-Codefly-Solution-Id"
	solutionPublisherHeader = "X-Codefly-Solution-Publisher"
)
