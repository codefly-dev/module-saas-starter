// Command custody-qualification is an explicitly local, bounded Accounts process.
// It performs no migrations, grants, seeding or managed-resource operations.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"accounts/pkg/adapters"
	"accounts/pkg/auth"
	minter "accounts/pkg/auth/ed25519"
	pgauth "accounts/pkg/auth/pg"
	"accounts/pkg/business"
	"accounts/pkg/infra"

	"github.com/google/uuid"
)

type config struct {
	ReaderURL              string                                      `json:"reader_url"`
	WriterURL              string                                      `json:"writer_url"`
	VaultURL               string                                      `json:"vault_url"`
	VaultToken             string                                      `json:"vault_token"`
	SigningKeyFile         string                                      `json:"signing_key_file"`
	TLSCertFile            string                                      `json:"tls_cert_file"`
	TLSKeyFile             string                                      `json:"tls_key_file"`
	ClientCAFile           string                                      `json:"client_ca_file"`
	InternalCredentialFile string                                      `json:"internal_credential_file"`
	OwnerTokenFile         string                                      `json:"owner_token_file"`
	StateFile              string                                      `json:"state_file"`
	OwnerID                string                                      `json:"owner_id"`
	OrgID                  string                                      `json:"org_id"`
	Issuer                 string                                      `json:"issuer"`
	AuthIssuer             string                                      `json:"auth_issuer"`
	AuthAudience           string                                      `json:"auth_audience"`
	Consumers              map[string]adapters.ExecutionConsumerPolicy `json:"consumers"`
}

func main() {
	local := flag.Bool("local-qualification", false, "required: local fixtures only")
	file := flag.String("config", "", "private JSON configuration file")
	lifetime := flag.Duration("max-runtime", 15*time.Minute, "bounded process lifetime (maximum 30m)")
	flag.Parse()
	if !*local || *file == "" || *lifetime <= 0 || *lifetime > 30*time.Minute {
		fmt.Fprintln(os.Stderr, "explicit bounded local qualification required")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *lifetime)
	defer cancel()
	if run(ctx, *file) != nil {
		fmt.Fprintln(os.Stderr, "local Accounts qualification failed (details suppressed)")
		os.Exit(1)
	}
}

func privateRead(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("private regular file required")
	}
	return os.ReadFile(path)
}
func localURL(raw string, schemes ...string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	allowed := false
	for _, s := range schemes {
		if u.Scheme == s {
			allowed = true
		}
	}
	return allowed && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")
}
func privateWrite(path string, value []byte) error {
	if !filepath.IsAbs(path) {
		return errors.New("absolute private output required")
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil || !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
		return errors.New("private output directory required")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".custody-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(value); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func run(ctx context.Context, file string) error {
	raw, err := privateRead(file)
	if err != nil {
		return err
	}
	var c config
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return err
	}
	if !localURL(c.ReaderURL, "postgres", "postgresql") || !localURL(c.WriterURL, "postgres", "postgresql") || !localURL(c.VaultURL, "http", "https") || c.Issuer == "" || c.AuthIssuer == "" || c.AuthAudience == "" || c.OwnerTokenFile == c.StateFile {
		return errors.New("loopback configuration required")
	}
	// Disallow a libpq host query override from escaping the loopback fixture.
	for _, raw := range []string{c.ReaderURL, c.WriterURL} {
		u, _ := url.Parse(raw)
		for name := range u.Query() {
			if name != "sslmode" && name != "pool_max_conns" {
				return errors.New("unsupported local database query option")
			}
		}
	}
	owner, err := uuid.Parse(c.OwnerID)
	if err != nil || owner == uuid.Nil {
		return errors.New("owner required")
	}
	org, err := uuid.Parse(c.OrgID)
	if err != nil || org == uuid.Nil {
		return errors.New("organization required")
	}
	keyPEM, err := privateRead(c.SigningKeyFile)
	if err != nil {
		return err
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return errors.New("signing key required")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return err
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return errors.New("Ed25519 key required")
	}
	if _, err = privateRead(c.TLSKeyFile); err != nil {
		return err
	}
	cert, err := tls.LoadX509KeyPair(c.TLSCertFile, c.TLSKeyFile)
	if err != nil {
		return err
	}
	ca, err := os.ReadFile(c.ClientCAFile)
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return errors.New("client CA required")
	}
	internal, err := privateRead(c.InternalCredentialFile)
	if err != nil || len(strings.TrimSpace(string(internal))) < 32 {
		return errors.New("separate revision credential required")
	}
	store, err := infra.NewPostgresStoreWithCapabilities(ctx, c.ReaderURL, c.WriterURL)
	if err != nil {
		return err
	}
	defer store.Close()
	service, err := business.NewService(store)
	if err != nil {
		return err
	}
	jwt := minter.New(minter.Config{Issuer: c.AuthIssuer, Audience: c.AuthAudience}, key, pgauth.NewSessionStore(store))
	service.SetJWTMinter(jwt)
	adapters.WithService(service)
	adapters.SetInternalToken(strings.TrimSpace(string(internal)))
	authority := &adapters.WorkContextAuthorityServer{}
	authority.Configure(adapters.WorkContextAuthorityConfiguration{Issuer: c.Issuer, KeyID: jwt.KeyID(), PrivateKey: key, Authority: store})
	tc := &tls.Config{Certificates: []tls.Certificate{cert}, ClientCAs: roots, MinVersion: tls.VersionTLS13}
	broker, err := adapters.NewExecutionCustodyServer(adapters.ExecutionCustodyConfig{Authority: authority, Minter: jwt, Store: store, Cipher: infra.NewVaultClientDirect(c.VaultURL, c.VaultToken), Consumers: c.Consumers}, tc)
	if err != nil {
		return err
	}
	// The same signing public key serves the canonical Work Context JWKS. Access
	// and Work Context issuer/audience verification remain separate contracts.
	privateHandler := broker.Handler
	broker.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/.well-known/jwks.json" {
			if r.Method != http.MethodGet {
				w.WriteHeader(405)
				return
			}
			keys, err := jwt.JWKS()
			if err != nil {
				w.WriteHeader(503)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write([]byte(keys))
			return
		}
		privateHandler.ServeHTTP(w, r)
	})
	tenant, err := adapters.NewCustodyWorkContextGRPC(authority, jwt, false, tc)
	if err != nil {
		return err
	}
	revision, err := adapters.NewCustodyWorkContextGRPC(authority, jwt, true, tc)
	if err != nil {
		return err
	}
	listeners := make([]net.Listener, 0, 3)
	defer func() {
		for _, l := range listeners {
			_ = l.Close()
		}
	}()
	for i := 0; i < 3; i++ {
		l, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			return err
		}
		listeners = append(listeners, l)
	}
	pair, err := jwt.Mint(ctx, &auth.Identity{UserID: owner, OrgID: org})
	if err != nil {
		return err
	}
	if err = privateWrite(c.OwnerTokenFile, []byte(pair.AccessToken)); err != nil {
		return err
	}
	failures := make(chan error, 3)
	go func() { failures <- broker.ServeTLS(listeners[0], "", "") }()
	go func() { failures <- tenant.Serve(listeners[1]) }()
	go func() { failures <- revision.Serve(listeners[2]) }()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = broker.Shutdown(shutdown)
		tenant.Stop()
		revision.Stop()
	}()
	state, _ := json.Marshal(map[string]string{"broker_url": "https://" + listeners[0].Addr().String(), "tenant_grpc": listeners[1].Addr().String(), "internal_grpc": listeners[2].Addr().String(), "jwks_url": "https://" + listeners[0].Addr().String() + "/v1/auth/.well-known/jwks.json"})
	if err = privateWrite(c.StateFile, state); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return nil
	case <-failures:
		return errors.New("listener stopped")
	}
}
