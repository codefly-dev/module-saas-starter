// Package vaultconnection owns Accounts' existing Vault transport projection.
package vaultconnection

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
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
	// Kubernetes, when set, makes the connection log in with the projected
	// ServiceAccount token and never read Token or TokenFile.
	Kubernetes *KubernetesAuth
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
	kubernetes       *kubernetesLogin
}

// Load consumes the existing primitive's Codefly projection. Mounted token
// mode never requires or falls back to a static token, including on rotation.
//
// The `vault` workspace group can instead name a Vault the deployment runs
// outside the composition (VAULT_ADDR, VAULT_CA_FILE) and select Kubernetes
// auth (VAULT_AUTH_METHOD=kubernetes, VAULT_K8S_ROLE, VAULT_K8S_MOUNT,
// VAULT_K8S_TOKEN_PATH), in which case accounts logs in itself and no Vault
// token is provisioned to it at all.
func Load(ctx context.Context) (*Connection, error) {
	address := groupValue(ctx, "VAULT_ADDR")
	if address == "" {
		var err error
		address, err = codefly.For(ctx).Service("vault").Configuration("vault", "address")
		if err != nil {
			return nil, errors.New("Vault address projection unavailable")
		}
	}
	ca := groupValue(ctx, "VAULT_CA_FILE")
	if ca == "" {
		ca, _ = codefly.For(ctx).Service("vault").Configuration("vault", "ca-file")
	}
	switch method := groupValue(ctx, "VAULT_AUTH_METHOD"); method {
	case "", "token":
	case AuthMethodKubernetes:
		return New(Config{
			Address: address, CAFile: ca,
			Kubernetes: &KubernetesAuth{
				Role:    groupValue(ctx, "VAULT_K8S_ROLE"),
				Mount:   groupValue(ctx, "VAULT_K8S_MOUNT"),
				JWTPath: groupValue(ctx, "VAULT_K8S_TOKEN_PATH"),
			},
			AllowInsecureHTTP: allowInsecureHTTP(ctx),
		})
	default:
		return nil, fmt.Errorf("unsupported VAULT_AUTH_METHOD %q: use token or kubernetes", method)
	}
	tokenFile, _ := codefly.For(ctx).Service("vault").Configuration("vault", "token-file")
	token := ""
	var err error
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

// groupValue reads one key of the `vault` workspace group, falling back to a
// plain process variable exactly as the rest of accounts' workspace reads do.
func groupValue(ctx context.Context, key string) string {
	value, err := codefly.For(ctx).WorkspaceValue("vault", key)
	if err != nil || value == "" {
		value = os.Getenv(key)
	}
	return strings.TrimSpace(value)
}

func allowInsecureHTTP(ctx context.Context) bool {
	return groupValue(ctx, "VAULT_ALLOW_INSECURE_HTTP") == "true"
}

func New(config Config) (*Connection, error) {
	u, err := url.Parse(config.Address)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("invalid Vault endpoint")
	}
	if (config.CAFile != "" || config.TokenFile != "") && u.Scheme != "https" {
		return nil, errors.New("projected Vault identity requires HTTPS")
	}
	loopback := isLoopbackHost(u.Hostname())
	if config.Kubernetes != nil {
		if config.Kubernetes.Role == "" {
			return nil, errors.New("Vault kubernetes auth requires VAULT_K8S_ROLE")
		}
		// The login presents the pod's identity and receives a token, so off
		// loopback it only ever travels over TLS anchored to a pinned CA — the
		// insecure-http assertion does not extend to handing out an identity.
		if !loopback && (u.Scheme != "https" || config.CAFile == "") {
			return nil, errors.New("Vault kubernetes auth requires https and VAULT_CA_FILE outside loopback")
		}
	}
	// The token rides every request, and Transit carries the secrets it seals,
	// so cleartext is refused for the whole connection, not only for the
	// signing-key fetch that used to be the one place it was checked.
	if u.Scheme == "http" && !loopback && !config.AllowInsecureHTTP {
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
	if config.Kubernetes != nil {
		auth := *config.Kubernetes
		if auth.Mount == "" {
			auth.Mount = DefaultKubernetesMount
		}
		if auth.JWTPath == "" {
			auth.JWTPath = DefaultKubernetesTokenPath
		}
		if strings.Trim(auth.Mount, "/") == "" || strings.Contains(auth.Mount, "..") {
			return nil, errors.New("invalid VAULT_K8S_MOUNT")
		}
		auth.Mount = strings.Trim(auth.Mount, "/")
		// No static token and no token file in this mode, ever.
		c.token, c.tokenFile = "", ""
		c.kubernetes = &kubernetesLogin{address: c.Address, client: c.Client, config: auth, now: time.Now}
	}
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

// Invalidate tells the connection Vault refused the token it presented, so a
// Kubernetes-auth connection logs in again on the next call. A static or file
// token has nothing to drop: the file is re-read on every call anyway.
func (c *Connection) Invalidate() {
	if c.kubernetes != nil {
		c.kubernetes.invalidate()
	}
}

func (c *Connection) Token() (string, error) {
	if c.kubernetes != nil {
		return c.kubernetes.current()
	}
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
