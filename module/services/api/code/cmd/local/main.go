package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"api/pkg/adapters"
	ed25519minter "api/pkg/auth/ed25519"
	pgauth "api/pkg/auth/pg"
	"api/pkg/business"
	"api/pkg/infra"

	"google.golang.org/grpc"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	grpcPort := uint16(9090)
	httpPort := uint16(8080)

	// Postgres
	pgURL := env("POSTGRES_URL", "postgres://postgres:postgres@localhost:5432/user_management?sslmode=disable")
	store, err := infra.NewPostgresStoreFromURL(ctx, pgURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "postgres: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	// Business service
	service, err := business.NewService(store)
	if err != nil {
		fmt.Fprintf(os.Stderr, "business: %v\n", err)
		os.Exit(1)
	}

	// Vault (optional — falls back to local hashing)
	vaultAddr := env("VAULT_ADDR", "http://localhost:8200")
	vaultToken := env("VAULT_TOKEN", "dev-token")
	vaultClient := infra.NewVaultClientDirect(vaultAddr, vaultToken)
	service.SetHasher(vaultClient)

	// Auth pipeline: pg-backed resolver + ed25519 minter. The signing
	// key is loaded from Vault when available so sessions survive a
	// restart; otherwise we fall back to an ephemeral key with a warning.
	sessionStore := pgauth.NewSessionStore(store.Pool())
	resolver := pgauth.NewResolver(store.Pool())
	priv, err := ed25519minter.LoadKeyFromVault(ctx, ed25519minter.VaultKeyLoaderConfig{
		Address: vaultAddr,
		Token:   vaultToken,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: vault key load failed (%v) — using ephemeral key\n", err)
		_, priv, err = ed25519minter.GenerateKey()
		if err != nil {
			fmt.Fprintf(os.Stderr, "ed25519: %v\n", err)
			os.Exit(1)
		}
	}
	minter := ed25519minter.New(ed25519minter.Config{
		Issuer:   "saas-starter-local",
		Audience: "saas-starter-local",
	}, priv, sessionStore)
	service.SetIdentityResolver(resolver)
	service.SetJWTMinter(minter)

	// Audit (async, in-process)
	auditEmitter := business.NewAsyncAuditEmitter(store, 1024)
	service.SetAuditEmitter(auditEmitter)

	// Entitlements
	entitlementChecker := business.NewDefaultEntitlementChecker(store)
	service.SetEntitlementChecker(entitlementChecker)

	// Feature flags
	featureChecker := business.NewDefaultFeatureChecker(store, entitlementChecker)
	service.SetFeatureChecker(featureChecker)

	// Slack notifications (optional)
	slackURL := os.Getenv("SLACK_WEBHOOK_URL")
	if slackURL != "" {
		service.SetSlackNotifier(business.NewSlackNotifier(slackURL))
	}

	// Wire adapters
	adapters.WithService(service)

	// Server with quota enforcement
	config := &adapters.Configuration{
		EndpointGrpcPort: grpcPort,
		EndpointHttpPort: &httpPort,
	}
	server, err := adapters.NewServer(config,
		grpc.UnaryInterceptor(adapters.QuotaInterceptor(entitlementChecker)),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "server: %v\n", err)
		os.Exit(1)
	}

	go func() {
		if err := server.Start(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "server error: %v\n", err)
			os.Exit(1)
		}
	}()

	fmt.Println("saas-starter API running — gRPC :9090, REST :8080")
	fmt.Println("postgres:", pgURL)

	<-ctx.Done()
	server.Stop()
	fmt.Println("shutdown complete")
}
