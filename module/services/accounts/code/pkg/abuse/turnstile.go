package abuse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

var ErrChallengeRejected = errors.New("abuse protection challenge rejected")

type Challenge struct {
	Token  string
	Action string
}

type Verifier interface {
	Verify(context.Context, Challenge) error
}

type DisabledVerifier struct{}

func (DisabledVerifier) Verify(context.Context, Challenge) error { return nil }

type TurnstileConfig struct {
	SecretKey        string
	VerifyURL        string
	AllowedHostnames []string
	HTTPClient       *http.Client
}

type TurnstileVerifier struct {
	secretKey        string
	verifyURL        string
	allowedHostnames []string
	client           *http.Client
}

func NewTurnstileVerifier(config TurnstileConfig) (*TurnstileVerifier, error) {
	if strings.TrimSpace(config.SecretKey) == "" {
		return nil, errors.New("abuse: Turnstile secret key is required")
	}
	if config.VerifyURL == "" {
		config.VerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
	}
	endpoint, err := url.Parse(config.VerifyURL)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return nil, errors.New("abuse: Turnstile verification URL must be absolute")
	}
	local := endpoint.Hostname() == "localhost" || endpoint.Hostname() == "127.0.0.1"
	if endpoint.Scheme != "https" && (endpoint.Scheme != "http" || !local) {
		return nil, errors.New("abuse: Turnstile verification URL must use HTTPS")
	}
	allowed := make([]string, 0, len(config.AllowedHostnames))
	for _, hostname := range config.AllowedHostnames {
		hostname = strings.ToLower(strings.TrimSpace(hostname))
		if hostname != "" {
			allowed = append(allowed, hostname)
		}
	}
	if len(allowed) == 0 {
		return nil, errors.New("abuse: at least one Turnstile hostname is required")
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &TurnstileVerifier{
		secretKey:        config.SecretKey,
		verifyURL:        endpoint.String(),
		allowedHostnames: allowed,
		client:           client,
	}, nil
}

func (v *TurnstileVerifier) Verify(ctx context.Context, challenge Challenge) error {
	token := strings.TrimSpace(challenge.Token)
	action := strings.TrimSpace(challenge.Action)
	if token == "" || action == "" {
		return ErrChallengeRejected
	}
	form := url.Values{
		"secret":   {v.secretKey},
		"response": {token},
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		v.verifyURL,
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return fmt.Errorf("abuse: create Turnstile request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := v.client.Do(request)
	if err != nil {
		return fmt.Errorf("abuse: verify Turnstile challenge: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	if err != nil {
		return fmt.Errorf("abuse: read Turnstile response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("abuse: Turnstile returned HTTP %d", response.StatusCode)
	}
	var result struct {
		Success  bool     `json:"success"`
		Action   string   `json:"action"`
		Hostname string   `json:"hostname"`
		Errors   []string `json:"error-codes"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("abuse: decode Turnstile response: %w", err)
	}
	if !result.Success ||
		result.Action != action ||
		!slices.Contains(v.allowedHostnames, strings.ToLower(result.Hostname)) {
		return ErrChallengeRejected
	}
	return nil
}
