package adapters

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// ListPlatformCatalogue is the platform Catalogue: every composed module and
// solution, what it declares, what runs, and where it is installed.
func (s *PlatformAdminServer) ListPlatformCatalogue(ctx context.Context, req *gen.ListPlatformCatalogueRequest) (*gen.ListPlatformCatalogueResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requirePlatformRole(ctx, actorID, "super_admin"); err != nil {
		return nil, err
	}
	catalogue, err := service.ListPlatformCatalogue(ctx, actorID, req.GetIncludeTombstoned())
	if err != nil {
		var storeErr *business.StoreError
		if errors.As(err, &storeErr) && storeErr.StoreErrorType == business.ErrTypePermission {
			return nil, status.Error(codes.PermissionDenied, "requires platform super_admin role")
		}
		return nil, status.Error(codes.Internal, "cannot read the platform catalogue")
	}
	response := &gen.ListPlatformCatalogueResponse{
		RegistryRevision:    catalogue.RegistryRevision,
		ApprovedInventories: catalogue.ApprovedInventories,
	}
	for _, row := range catalogue.Entries {
		entry := row.Entry
		if row.Registration != nil {
			entry.Registration = catalogueRegistrationProto(row.Registration)
		}
		response.Entries = append(response.Entries, entry)
	}
	return response, nil
}

// catalogueRegistrationProto is the registry's own projection with deployment
// topology withheld. The frontend manifest, backend upstream and service alias
// are served only on the cluster-internal projection, never to a browser, and a
// super administrator's browser is still a browser. Revisions, contract
// versions and leases stay: they are what the derived status is read from.
func catalogueRegistrationProto(record *business.SolutionRegistration) *gen.SolutionRegistration {
	out := solutionRegistrationProto(record)
	if out.Frontend != nil {
		out.Frontend.Manifest = ""
	}
	if out.Backend != nil {
		out.Backend.Upstream = ""
		out.Backend.ServiceAlias = ""
	}
	return out
}

func (h *platformAdminConnectHandler) ListPlatformCatalogue(ctx context.Context, req *connect.Request[gen.ListPlatformCatalogueRequest]) (*connect.Response[gen.ListPlatformCatalogueResponse], error) {
	return unary(ctx, req, h.inner.ListPlatformCatalogue)
}
