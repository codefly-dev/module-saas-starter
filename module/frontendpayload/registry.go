package frontendpayload

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/codefly-dev/core/solutionhost"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
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
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("frontend artifact redirects are refused")
	}}
	scheme := "https"
	if repo.PlainHTTP {
		scheme = "http"
	}
	client.Transport = repositoryTransport{host: repo.Reference.Registry, scheme: scheme}
	repo.Client = &auth.Client{Client: client, Cache: auth.NewCache(), Credential: auth.StaticCredential(repo.Reference.Registry, auth.Credential{
		Username: username, Password: password,
	})}
	return &RegistrySource{repository: repo}, nil
}

// Registry auth challenges are requests too. They must not send a configured
// reader credential to a realm outside this operator-selected source.
type repositoryTransport struct{ host, scheme string }

func (t repositoryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != t.host || r.URL.Scheme != t.scheme || r.URL.User != nil {
		return nil, errors.New("frontend artifact request left the configured source")
	}
	return http.DefaultTransport.RoundTrip(r)
}

func (s *RegistrySource) Open(ctx context.Context, digest solutionhost.PayloadDigest) (io.ReadCloser, error) {
	if s == nil || s.repository == nil {
		return nil, ErrUnavailable
	}
	descriptor, reader, err := s.repository.Blobs().FetchReference(ctx, string(digest))
	if err != nil {
		return nil, ErrUnavailable
	}
	if descriptor.Digest.String() != string(digest) || descriptor.Size < 0 || descriptor.Size > MaxBundleBytes {
		reader.Close()
		return nil, errors.New("frontend artifact descriptor differs from the admitted bundle")
	}
	return reader, nil
}
