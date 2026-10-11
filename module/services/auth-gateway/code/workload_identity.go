package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"

	codefly "github.com/codefly-dev/sdk-go"
)

const workloadSPIFFEHeader = "x-codefly-workload-spiffe-id"

type workloadPeerKey struct{}

// Only the workload listener supplies this context value. HTTP headers never
// attest a peer, including on the plain listener.
func workloadPeer(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(workloadPeerKey{}).(string)
	return id, ok
}

type workloadIdentity struct {
	certificate *codefly.CertificateReloader
	trustFile   string
	callersFile string
}

// Resolve only the declared group: bare environment variables are not a second
// source of workload credentials. Validate every projected path before serving.
func loadWorkloadIdentity(value func(string) (string, error)) (*workloadIdentity, error) {
	paths := make(map[string]string)
	for _, key := range []string{"CERT_FILE", "KEY_FILE", "TRUST_BUNDLE_FILE", "ALLOWED_CALLERS_FILE", "TOKEN_FILE"} {
		path, err := value(key)
		if err != nil || strings.TrimSpace(path) == "" {
			return nil, fmt.Errorf("workload-identity %s: missing path (%v)", key, err)
		}
		if _, err := os.ReadFile(path); err != nil {
			return nil, fmt.Errorf("workload-identity %s: %w", key, err)
		}
		paths[key] = path
	}
	reloader, err := codefly.NewCertificateReloader(paths["CERT_FILE"], paths["KEY_FILE"])
	if err != nil {
		return nil, fmt.Errorf("workload-identity CERT_FILE/KEY_FILE: %w", err)
	}
	w := &workloadIdentity{certificate: reloader, trustFile: paths["TRUST_BUNDLE_FILE"], callersFile: paths["ALLOWED_CALLERS_FILE"]}
	if _, err := w.roots(); err != nil {
		return nil, err
	}
	if _, err := w.allowedCallers(); err != nil {
		return nil, err
	}
	if _, err := certificateSPIFFEID(reloader.Certificate().Leaf); err != nil {
		return nil, fmt.Errorf("workload-identity CERT_FILE: %w", err)
	}
	return w, nil
}

var workloadSPIFFEPattern = regexp.MustCompile(`^spiffe://[a-z0-9._-]{1,255}/ns/[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?/sa/[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func certificateSPIFFEID(cert *x509.Certificate) (string, error) {
	if cert == nil || len(cert.URIs) != 1 || !workloadSPIFFEPattern.MatchString(cert.URIs[0].String()) {
		return "", fmt.Errorf("expected exactly one Kubernetes SPIFFE URI SAN")
	}
	return cert.URIs[0].String(), nil
}

func (w *workloadIdentity) roots() (*x509.CertPool, error) {
	pem, err := os.ReadFile(w.trustFile)
	if err != nil {
		return nil, fmt.Errorf("workload-identity TRUST_BUNDLE_FILE: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("workload-identity TRUST_BUNDLE_FILE: no certificates")
	}
	return roots, nil
}

// Read on every admission, including requests on an existing connection. A
// removed identity, missing file or malformed replacement revokes immediately.
func (w *workloadIdentity) allowedCallers() (map[string]bool, error) {
	body, err := os.ReadFile(w.callersFile)
	if err != nil {
		return nil, fmt.Errorf("workload-identity ALLOWED_CALLERS_FILE: %w", err)
	}
	allowed := make(map[string]bool)
	for _, line := range strings.Split(string(body), "\n") {
		line, _, _ = strings.Cut(line, "#")
		id := strings.TrimSpace(line)
		if id == "" {
			continue
		}
		if !workloadSPIFFEPattern.MatchString(id) {
			return nil, fmt.Errorf("workload-identity ALLOWED_CALLERS_FILE: invalid SPIFFE ID")
		}
		allowed[id] = true
	}
	return allowed, nil
}

func (w *workloadIdentity) admit(state tls.ConnectionState) (string, error) {
	if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
		return "", fmt.Errorf("verified client certificate required")
	}
	id, err := certificateSPIFFEID(state.PeerCertificates[0])
	if err != nil {
		return "", err
	}
	allowed, err := w.allowedCallers()
	if err != nil {
		return "", err
	}
	if !allowed[id] {
		return "", fmt.Errorf("workload caller is not allowed")
	}
	return id, nil
}

func (w *workloadIdentity) serverTLSConfig() (*tls.Config, error) {
	roots, err := w.roots()
	if err != nil {
		return nil, err
	}
	config := &tls.Config{
		MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs: roots, GetCertificate: w.certificate.GetCertificate,
		// Revalidate the chain on every handshake, including after revocation.
		SessionTicketsDisabled: true,
		VerifyConnection:       func(state tls.ConnectionState) error { _, err := w.admit(state); return err },
	}
	config.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		current := config.Clone()
		current.GetConfigForClient = nil
		roots, err := w.roots()
		current.ClientCAs = roots
		return current, err
	}
	return config, nil
}

func (w *workloadIdentity) handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			httpError(rw, http.StatusForbidden, "workload identity required")
			return
		}
		id, err := w.admit(*r.TLS)
		if err != nil {
			httpError(rw, http.StatusForbidden, "workload identity refused")
			return
		}
		next.ServeHTTP(rw, r.WithContext(context.WithValue(r.Context(), workloadPeerKey{}, id)))
	})
}

func (w *workloadIdentity) upstreamTransport(expected string) (*http.Transport, error) {
	if w == nil || !workloadSPIFFEPattern.MatchString(expected) {
		return nil, fmt.Errorf("solution workload identity unavailable")
	}
	transport := newModuleUpstreamTransport(net.DefaultResolver)
	// Each proxy exchange authenticates the current peer and projected leaf.
	transport.DisableKeepAlives = true
	transport.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS13, GetClientCertificate: w.certificate.GetClientCertificate,
		// SPIFFE authenticates a URI, not a DNS name. VerifyConnection performs
		// the full chain/EKU verification below before comparing that URI.
		InsecureSkipVerify: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return fmt.Errorf("solution certificate required")
			}
			roots, err := w.roots()
			if err != nil {
				return err
			}
			intermediates := x509.NewCertPool()
			for _, cert := range state.PeerCertificates[1:] {
				intermediates.AddCert(cert)
			}
			leaf := state.PeerCertificates[0]
			if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
				return err
			}
			id, err := certificateSPIFFEID(leaf)
			if err != nil {
				return err
			}
			if id != expected {
				return fmt.Errorf("solution SPIFFE ID mismatch")
			}
			return nil
		},
	}
	return transport, nil
}
