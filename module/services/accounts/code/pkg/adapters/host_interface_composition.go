package adapters

import (
	"context"
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	compositionv1 "accounts/pkg/gen/host/composition/v1"
)

// HostCompositionServer serves codefly.dev/composition.
type HostCompositionServer struct {
	compositionv1.UnimplementedCompositionServiceServer
}

// ApplyManifest delivers one signed carrier: accounts' delivery owner
// authenticates and records it, and the authority core composes the module it
// declares (business.ApplyComposition).
func (s *HostCompositionServer) ApplyManifest(ctx context.Context, req *compositionv1.ApplyManifestRequest) (*compositionv1.ApplyManifestResponse, error) {
	if service == nil {
		return nil, status.Error(codes.Unavailable, "composition service is not wired")
	}
	out, err := service.ApplyComposition(ctx, req)
	if err == nil {
		return out, nil
	}
	httpStatus, reason := solutionDeliveryStatus(err)
	code := codes.Unavailable
	switch httpStatus {
	case http.StatusBadRequest:
		code = codes.InvalidArgument
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity:
		code = codes.PermissionDenied
	case http.StatusConflict:
		code = codes.FailedPrecondition
	}
	return nil, status.Error(code, reason)
}
