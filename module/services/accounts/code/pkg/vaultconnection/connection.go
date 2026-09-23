// Package vaultconnection owns Accounts' existing Vault transport projection.
package vaultconnection

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	codefly "github.com/codefly-dev/sdk-go"
)

type Config struct {
	Address, Token, CAFile, TokenFile string
	// AllowInsecureHTTP is the operator's assertion that a cleartext hop to a
	// non-loopback Vault is protected out of band (an mTLS mesh). Without it
	// every Vault request — the Transit calls carrying the token as much as the
	// signing-key fetch — refuses http to anything but loopback.
	AllowInsecureHTTP bool
}
type Connection struct {
	Address          string
	Client           *http.Client
	token, tokenFile string
}

// Load consumes the existing primitive's Codefly projection. Mounted token
// mode never requires or falls back to a static token, including on rotation.
func Load(ctx context.Context) (*Connection, error) {
	address, err := codefly.For(ctx).Service("vault").Configuration("vault", "address")
	if err != nil {
		return nil, errors.New("Vault address projection unavailable")
	}
	ca, _ := codefly.For(ctx).Service("vault").Configuration("vault", "ca-file")
	tokenFile, _ := codefly.For(ctx).Service("vault").Configuration("vault", "token-file")
	token := ""
	if tokenFile == "" {
		token, err = codefly.For(ctx).Service("vault").Secret("vault", "token")
		if err != nil {
			return nil, errors.New("Vault token projection unavailable")
		}
	}
	return New(Config{
		Address: address, Token: token, CAFile: ca, TokenFile: tokenFile,
		AllowInsecureHTTP: allowInsecureHTTP(ctx),
	})
}

// allowInsecureHTTP reads the `vault` workspace group's opt-in, falling back to
// a plain process variable exactly as the rest of accounts' workspace reads do.
func allowInsecureHTTP(ctx context.Context) bool {
	value, err := codefly.For(ctx).WorkspaceValue("vault", "VAULT_ALLOW_INSECURE_HTTP")
	if err != nil || value == "" {
		value = os.Getenv("VAULT_ALLOW_INSECURE_HTTP")
	}
	return strings.TrimSpace(value) == "true"
}

func New(config Config) (*Connection, error) {
	u, err := url.Parse(config.Address)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("invalid Vault endpoint")
	}
	if (config.CAFile != "" || config.TokenFile != "") && u.Scheme != "https" {
		return nil, errors.New("projected Vault identity requires HTTPS")
	}
	// The token rides every request, and Transit carries the secrets it seals,
	// so cleartext is refused for the whole connection, not only for the
	// signing-key fetch that used to be the one place it was checked.
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) && !config.AllowInsecureHTTP {
		return nil, errors.New("refusing cleartext http to a non-loopback Vault: use https, or set VAULT_ALLOW_INSECURE_HTTP=true only when the hop is protected out of band")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // credentials stay on the declared private dependency
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if config.CAFile != "" {
		pem, err := ReadProjection(config.CAFile)
		if err != nil {
			return nil, err
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("invalid Vault CA projection")
		}
		transport.TLSClientConfig.RootCAs = roots
	}
	c := &Connection{Address: strings.TrimSuffix(config.Address, "/"), token: config.Token, tokenFile: config.TokenFile, Client: &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("Vault redirect denied") }}}
	if _, err := c.Token(); err != nil {
		return nil, err
	}
	return c, nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c *Connection) Token() (string, error) {
	token := c.token
	if c.tokenFile != "" {
		data, err := ReadProjection(c.tokenFile)
		if err != nil {
			return "", err
		}
		token = strings.TrimSpace(string(data))
	}
	if token == "" || strings.ContainsAny(token, "\r\n\t ") {
		return "", errors.New("invalid Vault token projection")
	}
	return token, nil
}

// ReadProjection follows atomic Kubernetes secret symlinks, permits fsGroup
// read (0440), and rejects writable groups, world access and oversized files.
func ReadProjection(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("absolute Vault projection path required")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("Vault projection unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0027 != 0 || info.Size() > 128<<10 {
		return nil, errors.New("Vault projection must be a bounded private file")
	}
	data, err := io.ReadAll(io.LimitReader(file, (128<<10)+1))
	if err != nil || len(data) > 128<<10 {
		return nil, errors.New("Vault projection unreadable")
	}
	return data, nil
}
