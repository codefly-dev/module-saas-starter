package business

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/codefly-dev/core/wool"
)

// ── MFA domain types ───────────────────────────────────────────

// MFADevice represents a registered multi-factor authentication device.
type MFADevice struct {
	ID              string
	UserID          string
	DeviceType      string // "totp"
	Name            string
	SecretEncrypted string
	VerifiedAt      *time.Time
	LastUsedAt      *time.Time
	CreatedAt       time.Time
}

// MFABackupCode represents a single backup code for MFA recovery.
type MFABackupCode struct {
	ID       string
	UserID   string
	CodeHash string
	UsedAt   *time.Time
}

// MFAStore abstracts persistence for MFA devices and backup codes.
type MFAStore interface {
	CreateMFADevice(ctx context.Context, device *MFADevice) error
	GetMFADevice(ctx context.Context, id string) (*MFADevice, error)
	ListMFADevices(ctx context.Context, userID string) ([]*MFADevice, error)
	DeleteMFADevice(ctx context.Context, id string) error
	UpdateMFADevice(ctx context.Context, device *MFADevice) error
	HasVerifiedMFA(ctx context.Context, userID string) (bool, error)
	CreateBackupCodes(ctx context.Context, codes []*MFABackupCode) error
	GetUnusedBackupCodes(ctx context.Context, userID string) ([]*MFABackupCode, error)
	UseBackupCode(ctx context.Context, id string) error
	DeleteBackupCodes(ctx context.Context, userID string) error
}

const (
	totpSecretBytes = 32
	totpPeriod      = 30
	totpDigits      = 6
	totpSkew        = 1 // ±1 time step tolerance
	backupCodeCount = 10
	backupCodeLen   = 8
	mfaAppName      = "SaaSStarter"
)

// ── TOTP algorithm (RFC 6238) ──────────────────────────────────

func generateTOTP(secret []byte, t time.Time) string {
	counter := uint64(t.Unix()) / totpPeriod
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, counter)
	mac := hmac.New(sha1.New, secret)
	mac.Write(buf)
	hash := mac.Sum(nil)
	offset := hash[len(hash)-1] & 0xf
	code := binary.BigEndian.Uint32(hash[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", code%1000000)
}

func validateTOTP(secret []byte, code string, t time.Time) bool {
	for i := -totpSkew; i <= totpSkew; i++ {
		candidate := generateTOTP(secret, t.Add(time.Duration(i)*totpPeriod*time.Second))
		if candidate == code {
			return true
		}
	}
	return false
}

// ── Business logic ─────────────────────────────────────────────

// SetupTOTP generates a new TOTP secret and creates an unverified device.
// Returns the base32-encoded secret and the otpauth:// provisioning URI.
func (s *Service) SetupTOTP(ctx context.Context, userID string) (secret string, provisioningURI string, err error) {
	w := wool.Get(ctx).In("SetupTOTP")

	mfaStore, ok := s.store.(MFAStore)
	if !ok {
		return "", "", w.NewError("store does not implement MFAStore")
	}

	// Generate random secret.
	raw := make([]byte, totpSecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", "", w.Wrapf(err, "cannot generate TOTP secret")
	}
	secretB32 := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)

	// Look up user email for the provisioning URI.
	user, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return "", "", w.Wrapf(err, "cannot get user for TOTP setup")
	}

	device := &MFADevice{
		ID:              NewIDString(),
		UserID:          userID,
		DeviceType:      "totp",
		Name:            "Authenticator",
		SecretEncrypted: secretB32, // In production, encrypt before storing.
	}

	// mfa_devices is RLS-protected by user_id (Phase 2G).
	if err := s.store.WithUserTx(ctx, userID, func(ctx context.Context) error {
		return mfaStore.CreateMFADevice(ctx, device)
	}); err != nil {
		return "", "", w.Wrapf(err, "cannot create MFA device")
	}

	// Build otpauth:// URI per https://github.com/google/google-authenticator/wiki/Key-Uri-Format
	uri := fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s&digits=%d&period=%d",
		mfaAppName, user.PrimaryEmail, secretB32, mfaAppName, totpDigits, totpPeriod)

	s.emit(ctx, userID, "user", "mfa.totp_setup_started", "mfa_device", device.ID, "")

	return secretB32, uri, nil
}

// VerifyTOTP validates a TOTP code against the user's unverified device
// and marks the device as verified on success.
func (s *Service) VerifyTOTP(ctx context.Context, userID, code string) error {
	w := wool.Get(ctx).In("VerifyTOTP")

	mfaStore, ok := s.store.(MFAStore)
	if !ok {
		return w.NewError("store does not implement MFAStore")
	}

	// All reads + writes for this verify cycle run inside ONE
	// WithUserTx so the listing, the per-device update, and the
	// audit emit all see the user GUC. Without the wrap, RLS would
	// hide the user's own devices (fail-closed) and the verify
	// would always fail.
	if err := s.store.WithUserTx(ctx, userID, func(ctx context.Context) error {
		devices, err := mfaStore.ListMFADevices(ctx, userID)
		if err != nil {
			return w.Wrapf(err, "cannot list MFA devices")
		}
		for _, device := range devices {
			if device.DeviceType != "totp" || device.VerifiedAt != nil {
				continue
			}
			secretBytes, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(device.SecretEncrypted)
			if err != nil {
				w.Warn("failed to decode TOTP secret for device", wool.Field("device_id", device.ID), wool.ErrField(err))
				continue
			}
			if validateTOTP(secretBytes, code, time.Now()) {
				now := time.Now()
				device.VerifiedAt = &now
				device.LastUsedAt = &now
				if err := mfaStore.UpdateMFADevice(ctx, device); err != nil {
					return w.Wrapf(err, "cannot mark device as verified")
				}
				s.emit(ctx, userID, "user", "mfa.totp_verified", "mfa_device", device.ID, "")
				return nil
			}
		}
		return w.NewError("invalid TOTP code")
	}); err != nil {
		return err
	}
	return nil
}

// ListMFADevices returns all MFA devices for a user.
func (s *Service) ListMFADevices(ctx context.Context, userID string) ([]*MFADevice, error) {
	w := wool.Get(ctx).In("ListMFADevices")

	mfaStore, ok := s.store.(MFAStore)
	if !ok {
		return nil, w.NewError("store does not implement MFAStore")
	}

	var devices []*MFADevice
	if err := s.store.WithUserTx(ctx, userID, func(ctx context.Context) error {
		ds, err := mfaStore.ListMFADevices(ctx, userID)
		devices = ds
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot list MFA devices")
	}
	return devices, nil
}

// RevokeMFADevice removes an MFA device after verifying ownership.
// Both reads (Get) and the delete run inside one WithUserTx so the
// existence check and the mutation see the same RLS policy state.
func (s *Service) RevokeMFADevice(ctx context.Context, userID, deviceID string) error {
	w := wool.Get(ctx).In("RevokeMFADevice")

	mfaStore, ok := s.store.(MFAStore)
	if !ok {
		return w.NewError("store does not implement MFAStore")
	}

	if err := s.store.WithUserTx(ctx, userID, func(ctx context.Context) error {
		device, err := mfaStore.GetMFADevice(ctx, deviceID)
		if err != nil {
			return w.Wrapf(err, "cannot get MFA device")
		}
		if device.UserID != userID {
			return w.NewError("device does not belong to user")
		}
		return mfaStore.DeleteMFADevice(ctx, deviceID)
	}); err != nil {
		return w.Wrapf(err, "cannot delete MFA device")
	}

	s.emit(ctx, userID, "user", "mfa.device_revoked", "mfa_device", deviceID, "")
	return nil
}

// GenerateBackupCodes creates a fresh set of backup codes for a user,
// replacing any existing codes. Returns the plaintext codes (shown once).
func (s *Service) GenerateBackupCodes(ctx context.Context, userID string) ([]string, error) {
	w := wool.Get(ctx).In("GenerateBackupCodes")

	mfaStore, ok := s.store.(MFAStore)
	if !ok {
		return nil, w.NewError("store does not implement MFAStore")
	}

	plaintextCodes := make([]string, backupCodeCount)
	codes := make([]*MFABackupCode, backupCodeCount)

	for i := 0; i < backupCodeCount; i++ {
		plain := randomAlphanumeric(backupCodeLen)
		hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
		if err != nil {
			return nil, w.Wrapf(err, "cannot hash backup code")
		}
		plaintextCodes[i] = plain
		codes[i] = &MFABackupCode{
			ID:       NewIDString(),
			UserID:   userID,
			CodeHash: string(hash),
		}
	}

	// Delete existing + insert new under one WithUserTx so the
	// rotate is atomic from RLS's POV (no momentary "no codes" gap
	// visible across other transactions).
	if err := s.store.WithUserTx(ctx, userID, func(ctx context.Context) error {
		if err := mfaStore.DeleteBackupCodes(ctx, userID); err != nil {
			return w.Wrapf(err, "cannot delete existing backup codes")
		}
		return mfaStore.CreateBackupCodes(ctx, codes)
	}); err != nil {
		return nil, w.Wrapf(err, "cannot create backup codes")
	}

	s.emit(ctx, userID, "user", "mfa.backup_codes_generated", "user", userID, "")
	return plaintextCodes, nil
}

// ValidateMFACode checks if the given code is a valid TOTP code or
// a valid backup code for the user. Returns true on success.
func (s *Service) ValidateMFACode(ctx context.Context, userID, code string) (bool, error) {
	w := wool.Get(ctx).In("ValidateMFACode")

	mfaStore, ok := s.store.(MFAStore)
	if !ok {
		return false, w.NewError("store does not implement MFAStore")
	}

	var ok2 bool
	if err := s.store.WithUserTx(ctx, userID, func(ctx context.Context) error {
		// Try TOTP first.
		devices, err := mfaStore.ListMFADevices(ctx, userID)
		if err != nil {
			return w.Wrapf(err, "cannot list MFA devices")
		}
		for _, device := range devices {
			if device.DeviceType != "totp" || device.VerifiedAt == nil {
				continue
			}
			secretBytes, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(device.SecretEncrypted)
			if err != nil {
				continue
			}
			if validateTOTP(secretBytes, code, time.Now()) {
				now := time.Now()
				device.LastUsedAt = &now
				_ = mfaStore.UpdateMFADevice(ctx, device)
				ok2 = true
				return nil
			}
		}
		// Try backup codes.
		backupCodes, err := mfaStore.GetUnusedBackupCodes(ctx, userID)
		if err != nil {
			return w.Wrapf(err, "cannot get backup codes")
		}
		for _, bc := range backupCodes {
			if bcrypt.CompareHashAndPassword([]byte(bc.CodeHash), []byte(code)) == nil {
				if err := mfaStore.UseBackupCode(ctx, bc.ID); err != nil {
					w.Warn("failed to mark backup code as used", wool.ErrField(err))
				}
				ok2 = true
				return nil
			}
		}
		return nil
	}); err != nil {
		return false, err
	}
	return ok2, nil
}

// UserHasMFA returns true if the user has at least one verified MFA device.
func (s *Service) UserHasMFA(ctx context.Context, userID string) (bool, error) {
	w := wool.Get(ctx).In("UserHasMFA")

	mfaStore, ok := s.store.(MFAStore)
	if !ok {
		return false, w.NewError("store does not implement MFAStore")
	}

	var has bool
	if err := s.store.WithUserTx(ctx, userID, func(ctx context.Context) error {
		h, err := mfaStore.HasVerifiedMFA(ctx, userID)
		has = h
		return err
	}); err != nil {
		return false, w.Wrapf(err, "cannot check MFA status")
	}
	return has, nil
}

// MintMFAPendingToken creates a short-lived (5 min) JWT with an
// mfa_pending claim, indicating the user has authenticated but still
// needs to complete MFA verification.
func (s *Service) MintMFAPendingToken(ctx context.Context, userID string) (string, error) {
	w := wool.Get(ctx).In("MintMFAPendingToken")

	if s.minter == nil {
		return "", w.NewError("JWT minter not configured")
	}

	// We create a minimal identity — no org/role — with a special
	// platform-role marker that the auth middleware can check.
	// The token will be very short-lived (handled by the minter's
	// access TTL). For a 5 min token we mint via the standard path
	// but callers should configure a short-lived minter or check
	// the mfa_pending claim downstream.
	//
	// For now, we return a SHA-256 hash of the user ID + timestamp
	// as a simple opaque token. A full implementation would use a
	// dedicated JWT with an mfa_pending claim.
	now := time.Now()
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", userID, now.UnixNano())))
	token := fmt.Sprintf("mfa_%s", base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hash[:]))

	return token, nil
}

// randomAlphanumeric generates a random alphanumeric string of length n.
func randomAlphanumeric(n int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("business: failed to read random bytes: %v", err))
	}
	for i := range b {
		b[i] = charset[b[i]%byte(len(charset))]
	}
	return strings.ToUpper(string(b))
}
