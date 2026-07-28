package business

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

var (
	// ErrUsageIdempotencyConflict means an idempotency key was reused for a
	// semantically different consumption operation.
	ErrUsageIdempotencyConflict = errors.New("usage idempotency key was reused with a different request")
	ErrInvalidUsageRange        = errors.New("invalid usage range")
	usageMeterPattern           = regexp.MustCompile(`^[a-z][a-z0-9_]{0,127}$`)
	usageDimensionPattern       = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
)

// UsageConsumption is the persistence command for one immutable usage event.
// It is executed inside the caller's tenant transaction.
type UsageConsumption struct {
	EventID        string
	OrgID          string
	Meter          string
	Quantity       int64
	IdempotencyKey string
	RequestHash    [sha256.Size]byte
	OccurredAt     time.Time
	PeriodStart    time.Time
	PeriodEnd      time.Time
	Dimensions     map[string]string
	Limit          int64
}

// UsageReceipt is the stable result of a usage consumption attempt.
type UsageReceipt struct {
	EventID     string
	OrgID       string
	Meter       string
	Quantity    int64
	Accepted    bool
	Duplicate   bool
	Used        int64
	Limit       int64
	PeriodStart time.Time
	PeriodEnd   time.Time
	OccurredAt  time.Time
}

// UsageSnapshot is the current aggregate and effective entitlement limit.
type UsageSnapshot struct {
	OrgID       string
	Meter       string
	Used        int64
	Limit       int64
	PeriodStart time.Time
	PeriodEnd   time.Time
}

type UsageMeterSnapshot struct {
	Meter       UsageMeterDefinition
	Used        int64
	Limit       int64
	PeriodStart time.Time
	PeriodEnd   time.Time
}

type UsageBucketInterval string

const (
	UsageBucketHour  UsageBucketInterval = "hour"
	UsageBucketDay   UsageBucketInterval = "day"
	UsageBucketMonth UsageBucketInterval = "month"
)

type UsageBucketValue struct {
	Start    time.Time
	End      time.Time
	Quantity int64
}

type UsageHistory struct {
	OrgID  string
	Meter  string
	Bucket UsageBucketInterval
	From   time.Time
	To     time.Time
	Total  int64
	Values []UsageBucketValue
}

// ConsumeUsageInput is protocol-independent so internal Go callers and the
// protobuf adapter share exactly the same metering behavior.
type ConsumeUsageInput struct {
	OrgID          string
	Meter          string
	Quantity       int64
	IdempotencyKey string
	OccurredAt     *time.Time
	Dimensions     map[string]string
}

// ConsumeUsage atomically resolves the current entitlement, applies a hard
// monthly quota, and records an immutable accepted/rejected receipt.
func (s *Service) ConsumeUsage(ctx context.Context, in ConsumeUsageInput) (*UsageReceipt, error) {
	if in.OrgID == "" {
		return nil, errors.New("organization id is required")
	}
	if !usageMeterPattern.MatchString(in.Meter) {
		return nil, errors.New("meter must be a canonical lowercase identifier")
	}
	if in.Quantity <= 0 {
		return nil, errors.New("usage quantity must be positive")
	}
	if len(in.IdempotencyKey) == 0 || len(in.IdempotencyKey) > 255 {
		return nil, errors.New("usage idempotency key must contain 1 to 255 bytes")
	}
	if err := validateUsageDimensions(in.Dimensions); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	occurredAt := now
	if in.OccurredAt != nil {
		occurredAt = in.OccurredAt.UTC()
	}
	periodStart, periodEnd := monthlyUsagePeriod(occurredAt)
	requestHash, err := hashUsageRequest(in)
	if err != nil {
		return nil, fmt.Errorf("hash usage request: %w", err)
	}

	command := UsageConsumption{
		EventID:        NewIDString(),
		OrgID:          in.OrgID,
		Meter:          in.Meter,
		Quantity:       in.Quantity,
		IdempotencyKey: in.IdempotencyKey,
		RequestHash:    requestHash,
		OccurredAt:     occurredAt,
		PeriodStart:    periodStart,
		PeriodEnd:      periodEnd,
		Dimensions:     cloneUsageDimensions(in.Dimensions),
	}

	var receipt *UsageReceipt
	if err := s.store.WithOrgTx(ctx, in.OrgID, func(ctx context.Context) error {
		limit, err := resolveEffectiveLimitInTx(ctx, s.store, in.OrgID, in.Meter, now)
		if err != nil {
			return err
		}
		command.Limit = limit
		receipt, err = s.store.ConsumeUsage(ctx, command)
		return err
	}); err != nil {
		return nil, fmt.Errorf("consume usage: %w", err)
	}
	return receipt, nil
}

// GetUsage returns one meter's current UTC calendar-month aggregate and
// effective plan/override limit.
func (s *Service) GetUsage(ctx context.Context, orgID, meter string) (*UsageSnapshot, error) {
	if orgID == "" {
		return nil, errors.New("organization id is required")
	}
	if !usageMeterPattern.MatchString(meter) {
		return nil, errors.New("meter must be a canonical lowercase identifier")
	}

	now := time.Now().UTC()
	periodStart, periodEnd := monthlyUsagePeriod(now)
	out := &UsageSnapshot{
		OrgID: orgID, Meter: meter, PeriodStart: periodStart, PeriodEnd: periodEnd,
	}
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		limit, err := resolveEffectiveLimitInTx(ctx, s.store, orgID, meter, now)
		if err != nil {
			return err
		}
		used, err := s.store.GetUsageTotal(ctx, orgID, meter, periodStart)
		if err != nil {
			return err
		}
		out.Limit = limit
		out.Used = used
		return nil
	}); err != nil {
		return nil, fmt.Errorf("get usage: %w", err)
	}
	return out, nil
}

func (s *Service) ListUsageMeters(ctx context.Context, orgID string) ([]UsageMeterSnapshot, time.Time, error) {
	if orgID == "" {
		return nil, time.Time{}, errors.New("organization id is required")
	}
	observedAt := time.Now().UTC()
	periodStart, periodEnd := monthlyUsagePeriod(observedAt)
	definitions := s.usageMeters.Definitions(UsageVisibilityCustomer)
	snapshots := make([]UsageMeterSnapshot, 0, len(definitions))
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		for _, definition := range definitions {
			limit, err := resolveEffectiveLimitInTx(
				ctx, s.store, orgID, definition.EntitlementKey, observedAt,
			)
			if err != nil {
				return err
			}
			used, err := s.store.GetUsageTotal(ctx, orgID, definition.Key, periodStart)
			if err != nil {
				return err
			}
			snapshots = append(snapshots, UsageMeterSnapshot{
				Meter: definition, Used: used, Limit: limit,
				PeriodStart: periodStart, PeriodEnd: periodEnd,
			})
		}
		return nil
	}); err != nil {
		return nil, time.Time{}, fmt.Errorf("list usage meters: %w", err)
	}
	return snapshots, observedAt, nil
}

func (s *Service) GetUsageHistory(
	ctx context.Context,
	orgID string,
	meter string,
	from time.Time,
	to time.Time,
	bucket UsageBucketInterval,
) (*UsageHistory, time.Time, error) {
	if orgID == "" {
		return nil, time.Time{}, errors.New("organization id is required")
	}
	if !usageMeterPattern.MatchString(meter) {
		return nil, time.Time{}, errors.New("meter must be a canonical lowercase identifier")
	}
	from = from.UTC()
	to = to.UTC()
	if from.IsZero() || to.IsZero() || !from.Before(to) {
		return nil, time.Time{}, fmt.Errorf("%w: from must be before to", ErrInvalidUsageRange)
	}
	if to.Sub(from) > 366*24*time.Hour {
		return nil, time.Time{}, fmt.Errorf("%w: range must not exceed 366 days", ErrInvalidUsageRange)
	}
	if bucket != UsageBucketHour && bucket != UsageBucketDay && bucket != UsageBucketMonth {
		return nil, time.Time{}, fmt.Errorf("%w: bucket must be hour, day, or month", ErrInvalidUsageRange)
	}
	firstBucket := floorUsageBucket(from, bucket)
	bucketCount := 0
	for at := firstBucket; at.Before(to); at = nextUsageBucket(at, bucket) {
		bucketCount++
		if bucketCount > 1000 {
			return nil, time.Time{}, fmt.Errorf("%w: range produces more than 1000 buckets", ErrInvalidUsageRange)
		}
	}

	var stored []UsageBucketValue
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		stored, err = s.store.GetUsageBuckets(ctx, orgID, meter, from, to, bucket)
		return err
	}); err != nil {
		return nil, time.Time{}, fmt.Errorf("get usage history: %w", err)
	}
	quantities := make(map[time.Time]int64, len(stored))
	for _, value := range stored {
		quantities[value.Start.UTC()] = value.Quantity
	}
	history := &UsageHistory{
		OrgID: orgID, Meter: meter, Bucket: bucket, From: from, To: to,
		Values: make([]UsageBucketValue, 0, bucketCount),
	}
	for start := firstBucket; start.Before(to); start = nextUsageBucket(start, bucket) {
		end := nextUsageBucket(start, bucket)
		displayStart := start
		if displayStart.Before(from) {
			displayStart = from
		}
		displayEnd := end
		if displayEnd.After(to) {
			displayEnd = to
		}
		quantity := quantities[start]
		history.Total += quantity
		history.Values = append(history.Values, UsageBucketValue{
			Start: displayStart, End: displayEnd, Quantity: quantity,
		})
	}
	return history, time.Now().UTC(), nil
}

func floorUsageBucket(at time.Time, bucket UsageBucketInterval) time.Time {
	at = at.UTC()
	switch bucket {
	case UsageBucketHour:
		return at.Truncate(time.Hour)
	case UsageBucketDay:
		return time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
	case UsageBucketMonth:
		return time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
	default:
		return at
	}
}

func nextUsageBucket(at time.Time, bucket UsageBucketInterval) time.Time {
	switch bucket {
	case UsageBucketHour:
		return at.Add(time.Hour)
	case UsageBucketDay:
		return at.AddDate(0, 0, 1)
	case UsageBucketMonth:
		return at.AddDate(0, 1, 0)
	default:
		return at
	}
}

func monthlyUsagePeriod(at time.Time) (time.Time, time.Time) {
	at = at.UTC()
	start := time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 1, 0)
}

func hashUsageRequest(in ConsumeUsageInput) ([sha256.Size]byte, error) {
	// encoding/json orders map keys, making this hash stable across retries.
	canonical := struct {
		Meter      string            `json:"meter"`
		Quantity   int64             `json:"quantity"`
		OccurredAt string            `json:"occurred_at,omitempty"`
		Dimensions map[string]string `json:"dimensions,omitempty"`
	}{
		Meter: in.Meter, Quantity: in.Quantity, Dimensions: in.Dimensions,
	}
	if in.OccurredAt != nil {
		canonical.OccurredAt = in.OccurredAt.UTC().Format(time.RFC3339Nano)
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

func cloneUsageDimensions(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func validateUsageDimensions(dimensions map[string]string) error {
	if len(dimensions) > 32 {
		return errors.New("usage dimensions must contain at most 32 entries")
	}
	for key, value := range dimensions {
		if len(key) == 0 || len(key) > 64 || !usageDimensionPattern.MatchString(key) {
			return fmt.Errorf("usage dimension key %q is not canonical", key)
		}
		if len(value) > 256 {
			return fmt.Errorf("usage dimension %q exceeds 256 bytes", key)
		}
	}
	return nil
}
