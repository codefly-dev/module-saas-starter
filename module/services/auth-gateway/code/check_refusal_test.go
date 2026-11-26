package main

import (
	"context"
	"errors"
	"net/http"
	"testing"

	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"github.com/stretchr/testify/require"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
)

// refusalOf is the one reading of a Check answer every gateway path shares.
// The cases ExtAuthz does not produce today are here too: an answer that is
// not OK and carries no denied response was answered 403 "forbidden", which
// turned any failure such an answer stood for into a denial.
func TestRefusalOfTellsADenialFromAFailure(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, tc := range map[string]struct {
		ctx        context.Context
		checked    *authv3.CheckResponse
		err        error
		admitted   bool
		denied     bool
		code       codes.Code
		httpStatus int
	}{
		"allow":                        {checked: &authv3.CheckResponse{Status: &rpcstatus.Status{}}, admitted: true},
		"denied":                       {checked: unauthenticated("token revoked"), denied: true, code: codes.Unauthenticated, httpStatus: http.StatusUnauthorized},
		"denied without a status":      {checked: &authv3.CheckResponse{Status: &rpcstatus.Status{Code: int32(codes.PermissionDenied)}}, denied: true, code: codes.PermissionDenied, httpStatus: http.StatusForbidden},
		"unavailable":                  {checked: unavailable(context.Background(), errors.New("store down"), "revocation check unavailable"), code: codes.Unavailable, httpStatus: http.StatusServiceUnavailable},
		"canceled":                     {checked: unavailable(cancelled, errors.New("store down"), "revocation check unavailable"), code: codes.Canceled, httpStatus: 499},
		"not OK, no denied response":   {checked: &authv3.CheckResponse{Status: &rpcstatus.Status{Code: int32(codes.Unavailable)}}, code: codes.Unavailable, httpStatus: http.StatusServiceUnavailable},
		"canceled, no denied response": {checked: &authv3.CheckResponse{Status: &rpcstatus.Status{Code: int32(codes.Canceled)}}, code: codes.Canceled, httpStatus: 499},
		"unknown code":                 {checked: &authv3.CheckResponse{Status: &rpcstatus.Status{Code: int32(codes.DataLoss)}}, code: codes.Internal, httpStatus: http.StatusInternalServerError},
		"check error":                  {err: errors.New("boom"), code: codes.Unavailable, httpStatus: http.StatusServiceUnavailable},
		"check error, caller gone":     {ctx: cancelled, err: errors.New("boom"), code: codes.Canceled, httpStatus: 499},
		// One definition of a verdict: a denial under any verdict code is a
		// denial, never a failure answered 500.
		"invalid argument verdict": {checked: &authv3.CheckResponse{Status: &rpcstatus.Status{Code: int32(codes.InvalidArgument)}, HttpResponse: &authv3.CheckResponse_DeniedResponse{DeniedResponse: &authv3.DeniedHttpResponse{Status: &typev3.HttpStatus{Code: typev3.StatusCode_BadRequest}, Body: "malformed credential"}}}, denied: true, code: codes.InvalidArgument, httpStatus: http.StatusBadRequest},
		"not found verdict":        {checked: &authv3.CheckResponse{Status: &rpcstatus.Status{Code: int32(codes.NotFound)}}, denied: true, code: codes.NotFound, httpStatus: http.StatusForbidden},
		// Only an explicit OK admits: an empty answer is a failure.
		"nil answer":       {checked: nil, code: codes.Internal, httpStatus: http.StatusInternalServerError},
		"answer no status": {checked: &authv3.CheckResponse{}, code: codes.Internal, httpStatus: http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := tc.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			refusal, admitted := refusalOf(ctx, tc.checked, tc.err)
			require.Equal(t, tc.admitted, admitted)
			if admitted {
				return
			}
			require.Equal(t, tc.denied, refusal.denied)
			require.Equal(t, tc.code, refusal.code)
			require.Equal(t, tc.httpStatus, refusal.httpStatus)
			require.NotEmpty(t, refusal.reason, "every refusal names its reason")
			if !tc.denied {
				require.NotEqual(t, codes.PermissionDenied, refusal.code)
				require.NotEqual(t, http.StatusForbidden, refusal.httpStatus)
			}
		})
	}
}
