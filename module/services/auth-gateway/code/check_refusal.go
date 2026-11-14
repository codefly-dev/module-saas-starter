package main

// How a request the gateway does not admit is answered, when the question was
// put to the perimeter's Check or to the authority.
//
// Two outcomes refuse a request, and they are different facts:
//
//   - a denial: the check reached a verdict about the caller — the credential
//     is missing, invalid or revoked, or the authority refused this person.
//     PermissionDenied (or Unauthenticated), 403 (or 401), with its reason.
//   - a failure: the check reached no verdict — the caller's own request ended
//     (Canceled), its deadline passed (DeadlineExceeded), or a dependency the
//     check needs did not answer (Unavailable). Refused all the same: nothing
//     is forwarded without an allow. But it is answered under the code that
//     says what happened, never as a denial, because a client reads a denial
//     as "you may not" and stops: a person holding the grant is told they do
//     not.
//
// The HTTP statuses follow the mapping the gateway's other refusals use (the
// hop library's forward.refuse, and the browser hop's Issue refusal in
// gateway_hop.go): Unavailable and DeadlineExceeded are 503. Canceled is 499,
// the status a proxy records for a client that closed its request; nothing
// else in HTTP means that. Anything else that reached no verdict is 500.

import (
	"context"
	"fmt"
	"net/http"

	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	"google.golang.org/grpc/codes"
)

// statusClientClosedRequest is the HTTP status of a request whose caller went
// away before it was answered.
const statusClientClosedRequest = 499

// isVerdict reports whether code is an answer about the request itself, as
// opposed to a failure to reach one.
func isVerdict(code codes.Code) bool {
	switch code {
	case codes.PermissionDenied, codes.Unauthenticated, codes.InvalidArgument, codes.NotFound,
		codes.FailedPrecondition, codes.AlreadyExists, codes.OutOfRange:
		return true
	}
	return false
}

// failureStatus is the code (failureCode) and HTTP status a failure is
// answered with. Never PermissionDenied, never 403.
func failureStatus(code codes.Code) (codes.Code, int) {
	switch code = failureCode(code); code {
	case codes.Canceled:
		return code, statusClientClosedRequest
	case codes.DeadlineExceeded, codes.Unavailable:
		return code, http.StatusServiceUnavailable
	}
	return code, http.StatusInternalServerError
}

// checkRefusal is the answer to a request the perimeter's Check did not admit.
type checkRefusal struct {
	httpStatus int
	code       codes.Code
	reason     string
	// denied is true for a verdict about the caller's credential, false for a
	// check that reached none.
	denied bool
}

// refusalOf reads the perimeter's Check answer, and reports whether it
// admits the request. Only an OK status with no denied response admits it.
func refusalOf(ctx context.Context, checked *authv3.CheckResponse, err error) (checkRefusal, bool) {
	if err != nil {
		code, httpStatus := failureStatus(failureOf(ctx, err))
		return checkRefusal{httpStatus: httpStatus, code: code, reason: "auth check failed: " + code.String()}, false
	}
	code := codes.Code(checked.GetStatus().GetCode())
	denied := checked.GetDeniedResponse()
	switch {
	case code == codes.OK && denied == nil:
		return checkRefusal{}, true
	case code == codes.OK, code == codes.PermissionDenied, code == codes.Unauthenticated:
		httpStatus := int(denied.GetStatus().GetCode())
		if httpStatus == 0 {
			httpStatus = http.StatusForbidden
		}
		if code == codes.OK {
			code = codes.PermissionDenied
		}
		reason := denied.GetBody()
		if reason == "" {
			reason = "the credential this request presents was refused"
		}
		return checkRefusal{httpStatus: httpStatus, code: code, reason: reason, denied: true}, false
	}
	code, httpStatus := failureStatus(code)
	reason := denied.GetBody()
	if reason == "" {
		reason = checked.GetStatus().GetMessage()
	}
	if reason == "" {
		reason = fmt.Sprintf("the credential this request presents could not be checked (%s)", code)
	}
	return checkRefusal{httpStatus: httpStatus, code: code, reason: reason}, false
}
