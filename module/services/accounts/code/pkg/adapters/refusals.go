package adapters

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/codefly-dev/core/wool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
)

// Every refusal names its reason in the response and in the log. This file is
// the one seam that decides both, for the Connect surface the browser uses and
// the gRPC surface everything else uses:
//
//   - refusalStatus turns whatever a handler returned into the status the
//     caller reads. A gRPC status passes through; a known business refusal
//     travels under its code with its own sentence; anything else is a fault
//     that is not the caller's to fix, and travels as Internal with a fixed
//     sentence — never the wrapped call path, a constraint name or a query.
//   - logRefusal writes one line per refused call: procedure, code, the
//     sentence the caller read, and, for a fault, the withheld cause. Ids
//     only — never a request body.

// internalRefusalMessage is what a caller reads in place of a fault.
const internalRefusalMessage = "the request could not be completed; try again, and contact an administrator if it keeps failing"

// businessRefusals are the business sentinels a person can act on, with the code
// each travels under. The sentinel's own text is the message: the error that
// reached the adapter may wrap it in a call path that is not the caller's to
// read. Typed refusals whose sentence carries data are handled in
// businessRefusal before this table.
var businessRefusals = []struct {
	err  error
	code codes.Code
}{
	{business.ErrOrgAdminContinuity, codes.FailedPrecondition},
	{business.ErrInvitationAlreadyPending, codes.AlreadyExists},
	{business.ErrAlreadyMember, codes.AlreadyExists},
	{business.ErrInvitationUnavailable, codes.NotFound},
	{business.ErrInvitationEmailMismatch, codes.PermissionDenied},
	{business.ErrInvitationEmailUnverified, codes.FailedPrecondition},
	{business.ErrInvitationExpired, codes.FailedPrecondition},
	{business.ErrInvitationDeliveryUnavailable, codes.FailedPrecondition},
	{business.ErrWaitlistDeliveryUnavailable, codes.FailedPrecondition},
	{business.ErrPlatformRoleRequired, codes.PermissionDenied},
	{business.ErrImpersonationJustificationRequired, codes.InvalidArgument},
	{business.ErrImpersonationTargetInactive, codes.FailedPrecondition},
	{business.ErrPrivacyWorkflowUnavailable, codes.FailedPrecondition},
	{business.ErrPolicyLogUnreachable, codes.FailedPrecondition},
}

// businessRefusal maps a known business refusal to its status.
func businessRefusal(err error) (*status.Status, bool) {
	var quota *business.EntitlementQuotaError
	if errors.As(err, &quota) || errors.Is(err, business.ErrEntitlementQuotaExceeded) {
		return status.Convert(quotaRefusal(err)), true
	}
	var continuity *business.IdentityAdminContinuityError
	if errors.As(err, &continuity) {
		return status.New(codes.FailedPrecondition, continuity.Error()), true
	}
	for _, refusal := range businessRefusals {
		if errors.Is(err, refusal.err) {
			return status.New(refusal.code, refusal.err.Error()), true
		}
	}
	return nil, false
}

// refusalStatus is the status a caller reads for err, and the cause withheld
// from it when err is a fault (nil otherwise).
func refusalStatus(err error) (*status.Status, error) {
	if st, ok := status.FromError(err); ok {
		return st, nil
	}
	if st, ok := businessRefusal(err); ok {
		return st, nil
	}
	switch {
	case errors.Is(err, context.Canceled):
		return status.New(codes.Canceled, "the request was canceled"), nil
	case errors.Is(err, context.DeadlineExceeded):
		return status.New(codes.DeadlineExceeded, "the request took too long; try again"), nil
	}
	return status.New(codes.Internal, internalRefusalMessage), err
}

// withheldCause carries a fault inside the Connect error the caller receives:
// the wire message is the fixed sentence, and the chain stays reachable for the
// log through Unwrap.
type withheldCause struct{ cause error }

func (e *withheldCause) Error() string { return internalRefusalMessage }
func (e *withheldCause) Unwrap() error { return e.cause }

// serverFault reports whether a code is the host's failure rather than the
// caller's: an operator has something to fix, so it is logged as an error.
func serverFault(code codes.Code) bool {
	switch code {
	case codes.Internal, codes.Unknown, codes.DataLoss, codes.Unavailable, codes.DeadlineExceeded:
		return true
	}
	return false
}

// logRefusal writes the one line a refused call leaves for an operator. The
// trace id is stamped by wool from the request's span, which is how a support
// person joins this line to the request the browser made.
func logRefusal(ctx context.Context, procedure string, code codes.Code, message string, cause error) {
	fields := []*wool.LogField{
		wool.Field("procedure", procedure),
		wool.Field("code", connectCodeFromGRPC(code).String()),
		wool.Field("reason", message),
	}
	if cause != nil {
		fields = append(fields, wool.Field("cause", cause.Error()))
	}
	w := wool.Get(ctx).In("refusal")
	if serverFault(code) {
		w.Error("request failed", fields...)
		return
	}
	w.Warn("request refused", fields...)
}

// connectRefusalInterceptor translates and logs every refusal on the Connect
// surface, including the auth and rate-limit interceptors' own, so it sits
// outside them in the chain.
func connectRefusalInterceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			resp, err := next(ctx, req)
			if err == nil {
				return resp, nil
			}
			translated := translateGRPCError(err)
			var cause error
			var withheld *withheldCause
			if errors.As(translated, &withheld) {
				cause = withheld.cause
			}
			var ce *connect.Error
			if !errors.As(translated, &ce) {
				return resp, translated
			}
			logRefusal(ctx, req.Spec().Procedure, grpcCodeFromConnect(ce.Code()), ce.Message(), cause)
			return resp, translated
		}
	}
}

// grpcRefusalInterceptor is the gRPC twin: the status a gRPC caller reads is
// decided by refusalStatus, and the refusal is logged the same way.
func grpcRefusalInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		resp, err := handler(ctx, req)
		if err == nil {
			return resp, nil
		}
		st, cause := refusalStatus(err)
		logRefusal(ctx, info.FullMethod, st.Code(), st.Message(), cause)
		return resp, st.Err()
	}
}

func grpcCodeFromConnect(c connect.Code) codes.Code {
	// Connect's codes are numbered as gRPC's, by design of the protocol.
	return codes.Code(c)
}
