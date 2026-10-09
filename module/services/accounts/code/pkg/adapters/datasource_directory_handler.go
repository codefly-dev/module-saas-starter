package adapters

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// The datasource directory surface. Linking one's own provider account needs
// organization membership; everything an administrator decides for the whole
// organization (bindings, domains, reading every link) needs administration.

func (h *datasourceConnectHandler) BeginDatasourceAccountLink(ctx context.Context, req *connect.Request[gen.BeginDatasourceAccountLinkRequest]) (*connect.Response[gen.BeginDatasourceAccountLinkResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgMember(ctx, actorID, req.Msg.GetOrgId()); err != nil {
		return nil, translateGRPCError(err)
	}
	handle, err := h.svc.BeginDatasourceAccountLink(ctx, actorID, req.Msg.GetOrgId(), req.Msg.GetConnector(), req.Msg.GetRedirectUri(), req.Msg.GetSourceId())
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(&gen.BeginDatasourceAccountLinkResponse{
		AuthorizeUrl: handle.AuthorizeURL, State: handle.State, ExpiresAt: timestamppb.New(handle.ExpiresAt),
	}), nil
}

func (h *datasourceConnectHandler) CompleteDatasourceAccountLink(ctx context.Context, req *connect.Request[gen.CompleteDatasourceAccountLinkRequest]) (*connect.Response[gen.CompleteDatasourceAccountLinkResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgMember(ctx, actorID, req.Msg.GetOrgId()); err != nil {
		return nil, translateGRPCError(err)
	}
	link, err := h.svc.CompleteDatasourceAccountLink(ctx, actorID, req.Msg.GetOrgId(), req.Msg.GetState(), req.Msg.GetCode())
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(&gen.CompleteDatasourceAccountLinkResponse{Link: accountLinkToProto(link)}), nil
}

func (h *datasourceConnectHandler) ListMyDatasourceAccountLinks(ctx context.Context, req *connect.Request[gen.ListMyDatasourceAccountLinksRequest]) (*connect.Response[gen.ListMyDatasourceAccountLinksResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgMember(ctx, actorID, req.Msg.GetOrgId()); err != nil {
		return nil, translateGRPCError(err)
	}
	links, err := h.svc.ListMyDatasourceAccountLinks(ctx, actorID, req.Msg.GetOrgId())
	if err != nil {
		return nil, translateGRPCError(err)
	}
	out := &gen.ListMyDatasourceAccountLinksResponse{}
	for _, l := range links {
		out.Links = append(out.Links, accountLinkToProto(l))
	}
	return connect.NewResponse(out), nil
}

func (h *datasourceConnectHandler) DeleteDatasourceAccountLink(ctx context.Context, req *connect.Request[gen.DeleteDatasourceAccountLinkRequest]) (*connect.Response[gen.DeleteDatasourceAccountLinkResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgMember(ctx, actorID, req.Msg.GetOrgId()); err != nil {
		return nil, translateGRPCError(err)
	}
	if err := h.svc.DeleteDatasourceAccountLink(ctx, actorID, req.Msg.GetOrgId(), req.Msg.GetId()); err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(&gen.DeleteDatasourceAccountLinkResponse{}), nil
}

func (h *datasourceConnectHandler) GetDatasourceDirectory(ctx context.Context, req *connect.Request[gen.GetDatasourceDirectoryRequest]) (*connect.Response[gen.GetDatasourceDirectoryResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.GetOrgId()); err != nil {
		return nil, translateGRPCError(err)
	}
	dir, err := h.svc.GetDatasourceDirectory(ctx, req.Msg.GetOrgId())
	if err != nil {
		return nil, translateGRPCError(err)
	}
	out := &gen.GetDatasourceDirectoryResponse{}
	for _, l := range dir.Links {
		out.Links = append(out.Links, accountLinkToProto(l))
	}
	for _, b := range dir.Bindings {
		out.Bindings = append(out.Bindings, groupBindingToProto(b))
	}
	for _, d := range dir.Domains {
		out.Domains = append(out.Domains, domainToProto(d))
	}
	for _, t := range dir.Teams {
		out.Teams = append(out.Teams, &gen.DatasourceDirectoryTeam{Id: t.ID, Name: t.Name})
	}
	return connect.NewResponse(out), nil
}

func (h *datasourceConnectHandler) BindDatasourceGroup(ctx context.Context, req *connect.Request[gen.BindDatasourceGroupRequest]) (*connect.Response[gen.BindDatasourceGroupResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.GetOrgId()); err != nil {
		return nil, translateGRPCError(err)
	}
	b, err := h.svc.BindDatasourceGroup(ctx, actorID, req.Msg.GetOrgId(), req.Msg.GetConnector(), req.Msg.GetProviderGroupId(), req.Msg.GetTeamId())
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(&gen.BindDatasourceGroupResponse{Binding: groupBindingToProto(b)}), nil
}

func (h *datasourceConnectHandler) UnbindDatasourceGroup(ctx context.Context, req *connect.Request[gen.UnbindDatasourceGroupRequest]) (*connect.Response[gen.UnbindDatasourceGroupResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.GetOrgId()); err != nil {
		return nil, translateGRPCError(err)
	}
	if err := h.svc.UnbindDatasourceGroup(ctx, actorID, req.Msg.GetOrgId(), req.Msg.GetId()); err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(&gen.UnbindDatasourceGroupResponse{}), nil
}

func (h *datasourceConnectHandler) ClaimDatasourceDomain(ctx context.Context, req *connect.Request[gen.ClaimDatasourceDomainRequest]) (*connect.Response[gen.ClaimDatasourceDomainResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.GetOrgId()); err != nil {
		return nil, translateGRPCError(err)
	}
	d, err := h.svc.ClaimDatasourceDomain(ctx, actorID, req.Msg.GetOrgId(), req.Msg.GetDomain())
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(&gen.ClaimDatasourceDomainResponse{Domain: domainToProto(d)}), nil
}

func (h *datasourceConnectHandler) VerifyDatasourceDomain(ctx context.Context, req *connect.Request[gen.VerifyDatasourceDomainRequest]) (*connect.Response[gen.VerifyDatasourceDomainResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.GetOrgId()); err != nil {
		return nil, translateGRPCError(err)
	}
	d, err := h.svc.VerifyDatasourceDomain(ctx, actorID, req.Msg.GetOrgId(), req.Msg.GetId())
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(&gen.VerifyDatasourceDomainResponse{Domain: domainToProto(d)}), nil
}

func (h *datasourceConnectHandler) DeleteDatasourceDomain(ctx context.Context, req *connect.Request[gen.DeleteDatasourceDomainRequest]) (*connect.Response[gen.DeleteDatasourceDomainResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.GetOrgId()); err != nil {
		return nil, translateGRPCError(err)
	}
	if err := h.svc.DeleteDatasourceDomain(ctx, actorID, req.Msg.GetOrgId(), req.Msg.GetId()); err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(&gen.DeleteDatasourceDomainResponse{}), nil
}

func accountLinkToProto(l *business.DatasourceAccountLink) *gen.DatasourceAccountLink {
	return &gen.DatasourceAccountLink{
		Id: l.ID, OrgId: l.OrgID, UserId: l.UserID, Connector: l.Connector,
		ProviderAccountId: l.ProviderAccountID, ProviderAccountLogin: l.ProviderAccountLogin,
		CreatedAt: timestamppb.New(l.CreatedAt),
	}
}

func groupBindingToProto(b *business.DatasourceGroupBinding) *gen.DatasourceGroupBinding {
	return &gen.DatasourceGroupBinding{
		Id: b.ID, OrgId: b.OrgID, Connector: b.Connector, ProviderGroupId: b.ProviderGroupID,
		TeamId: b.TeamID, CreatedBy: b.CreatedBy, CreatedAt: timestamppb.New(b.CreatedAt),
	}
}

func domainToProto(d *business.DatasourceDomain) *gen.DatasourceVerifiedDomain {
	out := &gen.DatasourceVerifiedDomain{
		Id: d.ID, OrgId: d.OrgID, Domain: d.Domain, Status: gen.DatasourceDomainStatus_DATASOURCE_DOMAIN_STATUS_PENDING,
		TxtRecordName: d.TXTRecordName(), TxtRecordValue: d.TXTRecordValue(), CreatedAt: timestamppb.New(d.CreatedAt),
	}
	if d.VerifiedAt != nil {
		out.Status = gen.DatasourceDomainStatus_DATASOURCE_DOMAIN_STATUS_VERIFIED
		out.VerifiedAt = timestamppb.New(*d.VerifiedAt)
	}
	return out
}
