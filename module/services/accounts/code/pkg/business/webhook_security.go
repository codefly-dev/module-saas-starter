package business

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const (
	webhookSecretPurposePrefix = "webhook-signing:"
	maxWebhookURLLength        = 2048
)

// webhookIPResolver is deliberately small so DNS rebinding and mixed-address
// answers can be tested without touching the process-wide resolver.
type webhookIPResolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type webhookDialContext func(ctx context.Context, network, address string) (net.Conn, error)

// WebhookEndpointPolicy owns both registration-time validation and the actual
// connection path. Reusing one policy prevents a safe-looking DNS answer at
// creation from being swapped for a private address when a delivery connects.
type WebhookEndpointPolicy struct {
	resolver webhookIPResolver
	dial     webhookDialContext
}

func NewWebhookEndpointPolicy() *WebhookEndpointPolicy {
	dialer := &net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return &WebhookEndpointPolicy{
		resolver: net.DefaultResolver,
		dial:     dialer.DialContext,
	}
}

func (p *WebhookEndpointPolicy) ensureDefaults() *WebhookEndpointPolicy {
	if p == nil {
		return NewWebhookEndpointPolicy()
	}
	if p.resolver == nil {
		p.resolver = net.DefaultResolver
	}
	if p.dial == nil {
		dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
		p.dial = dialer.DialContext
	}
	return p
}

// NormalizeAndValidate rejects every endpoint that is not an exact HTTPS URL
// on the public Internet. DNS names are resolved at registration and every A
// and AAAA answer must be public; a mixed public/private response is rejected.
func (p *WebhookEndpointPolicy) NormalizeAndValidate(ctx context.Context, raw string) (string, error) {
	p = p.ensureDefaults()
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxWebhookURLLength {
		return "", errors.New("webhook URL must be between 1 and 2048 bytes")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid webhook URL: %w", err)
	}
	if u.Scheme != "https" {
		return "", errors.New("webhook URL must use https")
	}
	if u.Host == "" || u.Opaque != "" || u.User != nil || u.Fragment != "" {
		return "", errors.New("webhook URL must be an absolute HTTPS URL without credentials or fragment")
	}
	if u.Port() != "" && u.Port() != "443" {
		return "", errors.New("webhook URL may use only HTTPS port 443")
	}

	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if err := validateWebhookHostname(host); err != nil {
		return "", err
	}
	addresses, err := p.resolvePublic(ctx, host)
	if err != nil {
		return "", err
	}
	if len(addresses) == 0 {
		return "", errors.New("webhook host did not resolve to an address")
	}

	if addr, err := netip.ParseAddr(host); err == nil && addr.Unmap().Is6() {
		u.Host = "[" + addr.Unmap().String() + "]"
	} else {
		u.Host = host
	}
	// The default port is canonical and therefore omitted from storage.
	u.RawFragment = ""
	return u.String(), nil
}

func validateWebhookHostname(host string) error {
	if host == "" || len(host) > 253 {
		return errors.New("webhook URL must contain a valid host")
	}
	for _, r := range host {
		if r > 127 {
			return errors.New("webhook host must use its ASCII/Punycode form")
		}
	}
	if strings.Contains(host, "%") {
		return errors.New("webhook host must not contain an IPv6 zone identifier")
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if !isPublicWebhookAddress(addr) {
			return fmt.Errorf("webhook address %s is not public", addr)
		}
		return nil
	}
	if isAmbiguousNumericHost(host) {
		return errors.New("webhook host uses an ambiguous numeric address form")
	}
	if !strings.Contains(host, ".") {
		return errors.New("webhook host must be a public fully-qualified domain name")
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("webhook host contains an invalid DNS label")
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return errors.New("webhook host contains an invalid DNS label")
			}
		}
	}
	return nil
}

func isAmbiguousNumericHost(host string) bool {
	if strings.HasPrefix(host, "0x") {
		return true
	}
	for _, c := range host {
		if (c < '0' || c > '9') && c != '.' {
			return false
		}
	}
	return true
}

var blockedWebhookPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

func isPublicWebhookAddress(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range blockedWebhookPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

func (p *WebhookEndpointPolicy) resolvePublic(ctx context.Context, host string) ([]netip.Addr, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		addr = addr.Unmap()
		if !isPublicWebhookAddress(addr) {
			return nil, fmt.Errorf("webhook address %s is not public", addr)
		}
		return []netip.Addr{addr}, nil
	}
	addresses, err := p.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve webhook host: %w", err)
	}
	if len(addresses) == 0 {
		return nil, errors.New("webhook host did not resolve to an address")
	}
	for _, addr := range addresses {
		if !isPublicWebhookAddress(addr) {
			return nil, fmt.Errorf("webhook host resolves to non-public address %s", addr.Unmap())
		}
	}
	return addresses, nil
}

// dialContext resolves and validates immediately before opening the socket,
// then dials the validated address directly. This pins the connection to the
// checked result and closes the validation-to-use DNS rebinding window.
func (p *WebhookEndpointPolicy) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	p = p.ensureDefaults()
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid webhook dial address: %w", err)
	}
	if port != "443" {
		return nil, errors.New("webhook delivery may connect only to port 443")
	}
	addresses, err := p.resolvePublic(ctx, strings.TrimSuffix(host, "."))
	if err != nil {
		return nil, err
	}
	var dialErr error
	for _, addr := range addresses {
		conn, err := p.dial(ctx, network, net.JoinHostPort(addr.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
		dialErr = errors.Join(dialErr, err)
	}
	return nil, fmt.Errorf("connect webhook endpoint: %w", dialErr)
}

func (p *WebhookEndpointPolicy) HTTPClient() *http.Client {
	p = p.ensureDefaults()
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           p.dialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          32,
			MaxIdleConnsPerHost:   2,
			MaxConnsPerHost:       2,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
		},
		// Return the first 3xx response to the sender; it is persisted as a
		// failed attempt and its Location is never requested.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// WebhookSecretPurpose binds an encrypted key envelope to one subscription so
// copying ciphertext between rows cannot silently substitute signing keys.
func WebhookSecretPurpose(subscriptionID string) string {
	return webhookSecretPurposePrefix + subscriptionID
}
