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

// AuthMethodKubernetes makes accounts log in to Vault itself with its projected
// ServiceAccount token, so no long-lived Vault token — and never a root token —
// is ever handed to the pod.
const AuthMethodKubernetes = "kubernetes"

// DefaultKubernetesMount is Vault's default path for the Kubernetes auth method.
const DefaultKubernetesMount = "kubernetes"

// DefaultKubernetesTokenPath is where the deployment projects a ServiceAccount
// token whose audience is `vault`. It is not the pod's default token: that one
// is audience-bound to the API server and must not be replayable against Vault.
const DefaultKubernetesTokenPath = "/var/run/secrets/vault/token"

// KubernetesAuth configures the Kubernetes auth login.
type KubernetesAuth struct {
	// Role is the Vault role bound to this service's ServiceAccount. Required.
	Role string
	// Mount is the auth method's mount path; DefaultKubernetesMount when empty.
	Mount string
	// JWTPath is the projected ServiceAccount token, re-read on every login so a
	// rotated projection is used; DefaultKubernetesTokenPath when empty.
	JWTPath string
}

// renewWhenRemaining is the fraction of a lease below which the token is
// renewed before use, so a request never presents a token about to lapse.
const renewWhenRemaining = 3 // renew once less than 1/3 of the lease is left

// loginRetryAfter bounds how often a failing login is retried, so an outage
// does not turn every Transit call into a login against the same outage.
const loginRetryAfter = 2 * time.Second

type kubernetesLogin struct {
	address string
	client  *http.Client
	config  KubernetesAuth
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
func (k *kubernetesLogin) current() (string, error) {
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
func (k *kubernetesLogin) invalidate() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.token = ""
	k.lastFailure, k.lastErr = time.Time{}, nil
}

func (k *kubernetesLogin) loginLocked() error {
	jwt, err := ReadProjection(k.config.JWTPath)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{
		"role": k.config.Role,
		"jwt":  strings.TrimSpace(string(jwt)),
	})
	auth, err := k.post("/v1/auth/"+k.config.Mount+"/login", "", body)
	if err != nil {
		return fmt.Errorf("Vault kubernetes login: %w", err)
	}
	k.store(auth)
	return nil
}

func (k *kubernetesLogin) renewLocked() error {
	auth, err := k.post("/v1/auth/token/renew-self", k.token, []byte("{}"))
	if err != nil {
		return err
	}
	k.store(auth)
	return nil
}

func (k *kubernetesLogin) store(auth *vaultAuthResponse) {
	k.token = auth.Auth.ClientToken
	k.obtained = k.now()
	k.lease = time.Duration(auth.Auth.LeaseDuration) * time.Second
	k.renewable = auth.Auth.Renewable
}

// post sends one auth request. Vault's body is never echoed into the error: a
// login response carries a token, and a refusal is diagnosed by its status.
func (k *kubernetesLogin) post(path, token string, body []byte) (*vaultAuthResponse, error) {
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
