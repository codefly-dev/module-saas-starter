package adapters

import (
	"context"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// errViewerNotAddressedToCaller is the refusal when the viewer Work Context a
// module presents was minted for another module.
const errViewerNotAddressedToCaller = "the viewer Work Context is not addressed to the calling module"

// authenticateModuleViewer is how a module asks about a viewer it serves, as
// the delegated exchanges do: the module proves itself with its own module Work
// Context in the header, held to its current grant and installation, and the
// viewer travels in the request body. The viewer must be addressed to that
// module — its audience is the module's prefix — so a viewer capability held by
// any other workload is a bearer that proves nothing here.
//
// Unauthenticated when the module or the viewer does not verify;
// PermissionDenied when the module is not current or the viewer is another
// module's.
func authenticateModuleViewer(ctx context.Context, encodedViewer string) (*basev0.WorkContextV1, error) {
	authority := WorkContextSingleton()
	if service == nil || !authority.configured() {
		return nil, status.Error(codes.Unavailable, "Work Context authority is not configured")
	}
	caller, err := moduleCaller(ctx)
	if err != nil {
		return nil, err
	}
	module, err := service.AuthorizeModuleCapability(ctx, caller)
	if err != nil {
		return nil, err
	}
	verified, err := authority.readBack(ctx, encodedViewer)
	if err != nil {
		return nil, refusedReadBack(ctx, err, status.Error(codes.Unauthenticated, "invalid viewer Work Context"))
	}
	claims := verified.Context()
	if claims.GetAudience() == "" || claims.GetAudience() != module.Grant.Prefix {
		return nil, status.Error(codes.PermissionDenied, errViewerNotAddressedToCaller)
	}
	return claims, nil
}
