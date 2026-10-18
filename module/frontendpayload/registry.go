package frontendpayload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/codefly-dev/core/solutionhost"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/errcode"
)

// RegistrySource reads blob bytes from a fixed operator-selected repository.
// It neither selects releases nor publishes or retains artifacts. Retention is
// a contract of that existing repository, not this disposable preparation cache.
type RegistrySource struct{ repository *remote.Repository }

// NewRegistrySource consumes values, including optional reader credentials.
// transport is "https", "mesh", or "local"; plaintext is confined to the
// asserted mesh Service or to loopback. No credential files or Docker helpers
// are read, and no redirect can move a read outside the configured repository.
func NewRegistrySource(repository, transport, username, password string) (*RegistrySource, error) {
	return newRegistrySource(repository, transport, func(registry string) auth.CredentialFunc {
		return auth.StaticCredential(registry, auth.Credential{Username: username, Password: password})
	}, nil)
}

// newRegistrySource builds the source over base (http.DefaultTransport when
// nil), the round tripper every request of the source goes through.
func newRegistrySource(repository, transport string, credential func(registry string) auth.CredentialFunc, base http.RoundTripper) (*RegistrySource, error) {
	repo, err := remote.NewRepository(repository)
	if err != nil || repo.Reference.Reference != "" {
		return nil, errors.New("frontend artifact repository must name one repository without a tag or digest")
	}
	host := repo.Reference.Registry
	if hostname, _, err := net.SplitHostPort(host); err == nil {
		host = hostname
	}
	switch transport {
	case "https":
	case "mesh":
		if !strings.HasSuffix(host, ".svc.cluster.local") && !strings.HasSuffix(host, ".svc") {
			return nil, errors.New("frontend mesh artifact source must name a cluster Service")
		}
		repo.PlainHTTP = true
	case "local":
		if host != "localhost" && !net.ParseIP(host).IsLoopback() {
			return nil, errors.New("frontend local artifact source must be loopback")
		}
		repo.PlainHTTP = true
	default:
		return nil, errors.New("frontend artifact transport must be explicitly https, mesh or local")
	}
	scheme := "https"
	if repo.PlainHTTP {
		scheme = "http"
	}
	if base == nil {
		base = http.DefaultTransport
	}
	// A registry may answer a blob read by redirecting to a download path on
	// its own host (Artifact Registry does, every time). That stays inside the
	// configured source; a redirect anywhere else is refused.
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if next.URL.Host != repo.Reference.Registry || next.URL.Scheme != scheme || next.URL.User != nil || len(via) > 3 {
			return errRedirectRefused
		}
		return nil
	}}
	client.Transport = repositoryTransport{host: repo.Reference.Registry, scheme: scheme, base: base}
	repo.Client = &auth.Client{Client: client, Cache: auth.NewCache(), Credential: credential(repo.Reference.Registry)}
	return &RegistrySource{repository: repo}, nil
}

// Registry auth challenges are requests too. They must not send a configured
// reader credential to a realm outside this operator-selected source.
type repositoryTransport struct {
	host, scheme string
	base         http.RoundTripper
}

func (t repositoryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != t.host || r.URL.Scheme != t.scheme || r.URL.User != nil {
		return nil, errors.New("frontend artifact request left the configured source")
	}
	return t.base.RoundTrip(r)
}

var errRedirectRefused = errors.New("frontend artifact redirects are refused")

func (s *RegistrySource) Open(ctx context.Context, digest solutionhost.PayloadDigest) (io.ReadCloser, error) {
	if s == nil || s.repository == nil {
		return nil, ErrUnavailable
	}
	descriptor, reader, err := s.repository.Blobs().FetchReference(ctx, string(digest))
	if err != nil {
		return nil, unavailable(err)
	}
	if descriptor.Digest.String() != string(digest) || descriptor.Size < 0 || descriptor.Size > MaxBundleBytes {
		reader.Close()
		return nil, errors.New("frontend artifact descriptor differs from the admitted bundle")
	}
	return reader, nil
}

// unavailable names why a read failed without the transport's own text, which
// may carry a source locator: the registry's status, a refused redirect, or
// the reader identity's refusal, which never holds the token itself.
func unavailable(err error) error {
	var response *errcode.ErrorResponse
	var identity *readerIdentityError
	switch {
	case errors.As(err, &identity):
		return fmt.Errorf("%w: %s", ErrUnavailable, identity.reason)
	case errors.Is(err, errdef.ErrNotFound):
		return fmt.Errorf("%w: the registry holds no blob at the admitted digest", ErrUnavailable)
	case errors.Is(err, errRedirectRefused):
		return fmt.Errorf("%w: the registry redirected the read outside the configured repository", ErrUnavailable)
	case errors.As(err, &response):
		return fmt.Errorf("%w: the registry answered HTTP %d", ErrUnavailable, response.StatusCode)
	case errors.Is(err, context.DeadlineExceeded):
		// The context's own error alone: the transport's text names the
		// request URL, and a registry's download link is a capability.
		return fmt.Errorf("%w: %w", ErrUnavailable, context.DeadlineExceeded)
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("%w: %w", ErrUnavailable, context.Canceled)
	}
	return ErrUnavailable
}
