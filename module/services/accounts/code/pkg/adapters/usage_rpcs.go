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
// policy exposes consumption only on the internal listener and reads only on
// tenant-facing listeners.
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

func (s *UsageServer) ListUsageMeters(
	ctx context.Context,
	req *gen.ListUsageMetersRequest,
) (*gen.ListUsageMetersResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := authorizeUsageRead(ctx, req.GetOrganizationId()); err != nil {
		return nil, err
	}
	snapshots, observedAt, err := service.ListUsageMeters(ctx, req.GetOrganizationId())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	out := make([]*gen.UsageMeterSnapshot, 0, len(snapshots))
	for _, snapshot := range snapshots {
		out = append(out, &gen.UsageMeterSnapshot{
			Meter:       usageMeterToProto(snapshot.Meter),
			Used:        snapshot.Used,
			Limit:       snapshot.Limit,
			PeriodStart: timestamppb.New(snapshot.PeriodStart),
			PeriodEnd:   timestamppb.New(snapshot.PeriodEnd),
		})
	}
	return &gen.ListUsageMetersResponse{
		Meters: out, ObservedAt: timestamppb.New(observedAt),
	}, nil
}

func (s *UsageServer) GetUsageHistory(
	ctx context.Context,
	req *gen.GetUsageHistoryRequest,
) (*gen.GetUsageHistoryResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := req.GetFrom().CheckValid(); err != nil {
		return nil, status.Error(codes.InvalidArgument, "from is invalid")
	}
	if err := req.GetTo().CheckValid(); err != nil {
		return nil, status.Error(codes.InvalidArgument, "to is invalid")
	}
	if err := authorizeUsageRead(ctx, req.GetOrganizationId()); err != nil {
		return nil, err
	}
	history, observedAt, err := service.GetUsageHistory(
		ctx,
		req.GetOrganizationId(),
		req.GetMeter(),
		req.GetFrom().AsTime(),
		req.GetTo().AsTime(),
		usageBucketFromProto(req.GetBucket()),
	)
	if err != nil {
		if errors.Is(err, business.ErrInvalidUsageRange) {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	buckets := make([]*gen.UsageBucket, 0, len(history.Values))
	for _, bucket := range history.Values {
		buckets = append(buckets, &gen.UsageBucket{
			Start:    timestamppb.New(bucket.Start),
			End:      timestamppb.New(bucket.End),
			Quantity: bucket.Quantity,
		})
	}
	return &gen.GetUsageHistoryResponse{
		OrganizationId: history.OrgID,
		Meter:          history.Meter,
		Bucket:         req.GetBucket(),
		Buckets:        buckets,
		Total:          history.Total,
		From:           timestamppb.New(history.From),
		To:             timestamppb.New(history.To),
		ObservedAt:     timestamppb.New(observedAt),
	}, nil
}

func authorizeUsageRead(ctx context.Context, orgID string) error {
	if err := requireScope(ctx, "entitlements:read"); err != nil {
		return err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return err
	}
	return requireOrgPermission(ctx, actorID, orgID, "entitlements", "read")
}

func usageMeterToProto(meter business.UsageMeterDefinition) *gen.UsageMeter {
	aggregation := gen.UsageAggregation_USAGE_AGGREGATION_UNSPECIFIED
	switch meter.Aggregation {
	case business.UsageAggregationSum:
		aggregation = gen.UsageAggregation_USAGE_AGGREGATION_SUM
	case business.UsageAggregationMax:
		aggregation = gen.UsageAggregation_USAGE_AGGREGATION_MAX
	case business.UsageAggregationLast:
		aggregation = gen.UsageAggregation_USAGE_AGGREGATION_LAST
	}
	visibility := gen.UsageVisibility_USAGE_VISIBILITY_UNSPECIFIED
	switch meter.Visibility {
	case business.UsageVisibilityCustomer:
		visibility = gen.UsageVisibility_USAGE_VISIBILITY_CUSTOMER
	case business.UsageVisibilityOperator:
		visibility = gen.UsageVisibility_USAGE_VISIBILITY_OPERATOR
	}
	return &gen.UsageMeter{
		Key: meter.Key, DisplayName: meter.DisplayName, Unit: meter.Unit,
		Aggregation: aggregation, Owner: meter.Owner, Source: meter.Source,
		EntitlementKey:     meter.EntitlementKey,
		ReconciliationRule: meter.ReconciliationRule,
		Visibility:         visibility,
	}
}

func usageBucketFromProto(bucket gen.UsageBucketInterval) business.UsageBucketInterval {
	switch bucket {
	case gen.UsageBucketInterval_USAGE_BUCKET_INTERVAL_HOUR:
		return business.UsageBucketHour
	case gen.UsageBucketInterval_USAGE_BUCKET_INTERVAL_DAY:
		return business.UsageBucketDay
	case gen.UsageBucketInterval_USAGE_BUCKET_INTERVAL_MONTH:
		return business.UsageBucketMonth
	default:
		return ""
	}
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
