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
	// composition names, over https, with Kubernetes auth, is accepted there.
	RuntimeDeployed Runtime = "deployed"
)

type Config struct {
	Address, Token, CAFile, TokenFile string
	// Runtime selects which bindings are permissible. Required.
	Runtime Runtime
	// Kubernetes, when set, makes the connection log in with the projected
	// ServiceAccount token and never read Token or TokenFile.
	Kubernetes *KubernetesAuth
	// AllowInsecureHTTP is the operator's assertion that a cleartext hop to a
	// non-loopback Vault is protected out of band (an mTLS mesh). Without it
	// every Vault request — the Transit calls carrying the token as much as the
	// signing-key fetch — refuses http to anything but loopback. It is a local
	// assertion only: a deployed runtime refuses cleartext whatever it says.
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
// The `vault` workspace group names the Vault the deployment reaches
// (VAULT_ADDR, VAULT_CA_FILE) and selects Kubernetes auth
// (VAULT_AUTH_METHOD=kubernetes, VAULT_K8S_ROLE, VAULT_K8S_MOUNT,
// VAULT_K8S_TOKEN_PATH), in which case accounts logs in itself and no Vault
// token is provisioned to it at all.
//
// Outside the local environment that group is the only source: the composed
// `vault` service's address and token projections are the local-development
// shape, and a deployed product that inherited them by omission is the failure
// this refuses. Each refusal names the key that is missing or wrong.
func Load(ctx context.Context) (*Connection, error) {
	runtime := RuntimeDeployed
	if codefly.IsLocal() {
		runtime = RuntimeLocal
	}
	address := groupValue(ctx, "VAULT_ADDR")
	if address == "" {
		if runtime == RuntimeDeployed {
			return nil, errors.New("VAULT_ADDR is required outside the local environment: name the cell's Vault in the `vault` configuration group (an https address, VAULT_CA_FILE, VAULT_AUTH_METHOD=kubernetes and VAULT_K8S_ROLE). The composed `vault` service's own projection is the local-development shape and is never inherited by a deployed product")
		}
		var err error
		address, err = codefly.For(ctx).Service("vault").Configuration("vault", "address")
		if err != nil {
			return nil, errors.New("Vault address projection unavailable")
		}
	}
	ca := groupValue(ctx, "VAULT_CA_FILE")
	if ca == "" && runtime == RuntimeLocal {
		ca, _ = codefly.For(ctx).Service("vault").Configuration("vault", "ca-file")
	}
	switch method := groupValue(ctx, "VAULT_AUTH_METHOD"); method {
	case "", "token":
		if runtime == RuntimeDeployed {
			return nil, errors.New("refusing a Vault token binding outside the local environment: set VAULT_AUTH_METHOD=kubernetes with VAULT_K8S_ROLE in the `vault` configuration group so accounts logs in as its own ServiceAccount. A long-lived Vault token provisioned to the pod is the local-development shape")
		}
	case AuthMethodKubernetes:
		return New(Config{
			Address: address, CAFile: ca, Runtime: runtime,
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
		Address: address, Token: token, CAFile: ca, TokenFile: tokenFile, Runtime: runtime,
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
	if (config.CAFile != "" || config.TokenFile != "") && u.Scheme != "https" {
		return nil, errors.New("projected Vault identity requires HTTPS")
	}
	loopback := isLoopbackHost(u.Hostname())
	// A deployed runtime accepts exactly one shape: the Vault the composition
	// names, over TLS anchored to a pinned CA, reached as accounts' own
	// ServiceAccount. Every other binding is the local-development shape, and a
	// product that reached a secrets store that way is the incident this
	// refuses — so it is refused here too, not only where Load reads the group,
	// because New is the one door every caller passes through.
	if deployed {
		if config.Kubernetes == nil {
			return nil, errors.New("refusing a Vault token binding outside the local environment: set VAULT_AUTH_METHOD=kubernetes with VAULT_K8S_ROLE so accounts logs in as its own ServiceAccount")
		}
		if u.Scheme != "https" {
			return nil, errors.New("refusing a cleartext Vault address outside the local environment: VAULT_ADDR must be an https URL, whatever VAULT_ALLOW_INSECURE_HTTP asserts")
		}
		if config.AllowInsecureHTTP {
			return nil, errors.New("refusing VAULT_ALLOW_INSECURE_HTTP=true outside the local environment: the signing key and every Transit call would cross the wire in the clear, and a mesh assertion nothing can verify is not a substitute for TLS to the Vault")
		}
		if config.Token != "" || config.TokenFile != "" {
			return nil, errors.New("refusing a provisioned Vault token outside the local environment: Kubernetes auth mints accounts' own token and no static or file token may be projected to the pod")
		}
	}
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
