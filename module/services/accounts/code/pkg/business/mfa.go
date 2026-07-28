package business

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/codefly-dev/core/wool"

	gen "accounts/pkg/gen/saas/accounts/v1"
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

// SecretCipher encrypts high-value application secrets with a versioned
// envelope. Implementations must fail closed; plaintext fallback is forbidden.
type SecretCipher interface {
	EncryptSecret(ctx context.Context, purpose, plaintext string) (string, error)
	DecryptSecret(ctx context.Context, purpose, envelope string) (string, error)
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
	if s.mfaCipher == nil {
		return "", "", w.NewError("MFA secret cipher is not configured")
	}

	// Generate random secret.
	raw := make([]byte, totpSecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", "", w.Wrapf(err, "cannot generate TOTP secret")
	}
	secretB32 := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	encryptedSecret, err := s.mfaCipher.EncryptSecret(ctx, "mfa-totp", secretB32)
	if err != nil {
		return "", "", w.Wrapf(err, "cannot encrypt TOTP secret")
	}

	device := &MFADevice{
		ID:              NewIDString(),
		UserID:          userID,
		DeviceType:      "totp",
		Name:            "Authenticator",
		SecretEncrypted: encryptedSecret,
	}

	// User profile and MFA devices share the same user scope. Load the email
	// and create the device in one transaction rather than briefly widening
	// users visibility for provisioning.
	var user *gen.User
	if err := s.store.WithUserTx(ctx, userID, func(ctx context.Context) error {
		var err error
		user, err = s.store.GetUser(ctx, userID)
		if err != nil {
			return err
		}
		return mfaStore.CreateMFADevice(ctx, device)
	}); err != nil {
		return "", "", w.Wrapf(err, "cannot load user and create MFA device")
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
	if s.mfaCipher == nil {
		return w.NewError("MFA secret cipher is not configured")
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
			secretB32, err := s.mfaCipher.DecryptSecret(ctx, "mfa-totp", device.SecretEncrypted)
			if err != nil {
				w.Warn("failed to decrypt TOTP secret for device", wool.Field("device_id", device.ID), wool.ErrField(err))
				continue
			}
			secretBytes, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secretB32)
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
	var method string
	if err := s.store.WithUserTx(ctx, userID, func(ctx context.Context) error {
		var err error
		ok2, method, err = validateMFACodeInTx(ctx, mfaStore, s.mfaCipher, userID, code, time.Now())
		if err == nil && ok2 && method == "backup_code" {
			return s.store.CreateNotification(ctx, backupCodeUseNotification(userID))
		}
		return err
	}); err != nil {
		return false, err
	}
	if ok2 && method == "backup_code" {
		s.emit(ctx, userID, "user", "mfa.backup_code_used", "user", userID, "")
	}
	return ok2, nil
}

// validateMFACodeInTx validates and consumes a factor using the transaction
// already carried by ctx. Keeping the transaction boundary outside is
// load-bearing for login: the factor use, challenge consume, and session insert
// must commit or roll back together.
func validateMFACodeInTx(ctx context.Context, mfaStore MFAStore, cipher SecretCipher, userID, code string, now time.Time) (bool, string, error) {
	w := wool.Get(ctx).In("validateMFACodeInTx")
	if cipher == nil {
		return false, "", w.NewError("MFA secret cipher is not configured")
	}

	devices, err := mfaStore.ListMFADevices(ctx, userID)
	if err != nil {
		return false, "", w.Wrapf(err, "cannot list MFA devices")
	}
	for _, device := range devices {
		if device.DeviceType != "totp" || device.VerifiedAt == nil {
			continue
		}
		secretB32, err := cipher.DecryptSecret(ctx, "mfa-totp", device.SecretEncrypted)
		if err != nil {
			return false, "", w.Wrapf(err, "cannot decrypt TOTP secret")
		}
		secretBytes, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secretB32)
		if err != nil {
			continue
		}
		if validateTOTP(secretBytes, code, now) {
			device.LastUsedAt = &now
			if err := mfaStore.UpdateMFADevice(ctx, device); err != nil {
				return false, "", w.Wrapf(err, "cannot update MFA device use")
			}
			return true, "totp", nil
		}
	}

	backupCodes, err := mfaStore.GetUnusedBackupCodes(ctx, userID)
	if err != nil {
		return false, "", w.Wrapf(err, "cannot get backup codes")
	}
	for _, bc := range backupCodes {
		if bcrypt.CompareHashAndPassword([]byte(bc.CodeHash), []byte(code)) == nil {
			if err := mfaStore.UseBackupCode(ctx, bc.ID); err != nil {
				return false, "", w.Wrapf(err, "cannot consume backup code")
			}
			return true, "backup_code", nil
		}
	}
	return false, "", nil
}

func backupCodeUseNotification(userID string) *Notification {
	return &Notification{
		ID:        NewIDString(),
		UserID:    userID,
		Title:     "Recovery code used",
		Body:      "A one-time MFA recovery code was used to sign in. Generate a new set if this was not you.",
		Type:      "security",
		ActionURL: "/settings/security",
	}
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
