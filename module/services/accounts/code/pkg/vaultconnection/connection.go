// Package vaultconnection owns Accounts' existing Vault transport projection.
package vaultconnection

import (
	"context"
	"crypto/tls"
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

	"accounts/pkg/meshtransport"

	codefly "github.com/codefly-dev/sdk-go"
)

// Runtime says whether this connection is being made from a local run or from a
// deployed one. It has no usable zero value on purpose: the failure this guards
// against is a deployed product inheriting the local-development Vault shape
// because nobody stated which runtime it was in, so an unstated runtime is
// refused rather than defaulted in either direction.
type Runtime string

const (
	// RuntimeLocal is a developer machine or a local cluster: the composed
	// `vault` service, a provisioned token and a loopback address are all fine.
	RuntimeLocal Runtime = "local"
	// RuntimeDeployed is any deployed runtime context. Only a Vault the
	// composition names, reached with an AppRole credential, is accepted there —
	// over https, or over plaintext http exactly when the mesh assertion covers
	// the address.
	RuntimeDeployed Runtime = "deployed"
)

type Config struct {
	Address, Token, TokenFile string
	// Runtime selects which bindings are permissible. Required.
	Runtime Runtime
	// AppRole, when set, makes the connection log in with the AppRole
	// credential and never read Token or TokenFile.
	AppRole *AppRoleAuth
	// MeshProtected is the composition's assertion that every in-cluster hop is
	// carried by a mutually authenticated mesh. It admits a plaintext address,
	// and only an in-cluster Service address: see package meshtransport, whose
	// rule this carries. Nothing else admits plaintext off loopback.
	MeshProtected bool
}

type Connection struct {
	Address string
	Client  *http.Client
	// MeshProtected is the assertion this connection was admitted under, so a
	// caller that validates the same address again — the signing-key loader
	// does — applies the decision that let the connection exist rather than
	// re-reading configuration and possibly disagreeing with it.
	MeshProtected    bool
	token, tokenFile string
	appRole          *appRoleLogin
}

// Load builds the connection from the `vault` workspace group.
//
// Locally it uses the composed `vault` service's own projection: its address
// and a token (or token file) the platform provisions.
//
// Outside the local environment that projection is never inherited. The
// composition names the cell's Vault and supplies an AppRole credential, and
// nothing in the binding is a file:
//
//	VAULT_ADDR              the cell Vault's address
//	VAULT_AUTH_METHOD       approle
//	VAULT_APPROLE_MOUNT     the auth mount; `approle` when empty
//	VAULT_APPROLE_ROLE_ID   from the `vault` SECRET group
//	VAULT_APPROLE_SECRET_ID from the `vault` SECRET group
//
// In-cluster transport security belongs to the mesh, so a cell Vault listens
// without TLS of its own and the address is plaintext http. That is admitted
// only by the mesh assertion, and only for an in-cluster Service address — see
// package meshtransport. Every refusal names the key that is missing or wrong.
func Load(ctx context.Context) (*Connection, error) {
	runtime := RuntimeDeployed
	if codefly.IsLocal() {
		runtime = RuntimeLocal
	}
	// A malformed assertion fails startup rather than being read as either
	// answer, so it is resolved before anything depends on it.
	protected, err := meshtransport.Protected(codefly.For(ctx))
	if err != nil {
		return nil, err
	}
	address := groupValue(ctx, "VAULT_ADDR")
	if address == "" {
		if runtime == RuntimeDeployed {
			return nil, errors.New("VAULT_ADDR is required outside the local environment: name the cell's Vault in the `vault` configuration group, with VAULT_AUTH_METHOD=approle and the AppRole credential in the `vault` secret group. The composed `vault` service's own projection is the local-development shape and is never inherited by a deployed product")
		}
		address, err = codefly.For(ctx).Service("vault").Configuration("vault", "address")
		if err != nil {
			return nil, errors.New("Vault address projection unavailable")
		}
	}
	switch method := groupValue(ctx, "VAULT_AUTH_METHOD"); method {
	case "", "token":
		if runtime == RuntimeDeployed {
			return nil, errors.New("refusing a Vault token binding outside the local environment: set VAULT_AUTH_METHOD=approle in the `vault` configuration group, with VAULT_APPROLE_ROLE_ID and VAULT_APPROLE_SECRET_ID in the `vault` secret group, so accounts logs in and holds its own short-lived token. A long-lived Vault token provisioned to the pod is the local-development shape")
		}
	case AuthMethodAppRole:
		return New(Config{
			Address: address, Runtime: runtime, MeshProtected: protected,
			AppRole: &AppRoleAuth{
				RoleID:   secretGroupValue(ctx, "VAULT_APPROLE_ROLE_ID"),
				SecretID: secretGroupValue(ctx, "VAULT_APPROLE_SECRET_ID"),
				Mount:    groupValue(ctx, "VAULT_APPROLE_MOUNT"),
			},
		})
	default:
		return nil, fmt.Errorf("unsupported VAULT_AUTH_METHOD %q: use approle, or token in the local environment", method)
	}
	tokenFile, _ := codefly.For(ctx).Service("vault").Configuration("vault", "token-file")
	token := ""
	if tokenFile == "" {
		token, err = codefly.For(ctx).Service("vault").Secret("vault", "token")
		if err != nil {
			return nil, errors.New("Vault token projection unavailable")
		}
	}
	return New(Config{
		Address: address, Token: token, TokenFile: tokenFile,
		Runtime: runtime, MeshProtected: protected,
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

// secretGroupValue reads one key of the `vault` workspace SECRET group. The
// AppRole credential arrives this way — as a secret, like every other secret
// accounts holds — rather than as a mounted file.
func secretGroupValue(ctx context.Context, key string) string {
	value, err := codefly.For(ctx).WorkspaceSecret("vault", key)
	if err != nil || value == "" {
		value = os.Getenv(key)
	}
	return strings.TrimSpace(value)
}

func New(config Config) (*Connection, error) {
	switch config.Runtime {
	case RuntimeLocal, RuntimeDeployed:
	default:
		return nil, errors.New("Vault connection requires a stated runtime: local or deployed")
	}
	deployed := config.Runtime == RuntimeDeployed
	u, err := url.Parse(config.Address)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("invalid Vault endpoint")
	}
	if config.TokenFile != "" && u.Scheme != "https" && !isLoopbackHost(u.Hostname()) {
		return nil, errors.New("a Vault token file off loopback requires HTTPS")
	}
	loopback := isLoopbackHost(u.Hostname())
	// A deployed runtime accepts exactly one shape: the Vault the composition
	// names, reached with an AppRole credential it delivered as a secret. Every
	// other binding is the local-development shape, and a product that reached a
	// secrets store that way is the incident this refuses — so it is refused
	// here too, not only where Load reads the group, because New is the one door
	// every caller passes through.
	if deployed {
		if config.AppRole == nil {
			return nil, errors.New("refusing a Vault token binding outside the local environment: set VAULT_AUTH_METHOD=approle so accounts logs in and holds its own short-lived token")
		}
		if config.Token != "" || config.TokenFile != "" {
			return nil, errors.New("refusing a provisioned Vault token outside the local environment: the AppRole login mints accounts' own token and no static or file token may be delivered to the pod")
		}
	}
	if config.AppRole != nil {
		if config.AppRole.RoleID == "" || config.AppRole.SecretID == "" {
			return nil, errors.New("Vault approle auth requires VAULT_APPROLE_ROLE_ID and VAULT_APPROLE_SECRET_ID in the `vault` secret group")
		}
	}
	// The token rides every request, the login carries the AppRole credential,
	// and Transit carries the secrets it seals — so plaintext is refused for the
	// whole connection unless the mesh covers it. Loopback is always allowed, so
	// a local run against the composed service needs no assertion.
	if u.Scheme == "http" && !loopback && !meshtransport.Admits(config.MeshProtected, config.Address) {
		return nil, fmt.Errorf("refusing cleartext http to a non-loopback Vault at %q: %s", u.Host, meshtransport.Remedy)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // credentials stay on the declared private dependency
	// No CA pinning: in-cluster transport security is the mesh's, so a cell
	// Vault has no certificate of its own to anchor. An https address is
	// verified against the system roots like any other.
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	c := &Connection{Address: strings.TrimSuffix(config.Address, "/"), MeshProtected: config.MeshProtected, token: config.Token, tokenFile: config.TokenFile, Client: &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("Vault redirect denied") }}}
	if config.AppRole != nil {
		auth := *config.AppRole
		if auth.Mount == "" {
			auth.Mount = DefaultAppRoleMount
		}
		if strings.Trim(auth.Mount, "/") == "" || strings.Contains(auth.Mount, "..") {
			return nil, errors.New("invalid VAULT_APPROLE_MOUNT")
		}
		auth.Mount = strings.Trim(auth.Mount, "/")
		// No static token and no token file in this mode, ever.
		c.token, c.tokenFile = "", ""
		c.appRole = &appRoleLogin{address: c.Address, client: c.Client, config: auth, now: time.Now}
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

// Invalidate tells the connection Vault refused the token it presented, so an
// AppRole connection logs in again on the next call. A static or file token has
// nothing to drop: the file is re-read on every call anyway.
func (c *Connection) Invalidate() {
	if c.appRole != nil {
		c.appRole.invalidate()
	}
}

func (c *Connection) Token() (string, error) {
	if c.appRole != nil {
		return c.appRole.current()
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
