package business

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/codefly-dev/core/wool"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// Linked devices — generic external-device ↔ organization pairing.
//
// An org admin mints a short-lived claim code (only its SHA-256 hash is
// stored — same recipe as invitation tokens); the device redeems it with its
// public key. Claim admission enforces the 'paired_devices' entitlement
// inside the tenant transaction. CheckDeviceEntitlement resolves
// device_public_key → org → live subscription → the requested entitlement
// key and is the fail-closed service-to-service paywall probe.

const (
	// deviceClaimCodeTTL — claim codes are deliberately short-lived: they are
	// low-entropy (human-typable) and single-use.
	deviceClaimCodeTTL = 15 * time.Minute

	// deviceClaimCodeLength — 8 chars over a 32-symbol alphabet = 40 bits.
	deviceClaimCodeLength = 8

	// deviceClaimCodeAlphabet avoids visually ambiguous symbols (0/O, 1/I).
	deviceClaimCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)

// Device-entitlement decision reasons. Stable protocol vocabulary — external
// callers (e.g. the lazybox relay) branch on these strings.
const (
	DeviceEntitlementReasonSubscriptionInactive = "subscription_inactive"
	DeviceEntitlementReasonUnknownDevice        = "unknown_device"
	DeviceEntitlementReasonDeviceRevoked        = "device_revoked"
)

var (
	ErrDeviceClaimCodeInvalid = errors.New("claim code is invalid or expired")
	ErrDeviceAlreadyClaimed   = errors.New("device is already claimed by an organization")
	ErrDeviceNotFound         = errors.New("device not found")
)

// Device is one linked device row (linked_devices).
type Device struct {
	ID              string
	OrgID           string
	DevicePublicKey string
	Name            string
	CreatedBy       string
	CreatedAt       time.Time
	RevokedAt       *time.Time
}

// DeviceClaimCode is one hashed, expiring claim-code row (device_claim_codes).
type DeviceClaimCode struct {
	ID             string
	OrgID          string
	CodeHash       string
	CreatedBy      string
	Status         string
	ExpiresAt      time.Time
	CreatedAt      time.Time
	UsedAt         *time.Time
	UsedByDeviceID string
}

// newDeviceClaimCode returns (plaintext, hash). The plaintext leaves the
// process exactly once, in the CreateClaimCode response.
func newDeviceClaimCode() (string, string, error) {
	raw := make([]byte, deviceClaimCodeLength)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	code := make([]byte, deviceClaimCodeLength)
	for i, b := range raw {
		code[i] = deviceClaimCodeAlphabet[int(b)%len(deviceClaimCodeAlphabet)]
	}
	plaintext := string(code)
	return plaintext, HashDeviceClaimCode(plaintext), nil
}

// HashDeviceClaimCode canonicalizes and hashes a claim code. Codes are
// case-insensitive on input; only this hash is ever persisted.
func HashDeviceClaimCode(code string) string {
	normalized := strings.ToUpper(strings.TrimSpace(code))
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

// CreateDeviceClaimCode mints a short-lived, single-use claim code for the
// org. Authz expectation: caller is an org admin, gated by the adapter.
func (s *Service) CreateDeviceClaimCode(
	ctx context.Context,
	actorID string,
	req *gen.CreateDeviceClaimCodeRequest,
) (*gen.CreateDeviceClaimCodeResponse, error) {
	w := wool.Get(ctx).In("CreateDeviceClaimCode")

	plaintext, hash, err := newDeviceClaimCode()
	if err != nil {
		return nil, w.Wrapf(err, "cannot generate claim code")
	}
	now := time.Now().UTC()
	code := &DeviceClaimCode{
		ID:        NewIDString(),
		OrgID:     req.OrgId,
		CodeHash:  hash,
		CreatedBy: actorID,
		Status:    "pending",
		ExpiresAt: now.Add(deviceClaimCodeTTL),
		CreatedAt: now,
	}
	if err := s.store.WithOrgTx(ctx, req.OrgId, func(ctx context.Context) error {
		if err := s.store.ExpirePendingDeviceClaimCodes(ctx, req.OrgId); err != nil {
			return w.Wrapf(err, "cannot expire stale claim codes")
		}
		return s.store.CreateDeviceClaimCode(ctx, code)
	}); err != nil {
		return nil, w.Wrapf(err, "cannot create claim code")
	}

	s.emit(ctx, actorID, "user", "device.claim_code_created", "device_claim_code", code.ID, req.OrgId)

	return &gen.CreateDeviceClaimCodeResponse{
		Code:      plaintext,
		ExpiresAt: timestamppb.New(code.ExpiresAt),
	}, nil
}

// ClaimDevice redeems a claim code and registers the device to the code's
// org. Called by the device itself — no user session; the code is the
// credential. The paired_devices entitlement is enforced inside the same
// tenant transaction as the device insert (advisory-lock serialized, so two
// concurrent claims cannot both observe the last free slot).
func (s *Service) ClaimDevice(
	ctx context.Context,
	req *gen.ClaimDeviceRequest,
) (*gen.ClaimDeviceResponse, error) {
	w := wool.Get(ctx).In("ClaimDevice")
	hash := HashDeviceClaimCode(req.Code)

	// Resolve code → org under the audited control-plane scope; the claim
	// itself has no tenant context yet. Validity is re-checked inside the
	// tenant transaction below (this read is only for routing).
	var claim *DeviceClaimCode
	if err := s.store.As(System()).Within(ctx, func(ctx context.Context) error {
		var lookupErr error
		claim, lookupErr = s.store.GetDeviceClaimCodeByHash(ctx, hash)
		return lookupErr
	}); err != nil {
		return nil, w.Wrapf(err, "cannot resolve claim code")
	}
	now := time.Now().UTC()
	if claim == nil || claim.Status != "pending" || !claim.ExpiresAt.After(now) {
		return nil, ErrDeviceClaimCodeInvalid
	}

	device := &Device{
		ID:              NewIDString(),
		OrgID:           claim.OrgID,
		DevicePublicKey: strings.TrimSpace(req.DevicePublicKey),
		Name:            strings.TrimSpace(req.Name),
		CreatedBy:       claim.CreatedBy,
		CreatedAt:       now,
	}

	if err := s.store.WithOrgTx(ctx, claim.OrgID, func(ctx context.Context) error {
		// Re-read the code inside the tenant tx; single-use consumption is
		// guaranteed by MarkDeviceClaimCodeUsed's conditional update below, and
		// concurrent claims for the org serialize on the quota advisory lock.
		fresh, err := s.store.GetDeviceClaimCodeByHash(ctx, hash)
		if err != nil {
			return err
		}
		if fresh == nil || fresh.Status != "pending" || !fresh.ExpiresAt.After(now) {
			return ErrDeviceClaimCodeInvalid
		}

		// paired_devices admission — same advisory-lock recipe as seats and
		// api_keys, kept in this transaction with the authoritative count and
		// the insert.
		if err := s.store.LockEntitlementQuota(ctx, claim.OrgID, EntitlementPairedDevices); err != nil {
			return err
		}
		limit, err := resolveEffectiveLimitInTx(ctx, s.store, claim.OrgID, EntitlementPairedDevices, now)
		if err != nil {
			return err
		}
		used, err := s.store.CountActiveDevices(ctx, claim.OrgID)
		if err != nil {
			return err
		}
		quota := CardinalityQuota{Feature: EntitlementPairedDevices, Used: used, Limit: limit}
		if err := quota.RequireAvailable(); err != nil {
			return err
		}

		if err := s.store.CreateDevice(ctx, device); err != nil {
			return err
		}
		consumed, err := s.store.MarkDeviceClaimCodeUsed(ctx, fresh.ID, device.ID)
		if err != nil {
			return err
		}
		if !consumed {
			return ErrDeviceClaimCodeInvalid
		}
		return nil
	}); err != nil {
		var storeErr *StoreError
		if errors.As(err, &storeErr) && storeErr.StoreErrorType == ErrTypeConflict {
			return nil, ErrDeviceAlreadyClaimed
		}
		return nil, err
	}

	s.emit(ctx, claim.CreatedBy, "user", "device.claimed", "device", device.ID, claim.OrgID)

	return &gen.ClaimDeviceResponse{Device: deviceToProto(device)}, nil
}

// ListDevices returns every device linked to the org, newest first.
// Authz expectation: caller is an org member, gated by the adapter.
func (s *Service) ListDevices(
	ctx context.Context,
	req *gen.ListDevicesRequest,
) (*gen.ListDevicesResponse, error) {
	var devices []*Device
	if err := s.store.WithOrgTx(ctx, req.OrgId, func(ctx context.Context) error {
		var err error
		devices, err = s.store.ListDevices(ctx, req.OrgId)
		return err
	}); err != nil {
		return nil, wool.Get(ctx).In("ListDevices").Wrapf(err, "cannot list devices")
	}
	out := &gen.ListDevicesResponse{Devices: make([]*gen.Device, 0, len(devices))}
	for _, d := range devices {
		out.Devices = append(out.Devices, deviceToProto(d))
	}
	return out, nil
}

// RevokeDevice marks a device revoked. The entitlement check flips to
// active=false for that key immediately (same table, no cache).
// Authz expectation: caller is an org admin, gated by the adapter.
func (s *Service) RevokeDevice(
	ctx context.Context,
	actorID string,
	req *gen.RevokeDeviceRequest,
) (*emptypb.Empty, error) {
	var revoked bool
	if err := s.store.WithOrgTx(ctx, req.OrgId, func(ctx context.Context) error {
		var err error
		revoked, err = s.store.RevokeDevice(ctx, req.DeviceId, req.OrgId)
		return err
	}); err != nil {
		return nil, wool.Get(ctx).In("RevokeDevice").Wrapf(err, "cannot revoke device")
	}
	if !revoked {
		return nil, ErrDeviceNotFound
	}
	s.emit(ctx, actorID, "user", "device.revoked", "device", req.DeviceId, req.OrgId)
	return &emptypb.Empty{}, nil
}

// CheckDeviceEntitlement is the fail-closed entitlement probe:
// device_public_key → non-revoked device → org → effective plan (live
// active|trialing subscription, else the default free plan) → the requested
// entitlement key (override-aware). Unknown keys and unentitled orgs are
// decisions, not errors — callers treat any non-200 as deny, so only
// infrastructure failures may error. Authz expectation: API-key caller with
// scope entitlements:check, gated by the adapter.
func (s *Service) CheckDeviceEntitlement(
	ctx context.Context,
	req *gen.CheckDeviceEntitlementRequest,
) (*gen.CheckDeviceEntitlementResponse, error) {
	w := wool.Get(ctx).In("CheckDeviceEntitlement")
	checkedAt := timestamppb.Now()

	key := strings.TrimSpace(req.DevicePublicKey)

	// Key → device is inherently cross-tenant (the caller is product
	// infrastructure, not a tenant); audited control-plane read, then
	// everything tenant-scoped runs under the resolved org.
	var device *Device
	if err := s.store.As(System()).Within(ctx, func(ctx context.Context) error {
		var lookupErr error
		device, lookupErr = s.store.GetDeviceByPublicKey(ctx, key)
		return lookupErr
	}); err != nil {
		return nil, w.Wrapf(err, "cannot resolve device")
	}
	if device == nil {
		return &gen.CheckDeviceEntitlementResponse{
			Active:    false,
			Reason:    DeviceEntitlementReasonUnknownDevice,
			CheckedAt: checkedAt,
		}, nil
	}

	var planName string
	var limit int64
	now := time.Now().UTC()
	if err := s.store.WithOrgTx(ctx, device.OrgID, func(ctx context.Context) error {
		planID, err := s.store.GetOrgPlanID(ctx, device.OrgID)
		if err != nil {
			return err
		}
		plan, err := s.store.GetPlanByID(ctx, planID)
		if err != nil {
			return err
		}
		if plan != nil {
			planName = plan.Name
		}
		limit, err = resolveEffectiveLimitInTx(ctx, s.store, device.OrgID, req.EntitlementKey, now)
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot resolve device entitlement")
	}

	if device.RevokedAt != nil {
		return &gen.CheckDeviceEntitlementResponse{
			Active:    false,
			Plan:      planName,
			Reason:    DeviceEntitlementReasonDeviceRevoked,
			CheckedAt: checkedAt,
		}, nil
	}
	if limit == 0 {
		// 0 = disabled / not in plan: no live subscription on an entitled plan.
		return &gen.CheckDeviceEntitlementResponse{
			Active:    false,
			Plan:      planName,
			Reason:    DeviceEntitlementReasonSubscriptionInactive,
			CheckedAt: checkedAt,
		}, nil
	}
	return &gen.CheckDeviceEntitlementResponse{
		Active:    true,
		Plan:      planName,
		CheckedAt: checkedAt,
	}, nil
}

func deviceToProto(d *Device) *gen.Device {
	out := &gen.Device{
		Id:              d.ID,
		OrgId:           d.OrgID,
		DevicePublicKey: d.DevicePublicKey,
		Name:            d.Name,
		CreatedBy:       d.CreatedBy,
	}
	if !d.CreatedAt.IsZero() {
		out.CreatedAt = timestamppb.New(d.CreatedAt)
	}
	if d.RevokedAt != nil {
		out.RevokedAt = timestamppb.New(*d.RevokedAt)
	}
	return out
}
