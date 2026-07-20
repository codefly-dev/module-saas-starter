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
