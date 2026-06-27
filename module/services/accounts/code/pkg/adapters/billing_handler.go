package adapters

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"accounts/pkg/billing"
	"accounts/pkg/business"
	"accounts/pkg/gen"
)

// billingConnectHandler — Connect-RPC surface for billing flows that
// the FE needs to invoke directly (without going through the
// auth-sidecar HTTP passthrough). Currently just OpenPortal; checkout
// stays on the REST path so paying customer flows match the documented
// auth-sidecar shape.
//
// Auth: org-admin gated. The portal session lets the holder change
// payment method / cancel subscription; we don't want a non-admin
// member to be able to do that.
type billingConnectHandler struct{ svc *business.Service }

func (h *billingConnectHandler) OpenPortal(
	ctx context.Context,
	req *connect.Request[gen.OpenBillingPortalRequest],
) (*connect.Response[gen.OpenBillingPortalResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	url, err := h.svc.OpenBillingPortal(ctx, business.OpenBillingPortalInput{
		UserID:    actorID,
		OrgID:     req.Msg.OrgId,
		ReturnURL: req.Msg.ReturnUrl,
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
