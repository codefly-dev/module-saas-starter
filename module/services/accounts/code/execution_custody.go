package main

import (
	"accounts/pkg/adapters"
	"accounts/pkg/auth"
	"accounts/pkg/infra"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	codefly "github.com/codefly-dev/sdk-go"
	"google.golang.org/grpc"
)

// This file mounts the private adapter in the ordinary Accounts process. The
// deployment supplies identities and policy, never another issuer or store.
type executionCustodyHost struct {
	broker   *http.Server
	revision *grpc.Server
	tenant   *grpc.Server
}

type executionCustodyProjection struct {
	EnableTenant bool                                        `json:"enable_tenant"`
	TLSCertFile  string                                      `json:"tls_cert_file"`
	TLSKeyFile   string                                      `json:"tls_key_file"`
	ClientCAFile string                                      `json:"client_ca_file"`
	Consumers    map[string]adapters.ExecutionConsumerPolicy `json:"consumers"`
}

func projectedCustodyFile(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("absolute custody projection path required")
	}
	// Stat follows Kubernetes atomic projected-secret symlinks. The resolved file
	// must be regular and private; group read permits an assigned fsGroup.
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("custody projection unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0027 != 0 || info.Size() > 128<<10 {
		return nil, errors.New("custody projection must be a bounded private file")
	}
	data, err := io.ReadAll(io.LimitReader(f, (128<<10)+1))
	if err != nil || len(data) > 128<<10 {
		return nil, errors.New("custody projection unreadable")
	}
	return data, nil
}

func configuredExecutionCustody(store *infra.PostgresStore, cipher *infra.VaultClient, minter auth.JWTMinter, keyID string, key ed25519.PrivateKey, revokerWired, failOpen bool) (*executionCustodyHost, error) {
	path := strings.TrimSpace(workspaceEnv("security", "EXECUTION_CUSTODY_CONFIG_FILE"))
	if path == "" {
		return nil, nil
	}
	if !revokerWired || failOpen {
		return nil, errors.New("execution custody requires fail-closed access-token revocation")
	}
	profile, err := infra.DatabaseTransportProfile()
	if err != nil || profile == "" {
		return nil, errors.New("execution custody requires explicit verified-tls or local-identity-proxy database transport")
	}
	data, err := projectedCustodyFile(path)
	if err != nil {
		return nil, err
	}
	var config executionCustodyProjection
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if dec.Decode(&config) != nil || dec.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid custody configuration")
	}
	cert, err := projectedCustodyFile(config.TLSCertFile)
	if err != nil {
		return nil, err
	}
	keyPEM, err := projectedCustodyFile(config.TLSKeyFile)
	if err != nil {
		return nil, err
	}
	ca, err := projectedCustodyFile(config.ClientCAFile)
	if err != nil {
		return nil, err
	}
	pair, err := tls.X509KeyPair(cert, keyPEM)
	if err != nil {
		return nil, errors.New("invalid custody TLS identity")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("invalid custody client CA")
	}
	// A stable instance of the canonical implementation shares the existing
	// issuer/key/store. Generated server setup may configure its singleton later.
	authority := &adapters.WorkContextAuthorityServer{}
	authority.Configure(adapters.WorkContextAuthorityConfiguration{Issuer: "saas-starter", KeyID: keyID, PrivateKey: key, Authority: store})
	tc := &tls.Config{Certificates: []tls.Certificate{pair}, ClientCAs: roots, MinVersion: tls.VersionTLS13}
	broker, err := adapters.NewExecutionCustodyServer(adapters.ExecutionCustodyConfig{Authority: authority, Minter: minter, Store: store, Cipher: cipher, Consumers: config.Consumers}, tc)
	if err != nil {
		return nil, err
	}
	revision, err := adapters.NewCustodyWorkContextGRPC(authority, minter, true, tc)
	if err != nil {
		return nil, err
	}
	host := &executionCustodyHost{broker: broker, revision: revision}
	if config.EnableTenant {
		host.tenant, err = adapters.NewCustodyWorkContextGRPC(authority, minter, false, tc)
		if err != nil {
			return nil, err
		}
	}
	return host, nil
}

func startExecutionCustody(ctx context.Context, host *executionCustodyHost) (func(), error) {
	return startExecutionCustodyWithListener(host, func(name string) (net.Listener, error) {
		endpoint, err := codefly.For(ctx).WithDefaultNetwork().Endpoint(name).API("tcp").ResolveNetworkInstance()
		if err != nil || endpoint == nil || endpoint.Port <= 0 {
			return nil, errors.New("private Accounts Codefly endpoint required")
		}
		listener, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(int(endpoint.Port))))
		if err != nil {
			return nil, errors.New("private Accounts listener unavailable")
		}
		return listener, nil
	})
}

// Bind every enabled endpoint before exposing any handler. This same lifecycle
// is exercised with real loopback listeners in the normal-host acceptance test.
func startExecutionCustodyWithListener(host *executionCustodyHost, listen func(string) (net.Listener, error)) (func(), error) {
	if host == nil {
		return func() {}, nil
	}
	if len(strings.TrimSpace(workspaceEnv("internal-auth", "CODEFLY_INTERNAL_TOKEN"))) < 32 {
		return nil, errors.New("private revision listener requires configured internal credential")
	}
	names := []string{"custody", "revision"}
	servers := []*grpc.Server{host.revision}
	if host.tenant != nil {
		names = append(names, "tenant")
		servers = append(servers, host.tenant)
	}
	listeners := make([]net.Listener, 0, len(names))
	for _, name := range names {
		listener, err := listen(name)
		if err != nil {
			for _, opened := range listeners {
				_ = opened.Close()
			}
			return nil, err
		}
		listeners = append(listeners, listener)
	}
	go func() {
		if err := host.broker.ServeTLS(listeners[0], "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			panic("private execution custody listener failed")
		}
	}()
	for i, server := range servers {
		go func() {
			if err := server.Serve(listeners[i+1]); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
				panic("private Accounts WorkContext listener failed")
			}
		}()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var wg sync.WaitGroup
			for _, server := range servers {
				wg.Add(1)
				go func() { defer wg.Done(); server.GracefulStop() }()
			}
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			if host.broker.Shutdown(shutdown) != nil {
				_ = host.broker.Close()
			}
			select {
			case <-done:
			case <-shutdown.Done():
				for _, server := range servers {
					server.Stop()
				}
			}
		})
	}, nil
}
