package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"backend/pkg/adapters"
	"backend/pkg/business"
	"backend/pkg/infra"
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

	// JWT (ephemeral key for local dev)
	tokenService, err := infra.NewTokenServiceLocal()
	if err != nil {
		fmt.Fprintf(os.Stderr, "jwt: %v\n", err)
		os.Exit(1)
	}
	service.SetTokenSigner(tokenService)

	// Audit (async, in-process)
	auditEmitter := business.NewAsyncAuditEmitter(store, 1024)
	service.SetAuditEmitter(auditEmitter)

	// Entitlements
	entitlementChecker := business.NewDefaultEntitlementChecker(store)
	service.SetEntitlementChecker(entitlementChecker)

	// Feature flags
	featureChecker := business.NewDefaultFeatureChecker(store, entitlementChecker)
	service.SetFeatureChecker(featureChecker)

	// Wire adapters
	adapters.WithService(service)

	// Server
	config := &adapters.Configuration{
		EndpointGrpcPort: grpcPort,
		EndpointHttpPort: &httpPort,
	}
	server, err := adapters.NewServer(config)
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
