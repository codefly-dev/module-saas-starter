package adapters

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// UsageServer is shared by raw gRPC and Connect registration. The descriptor
// policy exposes ConsumeUsage only on the internal listener and GetUsage only
// on tenant-facing listeners.
type UsageServer struct {
	gen.UnimplementedUsageServiceServer
}

func (s *UsageServer) ConsumeUsage(ctx context.Context, req *gen.ConsumeUsageRequest) (*gen.ConsumeUsageResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}

	var occurredAt *time.Time
	if req.GetOccurredAt() != nil {
		if err := req.GetOccurredAt().CheckValid(); err != nil {
			return nil, status.Error(codes.InvalidArgument, "occurred_at is invalid")
		}
		value := req.GetOccurredAt().AsTime()
		occurredAt = &value
	}
	receipt, err := service.ConsumeUsage(ctx, business.ConsumeUsageInput{
		OrgID:          req.GetOrganizationId(),
		Meter:          req.GetMeter(),
		Quantity:       req.GetQuantity(),
		IdempotencyKey: req.GetIdempotencyKey(),
		OccurredAt:     occurredAt,
		Dimensions:     req.GetDimensions(),
	})
	if err != nil {
		if errors.Is(err, business.ErrUsageIdempotencyConflict) {
			return nil, status.Error(codes.AlreadyExists, business.ErrUsageIdempotencyConflict.Error())
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &gen.ConsumeUsageResponse{Receipt: usageReceiptToProto(receipt)}, nil
}

func (s *UsageServer) GetUsage(ctx context.Context, req *gen.GetUsageRequest) (*gen.GetUsageResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := requireScope(ctx, "entitlements:read"); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgPermission(ctx, actorID, req.GetOrganizationId(), "entitlements", "read"); err != nil {
		return nil, err
	}
	snapshot, err := service.GetUsage(ctx, req.GetOrganizationId(), req.GetMeter())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &gen.GetUsageResponse{
		OrganizationId: snapshot.OrgID,
		Meter:          snapshot.Meter,
		Used:           snapshot.Used,
		Limit:          snapshot.Limit,
		PeriodStart:    timestamppb.New(snapshot.PeriodStart),
		PeriodEnd:      timestamppb.New(snapshot.PeriodEnd),
	}, nil
}

func usageReceiptToProto(receipt *business.UsageReceipt) *gen.UsageReceipt {
	if receipt == nil {
		return nil
	}
	return &gen.UsageReceipt{
		EventId:        receipt.EventID,
		OrganizationId: receipt.OrgID,
		Meter:          receipt.Meter,
		Quantity:       receipt.Quantity,
		Accepted:       receipt.Accepted,
		Duplicate:      receipt.Duplicate,
		Used:           receipt.Used,
		Limit:          receipt.Limit,
		PeriodStart:    timestamppb.New(receipt.PeriodStart),
		PeriodEnd:      timestamppb.New(receipt.PeriodEnd),
		OccurredAt:     timestamppb.New(receipt.OccurredAt),
	}
}

var _ gen.UsageServiceServer = (*UsageServer)(nil)
