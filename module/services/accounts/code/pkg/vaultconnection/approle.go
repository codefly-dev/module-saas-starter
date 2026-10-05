package vaultconnection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// AuthMethodAppRole makes accounts log in to Vault itself with an AppRole
// credential the configuration plane delivers as a secret, exactly the way
// every other accounts secret arrives. The Vault token it receives is held in
// memory only, renewed before its lease runs out, and re-minted by logging in
// again when renewal fails or Vault refuses it — so no long-lived Vault token,
// and never a root token, is provisioned to the pod.
//
// Nothing here reads a file. In-cluster transport security belongs to the mesh,
// so there is no CA to pin and no projected token to mount: the whole binding
// is values plus one secret.
const AuthMethodAppRole = "approle"

// DefaultAppRoleMount is Vault's default path for the AppRole auth method.
const DefaultAppRoleMount = "approle"

// AppRoleAuth configures the AppRole login. RoleID and SecretID come from the
// `vault` workspace SECRET group; Mount comes from the non-secret group.
type AppRoleAuth struct {
	// RoleID identifies the Vault role. Required.
	RoleID string
	// SecretID is the credential proving the holder may assume that role.
	// Required.
	SecretID string
	// Mount is the auth method's mount path; DefaultAppRoleMount when empty.
	Mount string
}

// renewWhenRemaining is the fraction of a lease below which the token is
// renewed before use, so a request never presents a token about to lapse.
const renewWhenRemaining = 3 // renew once less than 1/3 of the lease is left

// loginRetryAfter bounds how often a failing login is retried, so an outage
// does not turn every Transit call into a login against the same outage.
const loginRetryAfter = 2 * time.Second

type appRoleLogin struct {
	address string
	client  *http.Client
	config  AppRoleAuth
	now     func() time.Time

	mu          sync.Mutex
	token       string
	obtained    time.Time
	lease       time.Duration
	renewable   bool
	lastFailure time.Time
	lastErr     error
}

type vaultAuthResponse struct {
	Auth *struct {
		ClientToken   string `json:"client_token"`
		LeaseDuration int64  `json:"lease_duration"`
		Renewable     bool   `json:"renewable"`
	} `json:"auth"`
}

// current returns a usable token: the cached one while it has most of its lease
// left, a renewed one when it is running low, and a fresh login when there is
// none, renewal failed, or the cache was invalidated by a refusal.
func (k *appRoleLogin) current() (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.now()
	if k.token != "" {
		if k.lease <= 0 || now.Sub(k.obtained) < k.lease-k.lease/renewWhenRemaining {
			return k.token, nil
		}
		if k.renewable && now.Sub(k.obtained) < k.lease {
			if err := k.renewLocked(); err == nil {
				return k.token, nil
			}
		}
		k.token = ""
	}
	if !k.lastFailure.IsZero() && now.Sub(k.lastFailure) < loginRetryAfter {
		return "", k.lastErr
	}
	if err := k.loginLocked(); err != nil {
		k.lastFailure, k.lastErr = now, err
		return "", err
	}
	k.lastFailure, k.lastErr = time.Time{}, nil
	return k.token, nil
}

// invalidate drops the cached token after Vault refused it, so the next call
// logs in again instead of presenting a revoked token until its lease ends.
func (k *appRoleLogin) invalidate() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.token = ""
	k.lastFailure, k.lastErr = time.Time{}, nil
}

func (k *appRoleLogin) loginLocked() error {
	body, _ := json.Marshal(map[string]string{
		"role_id":   k.config.RoleID,
		"secret_id": k.config.SecretID,
	})
	auth, err := k.post("/v1/auth/"+k.config.Mount+"/login", "", body)
	if err != nil {
		// Vault's own body is never echoed — the login request carries the
		// credential and a server that reflected it back would put it in this
		// error — so the status is all there is to go on. On its own that is a
		// bare number, and the operator who meets it in a crash loop needs to
		// know which two keys to look at and that a revoked or rotated secret_id
		// looks exactly like a wrong one. Hence a named remedy beside it.
		return fmt.Errorf("Vault approle login at mount %q: %w — check VAULT_APPROLE_ROLE_ID and VAULT_APPROLE_SECRET_ID in the `vault` secret group: the role may not exist at this mount, or the secret_id may be revoked, expired or used past its limit. Replace the secret_id through the configuration plane and restart; a credential this login never accepted will not start accepting itself", k.config.Mount, err)
	}
	k.store(auth)
	return nil
}

func (k *appRoleLogin) renewLocked() error {
	auth, err := k.post("/v1/auth/token/renew-self", k.token, []byte("{}"))
	if err != nil {
		return err
	}
	k.store(auth)
	return nil
}

func (k *appRoleLogin) store(auth *vaultAuthResponse) {
	k.token = auth.Auth.ClientToken
	k.obtained = k.now()
	k.lease = time.Duration(auth.Auth.LeaseDuration) * time.Second
	k.renewable = auth.Auth.Renewable
}

// post sends one auth request. Vault's body is never echoed into the error: a
// login response carries a token, and a refusal is diagnosed by its status.
func (k *appRoleLogin) post(path, token string, body []byte) (*vaultAuthResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), k.client.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, k.address+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	resp, err := k.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Vault answered %d", resp.StatusCode)
	}
	var auth vaultAuthResponse
	if err := json.Unmarshal(data, &auth); err != nil || auth.Auth == nil ||
		auth.Auth.ClientToken == "" || strings.ContainsAny(auth.Auth.ClientToken, "\r\n\t ") {
		return nil, errors.New("Vault returned no usable client token")
	}
	return &auth, nil
}
