package main

import (
	"accounts/pkg/adapters"
	"accounts/pkg/auth"
	"accounts/pkg/certreload"
	"accounts/pkg/infra"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/codefly-dev/core/wool"
	codefly "github.com/codefly-dev/sdk-go"
	"google.golang.org/grpc"
)

// This file mounts the private adapter in the ordinary Accounts process. The
// deployment supplies identities and policy, never another issuer or store.
type executionCustodyHost struct {
	broker   *http.Server
	revision *grpc.Server
	identity *certreload.Reloader
}

type executionCustodyProjection struct {
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
	// The custody and revision listeners share this identity. The Reloader
	// re-reads the mounted leaf and client CA bundle (through the same
	// permission-checking projected reader) whenever their contents change and
	// serves the new material on the next handshake, so neither a rotated 24h
	// leaf nor a re-issued CA needs a pod restart; a malformed replacement is
	// rejected and the last good material keeps serving. Certificates is left
	// empty on purpose: crypto/tls skips GetCertificate when it is populated and
	// the client sends no SNI, which would pin the startup leaf forever.
	reloader, err := certreload.New(config.TLSCertFile, config.TLSKeyFile, config.ClientCAFile, projectedCustodyFile)
	if err != nil {
		return nil, errors.New("invalid custody TLS identity")
	}
	// A stable instance of the canonical implementation shares the existing
	// issuer/key/store. Generated server setup may configure its singleton later.
	authority := &adapters.WorkContextAuthorityServer{}
	authority.Configure(adapters.WorkContextAuthorityConfiguration{Issuer: "saas-starter", KeyID: keyID, PrivateKey: key, Authority: store})
	tc := &tls.Config{GetCertificate: reloader.GetCertificate, GetConfigForClient: reloader.GetConfigForClient, ClientCAs: reloader.ClientCAs(), MinVersion: tls.VersionTLS13}
	broker, err := adapters.NewExecutionCustodyServer(adapters.ExecutionCustodyConfig{Authority: authority, Minter: minter, Store: store, Cipher: cipher, Consumers: config.Consumers}, tc)
	if err != nil {
		return nil, err
	}
	revision, err := adapters.NewCustodyWorkContextGRPC(authority, minter, true, tc)
	if err != nil {
		return nil, err
	}
	return &executionCustodyHost{broker: broker, revision: revision, identity: reloader}, nil
}

func startExecutionCustody(ctx context.Context, host *executionCustodyHost) (func(), error) {
	if host == nil {
		return func() {}, nil
	}
	if len(strings.TrimSpace(workspaceEnv("internal-auth", "CODEFLY_INTERNAL_TOKEN"))) < 32 {
		return nil, errors.New("private revision listener requires configured internal credential")
	}
	// A refused rotation otherwise looks exactly like no rotation at all, and only
	// surfaces hours later as expired-leaf handshake failures. Report both
	// outcomes; the reloader suppresses repeats of a standing failure. Neither the
	// message nor the paths carry key material.
	host.identity.Observe(func(err error) {
		w := wool.Get(ctx).In("executionCustody.identity")
		if err != nil {
			w.Warn("custody TLS material was not reloaded; continuing to serve the last validated leaf and client CA", wool.Field("reason", err.Error()))
			return
		}
		w.Info("custody TLS material reloaded without restart")
	})
	listeners := make([]net.Listener, 0, 2)
	for _, name := range []string{"custody", "revision"} {
		endpoint, err := codefly.For(ctx).WithDefaultNetwork().Endpoint(name).API("tcp").ResolveNetworkInstance()
		if err != nil || endpoint == nil || endpoint.Port <= 0 {
			for _, listener := range listeners {
				_ = listener.Close()
			}
			return nil, errors.New("private Accounts Codefly endpoint required")
		}
		listener, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(int(endpoint.Port))))
		if err != nil {
			for _, opened := range listeners {
				_ = opened.Close()
			}
			return nil, errors.New("private Accounts listener unavailable")
		}
		listeners = append(listeners, listener)
	}
	go func() {
		if err := host.broker.ServeTLS(listeners[0], "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			panic("private execution custody listener failed")
		}
	}()
	go func() {
		if err := host.revision.Serve(listeners[1]); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			panic("private authorization revision listener failed")
		}
	}()
	return func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done := make(chan struct{})
		go func() { host.revision.GracefulStop(); close(done) }()
		if host.broker.Shutdown(shutdown) != nil {
			_ = host.broker.Close()
		}
		select {
		case <-done:
		case <-shutdown.Done():
			host.revision.Stop()
		}
	}, nil
}
