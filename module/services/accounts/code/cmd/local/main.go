package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"accounts/pkg/adapters"
	"accounts/pkg/auth"
	ed25519minter "accounts/pkg/auth/ed25519"
	pgauth "accounts/pkg/auth/pg"
	"accounts/pkg/business"
	"accounts/pkg/email"
	"accounts/pkg/infra"
	"accounts/pkg/jobs"

	"github.com/codefly-dev/core/wool"
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
	w := wool.Get(ctx).In("cmd.local")

	grpcPort := uint16(9090)
	httpPort := uint16(8080)

	// Postgres
	pgURL := env("POSTGRES_URL", "postgres://postgres:postgres@localhost:5432/user_management?sslmode=disable")
	store, err := infra.NewPostgresStoreFromURL(ctx, pgURL)
	if err != nil {
		w.Error("postgres connect failed", wool.ErrField(err))
		os.Exit(1)
	}
	defer store.Close()

	// Business service
	service, err := business.NewService(store)
	if err != nil {
		w.Error("business service init failed", wool.ErrField(err))
		os.Exit(1)
	}

	// Vault (optional — falls back to local hashing)
	vaultAddr := env("VAULT_ADDR", "http://localhost:8200")
	vaultToken := env("VAULT_TOKEN", "dev-token")
	vaultClient := infra.NewVaultClientDirect(vaultAddr, vaultToken)
	service.SetHasher(vaultClient)
	webhookPolicy := business.NewWebhookEndpointPolicy()
	service.SetWebhookSecurity(vaultClient, webhookPolicy)
	if _, _, err := store.MigrateLegacyWebhookSecrets(ctx, vaultClient); err != nil {
		w.Error("webhook secret migration failed", wool.ErrField(err))
		os.Exit(1)
	}

	// Auth pipeline: pg-backed resolver + ed25519 minter. The signing
	// key is loaded from Vault when available so sessions survive a
	// restart; otherwise we fall back to an ephemeral key with a warning.
	sessionPolicy := auth.DefaultSessionPolicy()
	sessionStore := pgauth.NewSessionStore(store, sessionPolicy)
	resolver := pgauth.NewResolver(store)
	priv, err := ed25519minter.LoadKeyFromVault(ctx, ed25519minter.VaultKeyLoaderConfig{
		Address: vaultAddr,
		Token:   vaultToken,
	})
	if err != nil {
		w.Warn("vault key load failed — using ephemeral key", wool.ErrField(err))
		_, priv, err = ed25519minter.GenerateKey()
		if err != nil {
			w.Error("ed25519 generate failed", wool.ErrField(err))
			os.Exit(1)
		}
	}
	minter := ed25519minter.New(ed25519minter.Config{
		Issuer:        "saas-starter-local",
		Audience:      "saas-starter-local",
		SessionPolicy: sessionPolicy,
	}, priv, sessionStore)
	service.SetIdentityResolver(resolver)
	service.SetJWTMinter(minter)

	jobPool, err := infra.NewJobWorkerPoolFromURL(ctx, pgURL)
	if err != nil {
		w.Error("job worker pool failed", wool.ErrField(err))
		os.Exit(1)
	}
	defer jobPool.Close()
	jobStore := infra.NewPostgresJobStore(jobPool)
	service.SetWebhookJobProducer(store)
	templateStore := infra.NewPostgresTemplateStore(store)
	emailOutbox, err := email.NewOutbox(
		store,
		templateStore,
		env("EMAIL_FROM", "no-reply@localhost"),
	)
	if err != nil {
		w.Error("email outbox init failed", wool.ErrField(err))
		os.Exit(1)
	}
	service.SetEmailOutbox(emailOutbox, env("APP_BASE_URL", "http://localhost:21931"))
	emailHandler, err := email.NewJobHandler(email.NewLogSender(func(format string, args ...any) {
		w.Info("email delivery", wool.Field("preview", format), wool.Field("args", args))
	}))
	if err != nil {
		w.Error("email handler init failed", wool.ErrField(err))
		os.Exit(1)
	}
	emailWorker, err := jobs.NewWorker(jobs.WorkerConfig{
		Store: jobStore, Queue: email.DeliveryQueue,
		Handler: emailHandler, RetryDelay: email.DeliveryRetryDelay,
	})
	if err != nil {
		w.Error("email worker init failed", wool.ErrField(err))
		os.Exit(1)
	}

	// Durable audit + transactional generic webhook outbox.
	auditEmitter, err := business.NewDurableAuditEmitter(store, store)
	if err != nil {
		w.Error("audit emitter init failed", wool.ErrField(err))
		os.Exit(1)
	}
	service.SetAuditEmitter(auditEmitter)
	webhookSender := business.NewWebhookSender(vaultClient, webhookPolicy)
	webhookPool, err := infra.NewWebhookProjectionPoolFromURL(ctx, pgURL)
	if err != nil {
		w.Error("webhook projection pool failed", wool.ErrField(err))
		os.Exit(1)
	}
	defer webhookPool.Close()
	webhookHandler, err := business.NewOutboundWebhookJobHandler(
		infra.NewPostgresWebhookProjection(webhookPool), webhookSender,
	)
	if err != nil {
		w.Error("webhook handler init failed", wool.ErrField(err))
		os.Exit(1)
	}
	webhookWorker, err := jobs.NewWorker(jobs.WorkerConfig{
		Store: jobStore, Queue: business.OutboundWebhookQueue,
		Handler: webhookHandler, RetryDelay: business.OutboundWebhookRetryDelay,
	})
	if err != nil {
		w.Error("webhook job worker init failed", wool.ErrField(err))
		os.Exit(1)
	}
	webhookWorker.Start(ctx)
	emailWorker.Start(ctx)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := emailWorker.Shutdown(shutdownCtx); err != nil {
			w.Warn("email worker shutdown timed out", wool.ErrField(err))
		}
		if err := webhookWorker.Shutdown(shutdownCtx); err != nil {
			w.Warn("outbound webhook worker shutdown timed out", wool.ErrField(err))
		}
	}()

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

	// Quotas are enforced by the domain commands that own the corresponding
	// writes; metered consumption is atomic in UsageService.
	config := &adapters.Configuration{
		EndpointGrpcPort: grpcPort,
		EndpointHttpPort: &httpPort,
	}
	server, err := adapters.NewServer(config)
	if err != nil {
		w.Error("server init failed", wool.ErrField(err))
		os.Exit(1)
	}

	go func() {
		if err := server.Start(ctx); err != nil {
			w.Error("server runtime failed", wool.ErrField(err))
			os.Exit(1)
		}
	}()

	w.Info("saas-starter API running",
		wool.Field("grpc_port", grpcPort),
		wool.Field("http_port", httpPort),
		wool.Field("postgres", pgURL))

	<-ctx.Done()
	server.Stop()
	w.Info("shutdown complete")
}
