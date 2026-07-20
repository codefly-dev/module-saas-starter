package business

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"maps"
	"time"

	"accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/wool"
	"github.com/google/uuid"
)

const mfaLoginTransactionTTL = 5 * time.Minute

// ErrMFAChallengeRejected intentionally covers unknown, expired, consumed,
// cross-user, and invalid-factor cases. Public adapters must not turn those
// states into an account or transaction oracle.
var ErrMFAChallengeRejected = errors.New("MFA challenge rejected")

// MFALoginTransaction snapshots the canonical identity resolved after primary
// authentication. It is not a session: no access or refresh credential exists
// until the transaction is successfully consumed.
type MFALoginTransaction struct {
	ID                    string
	TokenHash             string
	UserID                string
	OrgID                 string
	OrgRole               string
	PlatformRole          string
	SessionID             string
	DeviceInfo            map[string]string
	IPAddress             string
	AuthenticationMethods []string
	AuthenticatedAt       time.Time
	ExpiresAt             time.Time
	ConsumedAt            *time.Time
	FailedAttempts        int
	MaxAttempts           int
	LockedUntil           *time.Time
	CreatedAt             time.Time
}

// MFALoginStore persists login hand-offs. Consume must lock the transaction,
// invoke issue inside the same database transaction, mark it consumed only
// after issue succeeds, and commit both changes together.
type MFALoginStore interface {
	CreateMFALoginTransaction(ctx context.Context, tx *MFALoginTransaction) error
	GetActiveMFALoginTransaction(ctx context.Context, tokenHash string, now time.Time) (*MFALoginTransaction, error)
	ConsumeMFALoginTransaction(
		ctx context.Context,
		tokenHash string,
		now time.Time,
		issue func(context.Context, *MFALoginTransaction) error,
	) error
}

func newMFALoginToken() (plaintext, hash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	plaintext = base64.RawURLEncoding.EncodeToString(raw)
	digest := sha256.Sum256([]byte(plaintext))
	return plaintext, hex.EncodeToString(digest[:]), nil
}

func hashMFALoginToken(plaintext string) string {
	digest := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(digest[:])
}

// BeginMFALogin persists a short-lived one-use transaction and returns only
// its random bearer value. It never calls the JWT minter.
func (s *Service) BeginMFALogin(ctx context.Context, identity *auth.Identity) (string, error) {
	w := wool.Get(ctx).In("BeginMFALogin")
	store, ok := s.store.(MFALoginStore)
	if !ok {
		return "", w.NewError("store does not implement MFALoginStore")
	}
	if identity == nil || identity.UserID == uuid.Nil {
		return "", w.NewError("MFA login identity missing user")
	}

	token, hash, err := newMFALoginToken()
	if err != nil {
		return "", w.Wrapf(err, "cannot generate MFA login token")
	}
	now := time.Now()
	tx := &MFALoginTransaction{
		ID:                    NewIDString(),
		TokenHash:             hash,
		UserID:                identity.UserID.String(),
		OrgRole:               identity.OrgRole,
		PlatformRole:          identity.PlatformRole,
		SessionID:             identity.SessionID.String(),
		DeviceInfo:            maps.Clone(identity.DeviceInfo),
		IPAddress:             identity.IPAddress,
		AuthenticationMethods: append([]string(nil), identity.AuthenticationMethods...),
		AuthenticatedAt:       identity.AuthenticatedAt,
		ExpiresAt:             now.Add(mfaLoginTransactionTTL),
		CreatedAt:             now,
	}
	if identity.OrgID != uuid.Nil {
		tx.OrgID = identity.OrgID.String()
	}
	if identity.SessionID == uuid.Nil {
		tx.SessionID = NewIDString()
	}

	if err := s.store.WithUserTx(ctx, tx.UserID, func(ctx context.Context) error {
		return store.CreateMFALoginTransaction(ctx, tx)
	}); err != nil {
		return "", w.Wrapf(err, "cannot persist MFA login transaction")
	}
	s.emit(ctx, tx.UserID, "user", "auth.mfa_challenge_started", "mfa_login_transaction", tx.ID, tx.OrgID)
	return token, nil
}

// CompleteMFAChallenge validates a login factor and creates the first normal
// session. Production storage runs factor consumption, session insertion, and
// challenge consumption in one database transaction.
func (s *Service) CompleteMFAChallenge(ctx context.Context, mfaToken, code string) (*gen.CompleteMFAChallengeResponse, error) {
	w := wool.Get(ctx).In("CompleteMFAChallenge")
	loginStore, ok := s.store.(MFALoginStore)
	if !ok {
		return nil, w.NewError("store does not implement MFALoginStore")
	}
	mfaStore, ok := s.store.(MFAStore)
	if !ok {
		return nil, w.NewError("store does not implement MFAStore")
	}
	if s.minter == nil || mfaToken == "" || code == "" {
		return nil, ErrMFAChallengeRejected
	}

	var pair *auth.TokenPair
	var consumed *MFALoginTransaction
	var factorMethod string
	now := time.Now()
	err := loginStore.ConsumeMFALoginTransaction(ctx, hashMFALoginToken(mfaToken), now, func(txCtx context.Context, tx *MFALoginTransaction) error {
		valid, method, err := validateMFACodeInTx(txCtx, mfaStore, s.mfaCipher, tx.UserID, code, now)
		if err != nil {
			return err
		}
		if !valid {
			return ErrMFAChallengeRejected
		}
		factorMethod = method
		if method == "backup_code" {
			if err := s.store.CreateNotification(txCtx, backupCodeUseNotification(tx.UserID)); err != nil {
				return w.Wrapf(err, "create backup-code security notification")
			}
		}

		var authenticationMethod string
		switch method {
		case "totp":
			authenticationMethod = auth.AuthenticationMethodOTP
		case "backup_code":
			authenticationMethod = auth.AuthenticationMethodRecovery
		default:
			return ErrMFAChallengeRejected
		}
		pair, err = s.mintMFASessionInTx(txCtx, tx, authenticationMethod, now)
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
		return nil, w.Wrapf(err, "complete MFA login transaction")
	}
	if pair == nil || consumed == nil {
		return nil, w.NewError("MFA login completed without a session")
	}

	user, err := s.getUserAsSelf(ctx, consumed.UserID)
	if err != nil || user == nil {
		user = &gen.User{Uuid: consumed.UserID}
	}
	s.emit(ctx, consumed.UserID, "user", "auth.login", "session", consumed.SessionID, consumed.OrgID)
	s.emit(ctx, consumed.UserID, "user", "auth.mfa_challenge_completed", "mfa_login_transaction", consumed.ID, consumed.OrgID)
	if factorMethod == "backup_code" {
		s.emit(ctx, consumed.UserID, "user", "mfa.backup_code_used", "user", consumed.UserID, consumed.OrgID)
	}

	return completeMFAResponse(pair, user), nil
}

func (s *Service) mintMFASessionInTx(ctx context.Context, tx *MFALoginTransaction, authenticationMethod string, now time.Time) (*auth.TokenPair, error) {
	w := wool.Get(ctx).In("mintMFASessionInTx")
	userID, err := uuid.Parse(tx.UserID)
	if err != nil {
		return nil, ErrMFAChallengeRejected
	}
	identity := &auth.Identity{
		UserID:                userID,
		OrgRole:               tx.OrgRole,
		PlatformRole:          tx.PlatformRole,
		MFASatisfied:          true,
		AuthenticationMethods: append(append([]string(nil), tx.AuthenticationMethods...), authenticationMethod),
		AuthenticatedAt:       tx.AuthenticatedAt,
		AssuranceLevel:        auth.AssuranceLevelAAL2,
		MFAVerifiedAt:         now,
		DeviceInfo:            maps.Clone(tx.DeviceInfo),
		IPAddress:             tx.IPAddress,
	}
	if tx.OrgID != "" {
		identity.OrgID, err = uuid.Parse(tx.OrgID)
		if err != nil {
			return nil, ErrMFAChallengeRejected
		}
	}
	if tx.SessionID != "" {
		identity.SessionID, err = uuid.Parse(tx.SessionID)
		if err != nil {
			return nil, ErrMFAChallengeRejected
		}
	}
	pair, err := s.minter.Mint(auth.WithAtomicSessionTransaction(ctx), identity)
	if err != nil {
		return nil, w.Wrapf(err, "mint MFA-completed session")
	}
	return pair, nil
}

func completeMFAResponse(pair *auth.TokenPair, user *gen.User) *gen.CompleteMFAChallengeResponse {
	return &gen.CompleteMFAChallengeResponse{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresIn:    int64(AccessTokenLifetime.Seconds()),
		User:         user,
	}
}
