package adapters

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

type dashboardConnectHandler struct{ svc *business.Service }

func (h *dashboardConnectHandler) CreateDashboard(ctx context.Context, req *connect.Request[gen.CreateDashboardRequest]) (*connect.Response[gen.Dashboard], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := requireScope(ctx, "dashboards:write"); err != nil {
		return nil, translateGRPCError(err)
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	orgID := req.Msg.OrgId
	if err := requireOrgMember(ctx, actorID, orgID); err != nil {
		return nil, translateGRPCError(err)
	}
	spec, err := specToJSON(req.Msg.Spec)
	if err != nil {
		return nil, err
	}
	record, err := h.svc.CreateDashboard(ctx, orgID, actorID, req.Msg.Id, req.Msg.Name, spec)
	if err != nil {
		return nil, err
	}
	return dashboardResponse(record)
}

func (h *dashboardConnectHandler) GetDashboard(ctx context.Context, req *connect.Request[gen.GetDashboardRequest]) (*connect.Response[gen.Dashboard], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := requireScope(ctx, "dashboards:read"); err != nil {
		return nil, translateGRPCError(err)
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	orgID := callerOrg(ctx)
	if err := requireOrgMember(ctx, actorID, orgID); err != nil {
		return nil, translateGRPCError(err)
	}
	record, err := h.svc.GetDashboard(ctx, orgID, actorID, isOrgAdmin(ctx, actorID, orgID), req.Msg.Id)
	if err != nil {
		return nil, err
	}
	return dashboardResponse(record)
}

func (h *dashboardConnectHandler) ListDashboards(ctx context.Context, req *connect.Request[gen.ListDashboardsRequest]) (*connect.Response[gen.ListDashboardsResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := requireScope(ctx, "dashboards:read"); err != nil {
		return nil, translateGRPCError(err)
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	orgID := req.Msg.OrgId
	if err := requireOrgMember(ctx, actorID, orgID); err != nil {
		return nil, translateGRPCError(err)
	}
	records, err := h.svc.ListDashboards(ctx, orgID, actorID, scopeFromProto(req.Msg.Scope))
	if err != nil {
		return nil, err
	}
	out := &gen.ListDashboardsResponse{Dashboards: make([]*gen.Dashboard, 0, len(records))}
	for _, record := range records {
		proto, err := dashboardToProto(record)
		if err != nil {
			return nil, err
		}
		out.Dashboards = append(out.Dashboards, proto)
	}
	return connect.NewResponse(out), nil
}

func (h *dashboardConnectHandler) UpdateDashboard(ctx context.Context, req *connect.Request[gen.UpdateDashboardRequest]) (*connect.Response[gen.Dashboard], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := requireScope(ctx, "dashboards:write"); err != nil {
		return nil, translateGRPCError(err)
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	orgID := callerOrg(ctx)
	if err := requireOrgMember(ctx, actorID, orgID); err != nil {
		return nil, translateGRPCError(err)
	}
	var spec []byte
	if req.Msg.Spec != nil {
		if spec, err = specToJSON(req.Msg.Spec); err != nil {
			return nil, err
		}
	}
	record, err := h.svc.UpdateDashboard(ctx, orgID, actorID, isOrgAdmin(ctx, actorID, orgID), req.Msg.Id, req.Msg.Name, spec)
	if err != nil {
		return nil, err
	}
	return dashboardResponse(record)
}

func (h *dashboardConnectHandler) DeleteDashboard(ctx context.Context, req *connect.Request[gen.DeleteDashboardRequest]) (*connect.Response[emptypb.Empty], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := requireScope(ctx, "dashboards:write"); err != nil {
		return nil, translateGRPCError(err)
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	orgID := callerOrg(ctx)
	if err := requireOrgMember(ctx, actorID, orgID); err != nil {
		return nil, translateGRPCError(err)
	}
	if err := h.svc.DeleteDashboard(ctx, orgID, actorID, isOrgAdmin(ctx, actorID, orgID), req.Msg.Id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (h *dashboardConnectHandler) ShareDashboard(ctx context.Context, req *connect.Request[gen.ShareDashboardRequest]) (*connect.Response[gen.Dashboard], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := requireScope(ctx, "dashboards:share"); err != nil {
		return nil, translateGRPCError(err)
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	orgID := callerOrg(ctx)
	if err := requireOrgAdmin(ctx, actorID, orgID); err != nil {
		return nil, translateGRPCError(err)
	}
	record, err := h.svc.ShareDashboard(ctx, orgID, actorID, req.Msg.Id, visibilityFromProto(req.Msg.Visibility))
	if err != nil {
		return nil, err
	}
	return dashboardResponse(record)
}

func isOrgAdmin(ctx context.Context, actorID, orgID string) bool {
	return requireOrgAdmin(ctx, actorID, orgID) == nil
}

func specToJSON(spec *structpb.Struct) ([]byte, error) {
	if spec == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, status.Error(codes.InvalidArgument, "dashboard spec is required"))
	}
	return spec.MarshalJSON()
}

func dashboardResponse(record *business.Dashboard) (*connect.Response[gen.Dashboard], error) {
	proto, err := dashboardToProto(record)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(proto), nil
}

func dashboardToProto(record *business.Dashboard) (*gen.Dashboard, error) {
	spec := &structpb.Struct{}
	if len(record.Spec) > 0 {
		if err := spec.UnmarshalJSON(record.Spec); err != nil {
			return nil, err
		}
	}
	out := &gen.Dashboard{
		Id:         record.ID,
		OrgId:      record.OrgID,
		OwnerId:    record.OwnerID,
		Name:       record.Name,
		Spec:       spec,
		Visibility: visibilityToProto(record.Visibility),
	}
	if !record.CreatedAt.IsZero() {
		out.CreatedAt = timestamppb.New(record.CreatedAt)
	}
	if !record.UpdatedAt.IsZero() {
		out.UpdatedAt = timestamppb.New(record.UpdatedAt)
	}
	return out, nil
}

func visibilityToProto(v business.DashboardVisibility) gen.DashboardVisibility {
	if v == business.DashboardVisibilityOrg {
		return gen.DashboardVisibility_DASHBOARD_VISIBILITY_ORG
	}
	return gen.DashboardVisibility_DASHBOARD_VISIBILITY_PRIVATE
}

func visibilityFromProto(v gen.DashboardVisibility) business.DashboardVisibility {
	if v == gen.DashboardVisibility_DASHBOARD_VISIBILITY_ORG {
		return business.DashboardVisibilityOrg
	}
	return business.DashboardVisibilityPrivate
}

func scopeFromProto(s gen.DashboardListScope) business.DashboardListScope {
	switch s {
	case gen.DashboardListScope_DASHBOARD_LIST_SCOPE_MINE:
		return business.DashboardListMine
	case gen.DashboardListScope_DASHBOARD_LIST_SCOPE_ORG_SHARED:
		return business.DashboardListOrgShared
	default:
		return business.DashboardListAll
	}
}
