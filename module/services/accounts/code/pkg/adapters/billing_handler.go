package adapters

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"accounts/pkg/billing"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

type billingConnectHandler struct{ svc *business.Service }

func (h *billingConnectHandler) ListPublicPlans(
	ctx context.Context,
	req *connect.Request[gen.ListPublicPlansRequest],
) (*connect.Response[gen.ListPublicPlansResponse], error) {
	if err := Validate(req.Msg); err != nil {
		return nil, translateGRPCError(err)
	}
	plans, revision, err := h.svc.ListPublicPlans(ctx)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	response := &gen.ListPublicPlansResponse{Revision: revision}
	for _, plan := range plans {
		entry := &gen.PublicPlan{
			Key:             plan.Key,
			Name:            plan.Name,
			Description:     plan.Description,
			Currency:        plan.Currency,
			AmountMinor:     plan.AmountMinor,
			Interval:        plan.Interval,
			CheckoutEnabled: plan.CheckoutEnabled,
			ContactSales:    plan.ContactSales,
			TrialDays:       int32(plan.TrialDays),
			TaxBehavior:     plan.TaxBehavior,
			Fixture:         plan.Fixture,
		}
		for _, entitlement := range plan.Entitlements {
			entry.Entitlements = append(entry.Entitlements, &gen.PublicPlanEntitlement{
				Key:   entitlement.Feature,
				Limit: entitlement.Limit,
			})
		}
		response.Plans = append(response.Plans, entry)
	}
	return connect.NewResponse(response), nil
}

func (h *billingConnectHandler) OpenPortal(
	ctx context.Context,
	req *connect.Request[gen.OpenBillingPortalRequest],
) (*connect.Response[gen.OpenBillingPortalResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireBillingAdmin(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	if err := requireRecentMFA(ctx); err != nil {
		return nil, translateGRPCError(err)
	}
	idempotencyKey := req.Header().Get("Idempotency-Key")
	if idempotencyKey == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("Idempotency-Key header required"))
	}
	url, err := h.svc.OpenBillingPortal(ctx, business.OpenBillingPortalInput{
		UserID:         actorID,
		OrgID:          req.Msg.OrgId,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(&gen.OpenBillingPortalResponse{Url: url}), nil
}

func (h *billingConnectHandler) ListInvoices(
	ctx context.Context,
	req *connect.Request[gen.ListInvoicesRequest],
) (*connect.Response[gen.ListInvoicesResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	invoices, err := h.svc.ListInvoices(ctx, req.Msg.OrgId, int(req.Msg.Limit))
	if err != nil {
		return nil, translateGRPCError(err)
	}
	out := &gen.ListInvoicesResponse{
		Invoices: make([]*gen.Invoice, 0, len(invoices)),
	}
	for _, inv := range invoices {
		out.Invoices = append(out.Invoices, invoiceToProto(inv))
	}
	return connect.NewResponse(out), nil
}

func invoiceToProto(i billing.Invoice) *gen.Invoice {
	out := &gen.Invoice{
		Id:               i.ID,
		Number:           i.Number,
		Status:           i.Status,
		AmountDue:        i.AmountDue,
		AmountPaid:       i.AmountPaid,
		Currency:         i.Currency,
		HostedInvoiceUrl: i.HostedInvoiceURL,
		InvoicePdf:       i.InvoicePDF,
	}
	if i.Created > 0 {
		out.Created = timestampFromUnix(i.Created)
	}
	if i.PeriodStart > 0 {
		out.PeriodStart = timestampFromUnix(i.PeriodStart)
	}
	if i.PeriodEnd > 0 {
		out.PeriodEnd = timestampFromUnix(i.PeriodEnd)
	}
	return out
}

func timestampFromUnix(s int64) *timestamppb.Timestamp {
	return &timestamppb.Timestamp{Seconds: s}
}
