package infra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"accounts/pkg/business"

	"github.com/go-webauthn/webauthn/protocol"
	webauthnlib "github.com/go-webauthn/webauthn/webauthn"
)

type webAuthnEngine struct {
	inner *webauthnlib.WebAuthn
}

var _ business.WebAuthnEngine = (*webAuthnEngine)(nil)

func NewWebAuthnEngine(rpID, displayName string, origins []string) (business.WebAuthnEngine, error) {
	if strings.TrimSpace(rpID) == "" {
		return nil, errors.New("WebAuthn RP ID is required")
	}
	if strings.TrimSpace(displayName) == "" {
		return nil, errors.New("WebAuthn RP display name is required")
	}
	if len(origins) == 0 {
		return nil, errors.New("at least one WebAuthn RP origin is required")
	}
	engine, err := webauthnlib.New(&webauthnlib.Config{
		RPID:          rpID,
		RPDisplayName: displayName,
		RPOrigins:     origins,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementPreferred,
			UserVerification: protocol.VerificationRequired,
		},
		Timeouts: webauthnlib.TimeoutsConfig{
			Login:        webauthnlib.TimeoutConfig{Enforce: true, Timeout: 2 * time.Minute, TimeoutUVD: 2 * time.Minute},
			Registration: webauthnlib.TimeoutConfig{Enforce: true, Timeout: 2 * time.Minute, TimeoutUVD: 2 * time.Minute},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("configure WebAuthn relying party: %w", err)
	}
	return &webAuthnEngine{inner: engine}, nil
}

func (e *webAuthnEngine) BeginRegistration(_ context.Context, user business.WebAuthnUser) ([]byte, []byte, time.Time, error) {
	wu, err := decodeWebAuthnUser(user)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	creation, session, err := e.inner.BeginRegistration(wu)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	options, err := json.Marshal(creation.Response)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	state, err := json.Marshal(session)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	return options, state, session.Expires, nil
}

func (e *webAuthnEngine) FinishRegistration(ctx context.Context, user business.WebAuthnUser, sessionJSON, responseJSON []byte) (*business.WebAuthnCredentialResult, error) {
	wu, err := decodeWebAuthnUser(user)
	if err != nil {
		return nil, err
	}
	var session webauthnlib.SessionData
	if err := json.Unmarshal(sessionJSON, &session); err != nil {
		return nil, fmt.Errorf("decode WebAuthn registration state: %w", err)
	}
	req, err := credentialRequest(ctx, responseJSON)
	if err != nil {
		return nil, err
	}
	credential, err := e.inner.FinishRegistration(wu, session, req)
	if err != nil {
		return nil, err
	}
	return encodeCredentialResult(credential)
}

func (e *webAuthnEngine) BeginLogin(_ context.Context, user business.WebAuthnUser) ([]byte, []byte, time.Time, error) {
	wu, err := decodeWebAuthnUser(user)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	assertion, session, err := e.inner.BeginLogin(wu, webauthnlib.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	options, err := json.Marshal(assertion.Response)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	state, err := json.Marshal(session)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	return options, state, session.Expires, nil
}

func (e *webAuthnEngine) FinishLogin(ctx context.Context, user business.WebAuthnUser, sessionJSON, responseJSON []byte) (*business.WebAuthnCredentialResult, error) {
	wu, err := decodeWebAuthnUser(user)
	if err != nil {
		return nil, err
	}
	var session webauthnlib.SessionData
	if err := json.Unmarshal(sessionJSON, &session); err != nil {
		return nil, fmt.Errorf("decode WebAuthn login state: %w", err)
	}
	req, err := credentialRequest(ctx, responseJSON)
	if err != nil {
		return nil, err
	}
	credential, err := e.inner.FinishLogin(wu, session, req)
	if err != nil {
		return nil, err
	}
	return encodeCredentialResult(credential)
}

type webAuthnLibraryUser struct {
	id          []byte
	name        string
	displayName string
	credentials []webauthnlib.Credential
}

func (u *webAuthnLibraryUser) WebAuthnID() []byte                            { return u.id }
func (u *webAuthnLibraryUser) WebAuthnName() string                          { return u.name }
func (u *webAuthnLibraryUser) WebAuthnDisplayName() string                   { return u.displayName }
func (u *webAuthnLibraryUser) WebAuthnCredentials() []webauthnlib.Credential { return u.credentials }

func decodeWebAuthnUser(user business.WebAuthnUser) (*webAuthnLibraryUser, error) {
	out := &webAuthnLibraryUser{
		id:          append([]byte(nil), user.ID...),
		name:        user.Name,
		displayName: user.DisplayName,
		credentials: make([]webauthnlib.Credential, 0, len(user.CredentialJSON)),
	}
	for _, raw := range user.CredentialJSON {
		var credential webauthnlib.Credential
		if err := json.Unmarshal(raw, &credential); err != nil {
			return nil, fmt.Errorf("decode stored WebAuthn credential: %w", err)
		}
		out.credentials = append(out.credentials, credential)
	}
	return out, nil
}

func credentialRequest(ctx context.Context, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://webauthn.invalid/finish", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func encodeCredentialResult(credential *webauthnlib.Credential) (*business.WebAuthnCredentialResult, error) {
	if credential == nil || len(credential.ID) == 0 {
		return nil, errors.New("WebAuthn returned an empty credential")
	}
	raw, err := json.Marshal(credential)
	if err != nil {
		return nil, err
	}
	return &business.WebAuthnCredentialResult{ID: append([]byte(nil), credential.ID...), CredentialJSON: raw}, nil
}
