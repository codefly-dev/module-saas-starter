package business

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/wool"
	"github.com/google/uuid"
)

const (
	webAuthnCeremonyTTL       = 5 * time.Minute
	webAuthnCredentialPurpose = "mfa-webauthn-credential"
	webAuthnSessionPurpose    = "mfa-webauthn-session"
)

var ErrWebAuthnCeremonyRejected = errors.New("WebAuthn ceremony rejected")

// WebAuthnUser is the protocol-facing user projection. CredentialJSON entries
// are decrypted complete credential records; they are never returned to a
// caller or persisted in plaintext.
type WebAuthnUser struct {
	ID             []byte
	Name           string
	DisplayName    string
	CredentialJSON [][]byte
}

// WebAuthnCredentialResult contains the complete authenticator record returned
// after verification. ID is intentionally separate so storage can enforce a
// global uniqueness constraint without decrypting every credential.
type WebAuthnCredentialResult struct {
	ID             []byte
	CredentialJSON []byte
}

// WebAuthnEngine isolates the current WebAuthn protocol implementation from
// transaction and persistence logic. Production uses go-webauthn; tests can
// exercise all one-use and atomicity rules with a deterministic engine.
type WebAuthnEngine interface {
	BeginRegistration(ctx context.Context, user WebAuthnUser) (optionsJSON, sessionJSON []byte, expiresAt time.Time, err error)
	FinishRegistration(ctx context.Context, user WebAuthnUser, sessionJSON, responseJSON []byte) (*WebAuthnCredentialResult, error)
	BeginLogin(ctx context.Context, user WebAuthnUser) (optionsJSON, sessionJSON []byte, expiresAt time.Time, err error)
	FinishLogin(ctx context.Context, user WebAuthnUser, sessionJSON, responseJSON []byte) (*WebAuthnCredentialResult, error)
}

type StoredWebAuthnCredential struct {
	DeviceID            string
	UserID              string
	CredentialID        []byte
	CredentialEncrypted string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type WebAuthnCeremony struct {
	ID                    string
	TokenHash             string
	UserID                string
	MFALoginTransactionID string
	CeremonyType          string
	SessionDataEncrypted  string
	ExpiresAt             time.Time
	ConsumedAt            *time.Time
	CreatedAt             time.Time
}

type WebAuthnStore interface {
	CreateWebAuthnCeremony(ctx context.Context, ceremony *WebAuthnCeremony) error
	GetWebAuthnCeremonyForUpdate(ctx context.Context, tokenHash, userID, ceremonyType, mfaLoginTransactionID string, now time.Time) (*WebAuthnCeremony, error)
	ConsumeWebAuthnCeremony(ctx context.Context, id string, now time.Time) error
	ListWebAuthnCredentials(ctx context.Context, userID string, forUpdate bool) ([]*StoredWebAuthnCredential, error)
	CreateWebAuthnCredential(ctx context.Context, device *MFADevice, credential *StoredWebAuthnCredential) error
	UpdateWebAuthnCredential(ctx context.Context, credential *StoredWebAuthnCredential, lastUsedAt time.Time) error
}

func (s *Service) SetWebAuthnEngine(engine WebAuthnEngine) {
	s.webAuthn = engine
}

func (s *Service) BeginWebAuthnRegistration(ctx context.Context, userID string) (ceremonyToken string, optionsJSON string, err error) {
	w := wool.Get(ctx).In("BeginWebAuthnRegistration")
	store, ok := s.store.(WebAuthnStore)
	if !ok {
		return "", "", w.NewError("store does not implement WebAuthnStore")
	}
	if s.webAuthn == nil || s.mfaCipher == nil {
		return "", "", w.NewError("WebAuthn is not configured")
	}

	token, tokenHash, err := newMFALoginToken()
	if err != nil {
		return "", "", w.Wrapf(err, "cannot generate WebAuthn ceremony token")
	}

	var options []byte
	err = s.store.WithUserTx(ctx, userID, func(txCtx context.Context) error {
		user, err := s.webAuthnUserInTx(txCtx, store, userID, false)
		if err != nil {
			return err
		}
		var session []byte
		var expiresAt time.Time
		options, session, expiresAt, err = s.webAuthn.BeginRegistration(txCtx, user)
		if err != nil {
			return w.Wrapf(err, "begin WebAuthn registration")
		}
		if expiresAt.IsZero() || expiresAt.After(time.Now().Add(webAuthnCeremonyTTL)) {
			expiresAt = time.Now().Add(webAuthnCeremonyTTL)
		}
		encrypted, err := s.mfaCipher.EncryptSecret(txCtx, webAuthnSessionPurpose, string(session))
		if err != nil {
			return w.Wrapf(err, "encrypt WebAuthn registration state")
		}
		return store.CreateWebAuthnCeremony(txCtx, &WebAuthnCeremony{
			ID:                   NewIDString(),
			TokenHash:            tokenHash,
			UserID:               userID,
			CeremonyType:         "registration",
			SessionDataEncrypted: encrypted,
			ExpiresAt:            expiresAt,
			CreatedAt:            time.Now(),
		})
	})
	if err != nil {
		return "", "", err
	}

	s.emit(ctx, userID, "user", EventMFAWebAuthnRegStarted, "user", userID, "")
	return token, string(options), nil
}

func (s *Service) FinishWebAuthnRegistration(ctx context.Context, userID, ceremonyToken, responseJSON, name string) (*MFADevice, error) {
	w := wool.Get(ctx).In("FinishWebAuthnRegistration")
	store, ok := s.store.(WebAuthnStore)
	if !ok {
		return nil, w.NewError("store does not implement WebAuthnStore")
	}
	if s.webAuthn == nil || s.mfaCipher == nil || ceremonyToken == "" || responseJSON == "" {
		return nil, ErrWebAuthnCeremonyRejected
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Passkey"
	}

	now := time.Now()
	var device *MFADevice
	err := s.store.WithUserTx(ctx, userID, func(txCtx context.Context) error {
		ceremony, err := store.GetWebAuthnCeremonyForUpdate(txCtx, hashMFALoginToken(ceremonyToken), userID, "registration", "", now)
		if err != nil {
			return ErrWebAuthnCeremonyRejected
		}
		sessionJSON, err := s.mfaCipher.DecryptSecret(txCtx, webAuthnSessionPurpose, ceremony.SessionDataEncrypted)
		if err != nil {
			return w.Wrapf(err, "decrypt WebAuthn registration state")
		}
		user, err := s.webAuthnUserInTx(txCtx, store, userID, true)
		if err != nil {
			return err
		}
		result, err := s.webAuthn.FinishRegistration(txCtx, user, []byte(sessionJSON), []byte(responseJSON))
		if err != nil || result == nil || len(result.ID) == 0 || len(result.CredentialJSON) == 0 {
			return ErrWebAuthnCeremonyRejected
		}
		encrypted, err := s.mfaCipher.EncryptSecret(txCtx, webAuthnCredentialPurpose, string(result.CredentialJSON))
		if err != nil {
			return w.Wrapf(err, "encrypt WebAuthn credential")
		}
		device = &MFADevice{
			ID:         NewIDString(),
			UserID:     userID,
			DeviceType: "webauthn",
			Name:       name,
			VerifiedAt: &now,
			LastUsedAt: &now,
		}
		credential := &StoredWebAuthnCredential{
			DeviceID:            device.ID,
			UserID:              userID,
			CredentialID:        append([]byte(nil), result.ID...),
			CredentialEncrypted: encrypted,
			CreatedAt:           now,
			UpdatedAt:           now,
		}
		if err := store.CreateWebAuthnCredential(txCtx, device, credential); err != nil {
			return w.Wrapf(err, "persist WebAuthn credential")
		}
		return store.ConsumeWebAuthnCeremony(txCtx, ceremony.ID, now)
	})
	if err != nil {
		if errors.Is(err, ErrWebAuthnCeremonyRejected) {
			return nil, ErrWebAuthnCeremonyRejected
		}
		return nil, err
	}

	s.emit(ctx, userID, "user", EventMFAWebAuthnRegistered, "mfa_device", device.ID, "")
	return device, nil
}

func (s *Service) BeginWebAuthnMFAChallenge(ctx context.Context, mfaToken string) (ceremonyToken string, optionsJSON string, err error) {
	w := wool.Get(ctx).In("BeginWebAuthnMFAChallenge")
	loginStore, ok := s.store.(MFALoginStore)
	if !ok {
		return "", "", w.NewError("store does not implement MFALoginStore")
	}
	store, ok := s.store.(WebAuthnStore)
	if !ok || s.webAuthn == nil || s.mfaCipher == nil || mfaToken == "" {
		return "", "", ErrMFAChallengeRejected
	}
	tx, err := loginStore.GetActiveMFALoginTransaction(ctx, hashMFALoginToken(mfaToken), time.Now())
	if err != nil || tx == nil {
		return "", "", ErrMFAChallengeRejected
	}

	token, tokenHash, err := newMFALoginToken()
	if err != nil {
		return "", "", w.Wrapf(err, "cannot generate WebAuthn ceremony token")
	}
	var options []byte
	err = s.store.WithUserTx(ctx, tx.UserID, func(txCtx context.Context) error {
		user, err := s.webAuthnUserInTx(txCtx, store, tx.UserID, false)
		if err != nil || len(user.CredentialJSON) == 0 {
			return ErrMFAChallengeRejected
		}
		var session []byte
		var expiresAt time.Time
		options, session, expiresAt, err = s.webAuthn.BeginLogin(txCtx, user)
		if err != nil {
			return ErrMFAChallengeRejected
		}
		if expiresAt.IsZero() || expiresAt.After(time.Now().Add(webAuthnCeremonyTTL)) {
			expiresAt = time.Now().Add(webAuthnCeremonyTTL)
		}
		encrypted, err := s.mfaCipher.EncryptSecret(txCtx, webAuthnSessionPurpose, string(session))
		if err != nil {
			return w.Wrapf(err, "encrypt WebAuthn login state")
		}
		return store.CreateWebAuthnCeremony(txCtx, &WebAuthnCeremony{
			ID:                    NewIDString(),
			TokenHash:             tokenHash,
			UserID:                tx.UserID,
			MFALoginTransactionID: tx.ID,
			CeremonyType:          "login",
			SessionDataEncrypted:  encrypted,
			ExpiresAt:             expiresAt,
			CreatedAt:             time.Now(),
		})
	})
	if err != nil {
		if errors.Is(err, ErrMFAChallengeRejected) {
			return "", "", ErrMFAChallengeRejected
		}
		return "", "", err
	}
	return token, string(options), nil
}

func (s *Service) CompleteWebAuthnMFAChallenge(ctx context.Context, mfaToken, ceremonyToken, responseJSON string) (*gen.CompleteMFAChallengeResponse, error) {
	w := wool.Get(ctx).In("CompleteWebAuthnMFAChallenge")
	loginStore, ok := s.store.(MFALoginStore)
	if !ok {
		return nil, w.NewError("store does not implement MFALoginStore")
	}
	store, ok := s.store.(WebAuthnStore)
	if !ok || s.webAuthn == nil || s.mfaCipher == nil || s.minter == nil || mfaToken == "" || ceremonyToken == "" || responseJSON == "" {
		return nil, ErrMFAChallengeRejected
	}

	var pair *auth.TokenPair
	var consumed *MFALoginTransaction
	now := time.Now()
	err := loginStore.ConsumeMFALoginTransaction(ctx, hashMFALoginToken(mfaToken), now, func(txCtx context.Context, tx *MFALoginTransaction) error {
		ceremony, err := store.GetWebAuthnCeremonyForUpdate(txCtx, hashMFALoginToken(ceremonyToken), tx.UserID, "login", tx.ID, now)
		if err != nil {
			return ErrMFAChallengeRejected
		}
		sessionJSON, err := s.mfaCipher.DecryptSecret(txCtx, webAuthnSessionPurpose, ceremony.SessionDataEncrypted)
		if err != nil {
			return w.Wrapf(err, "decrypt WebAuthn login state")
		}
		user, records, err := s.webAuthnUserAndRecordsInTx(txCtx, store, tx.UserID, true)
		if err != nil || len(records) == 0 {
			return ErrMFAChallengeRejected
		}
		result, err := s.webAuthn.FinishLogin(txCtx, user, []byte(sessionJSON), []byte(responseJSON))
		if err != nil || result == nil || len(result.ID) == 0 || len(result.CredentialJSON) == 0 {
			return ErrMFAChallengeRejected
		}
		var matched *StoredWebAuthnCredential
		for _, record := range records {
			if len(record.CredentialID) == len(result.ID) && subtle.ConstantTimeCompare(record.CredentialID, result.ID) == 1 {
				matched = record
				break
			}
		}
		if matched == nil {
			return ErrMFAChallengeRejected
		}
		matched.CredentialEncrypted, err = s.mfaCipher.EncryptSecret(txCtx, webAuthnCredentialPurpose, string(result.CredentialJSON))
		if err != nil {
			return w.Wrapf(err, "encrypt updated WebAuthn credential")
		}
		if err := store.UpdateWebAuthnCredential(txCtx, matched, now); err != nil {
			return w.Wrapf(err, "update WebAuthn credential")
		}
		if err := store.ConsumeWebAuthnCeremony(txCtx, ceremony.ID, now); err != nil {
			return ErrMFAChallengeRejected
		}
		pair, err = s.mintMFASessionInTx(txCtx, tx, auth.AuthenticationMethodWebAuthn, now)
		if err != nil {
			return err
		}
		consumed = tx
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrMFAChallengeRejected) {
			return nil, ErrMFAChallengeRejected
		}
		return nil, w.Wrapf(err, "complete WebAuthn MFA login transaction")
	}
	if pair == nil || consumed == nil {
		return nil, w.NewError("WebAuthn MFA login completed without a session")
	}

	user, err := s.getUserAsSelf(ctx, consumed.UserID)
	if err != nil || user == nil {
		user = &gen.User{Uuid: consumed.UserID}
	}
	s.emit(ctx, consumed.UserID, "user", EventAuthLogin, "session", consumed.SessionID, consumed.OrgID)
	s.emit(ctx, consumed.UserID, "user", EventAuthMFAChallengeDone, "mfa_login_transaction", consumed.ID, consumed.OrgID)
	s.emit(ctx, consumed.UserID, "user", EventMFAWebAuthnUsed, "user", consumed.UserID, consumed.OrgID)
	return completeMFAResponse(pair, user), nil
}

func (s *Service) webAuthnUserInTx(ctx context.Context, store WebAuthnStore, userID string, forUpdate bool) (WebAuthnUser, error) {
	user, _, err := s.webAuthnUserAndRecordsInTx(ctx, store, userID, forUpdate)
	return user, err
}

func (s *Service) webAuthnUserAndRecordsInTx(ctx context.Context, store WebAuthnStore, userID string, forUpdate bool) (WebAuthnUser, []*StoredWebAuthnCredential, error) {
	user, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return WebAuthnUser{}, nil, err
	}
	id, err := uuid.Parse(userID)
	if err != nil {
		return WebAuthnUser{}, nil, fmt.Errorf("invalid WebAuthn user id: %w", err)
	}
	displayName := strings.TrimSpace(user.Profile["name"])
	if displayName == "" {
		displayName = user.PrimaryEmail
	}
	out := WebAuthnUser{ID: id[:], Name: user.PrimaryEmail, DisplayName: displayName}
	records, err := store.ListWebAuthnCredentials(ctx, userID, forUpdate)
	if err != nil {
		return WebAuthnUser{}, nil, err
	}
	for _, record := range records {
		plaintext, err := s.mfaCipher.DecryptSecret(ctx, webAuthnCredentialPurpose, record.CredentialEncrypted)
		if err != nil {
			return WebAuthnUser{}, nil, fmt.Errorf("decrypt WebAuthn credential %s: %w", base64.RawURLEncoding.EncodeToString(record.CredentialID), err)
		}
		out.CredentialJSON = append(out.CredentialJSON, []byte(plaintext))
	}
	return out, records, nil
}
