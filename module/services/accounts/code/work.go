package main

import (
	"accounts/fixtures"
	"accounts/pkg/adapters"
	"accounts/pkg/analytics"
	"accounts/pkg/auth"
	devvalidator "accounts/pkg/auth/dev"
	ed25519minter "accounts/pkg/auth/ed25519"
	"accounts/pkg/auth/oidc"
	pgauth "accounts/pkg/auth/pg"
	"accounts/pkg/billing"
	pgbilling "accounts/pkg/billing/pg"
	"accounts/pkg/business"
	"accounts/pkg/cache"
	"accounts/pkg/email"
	"accounts/pkg/infra"
	"accounts/pkg/jobs"
	"accounts/pkg/metrics"
	"accounts/pkg/permissionsplugin"
	"context"
	ed25519core "crypto/ed25519"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/codefly-dev/core/wool"
	wooltel "github.com/codefly-dev/core/wool/otel"
	codefly "github.com/codefly-dev/sdk-go"
	"go.opentelemetry.io/otel"
)

func init() {
	WithWork(doWork)
}

func doWork(ctx context.Context) (Clean, error) {
	w := wool.Get(ctx).In("doWork")
	measurementPack, err := metrics.DefaultSeedBundle()
	if err != nil {
		return nil, fmt.Errorf("load measurement seed pack: %w", err)
	}
	w.Info(
		"measurement seed pack loaded",
		wool.Field("dashboard.version", measurementPack.DashboardPack.Version),
		wool.Field("slo.version", measurementPack.SLOPack.Version),
	)
	selectedFixture, err := fixtures.SelectedName()
	if err != nil {
		return nil, err
	}
	stepUpMaxAge, err := configuredMFAStepUpMaxAge()
	if err != nil {
		return nil, err
	}
	adapters.SetRecentStepUpMaxAge(stepUpMaxAge)
	sessionPolicy, err := configuredSessionPolicy()
	if err != nil {
		return nil, err
	}

	// OpenTelemetry — enabled when OTEL_EXPORTER_OTLP_ENDPOINT is
	// set (or by setting OTEL_SERVICE_NAME for stdout-fallback dev
	// mode). The provider exports to any OTLP-compatible collector
	// (Tempo, Jaeger, signoz, Honeycomb, Datadog…). Empty env → no
	// provider registered → all spans are no-ops.
	//
	// FE→BE trace continuation: the browser stamps W3C `traceparent`
	// on every Connect-ES fetch (Sentry's browserTracingIntegration),
	// the api extracts it on the Connect path via otelconnect (see
	// connect_gen.go) and on the raw-gRPC path via otelgrpc (see
	// grpc_gen.go), and starts a child span. End-to-end traces from
	// browser click to SQL query require both this provider AND the
	// CORS allowlist for `traceparent` / `baggage` (connect_gen.go).
	var otelProvider *wooltel.Provider
	var otelMetricProvider interface {
		Shutdown(context.Context) error
	}
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || os.Getenv("OTEL_SERVICE_NAME") != "" {
		p, oerr := wooltel.Enable(wooltel.WithServiceName("saas-starter-api"))
		if oerr != nil {
			w.Warn("OTEL setup failed; continuing without tracing", wool.ErrField(oerr))
		} else {
			otelProvider = p
			w.Info("OTEL enabled",
				wool.Field("endpoint", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")),
				wool.Field("service.name", "saas-starter-api"))
		}
	}
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		p, oerr := enableOTLPMetrics(ctx, "saas-starter-api")
		if oerr != nil {
			w.Warn("OTEL metrics setup failed; continuing without metric export", wool.ErrField(oerr))
		} else {
			otelMetricProvider = p
		}
	}

	store, err := infra.NewPostgresStore(ctx)
	if err != nil {
		return nil, err
	}

	service, err := business.NewService(store)
	if err != nil {
		return nil, err
	}
	// Cross-tenant job administration is isolated from request traffic at the
	// connection-pool boundary. The API exposes payload-free metadata only;
	// replay copies payload bytes inside PostgreSQL under app_job_worker.
	jobWorkerPool, err := infra.NewJobWorkerPool(ctx)
	if err != nil {
		return nil, fmt.Errorf("configure job operations database pool: %w", err)
	}
	jobStore := infra.NewPostgresJobStore(jobWorkerPool)
	var jobOperationsMonitor *jobs.OperationsMonitor
	if otelMetricProvider != nil {
		jobOperationsMonitor, err = jobs.NewOperationsMonitor(
			jobStore,
			otel.Meter("github.com/codefly-dev/module-saas-starter/job-operations"),
			30*time.Second,
		)
		if err != nil {
			return nil, fmt.Errorf("configure durable job metrics: %w", err)
		}
	}
	service.SetJobOperations(jobStore)
	service.SetWebhookJobProducer(store)

	eventRegistry, err := analytics.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	analyticsSink, analyticsEnabled, err := configuredAnalyticsSink()
	if err != nil {
		return nil, err
	}
	var analyticsWorker *jobs.Worker
	if analyticsEnabled {
		productEventOutbox, err := analytics.NewOutbox(store, eventRegistry)
		if err != nil {
			return nil, err
		}
		service.SetProductAnalytics(eventRegistry, productEventOutbox)
		analyticsHandler, err := analytics.NewExportHandler(analytics.ExportHandlerConfig{
			Destination: analyticsSink,
			Deliveries:  jobStore,
		})
		if err != nil {
			return nil, err
		}
		analyticsWorker, err = jobs.NewWorker(jobs.WorkerConfig{
			Store:      jobStore,
			Queue:      analytics.ExportQueue,
			Handler:    analyticsHandler,
			RetryDelay: analytics.ExportRetryDelay,
		})
		if err != nil {
			return nil, err
		}
	}

	// Vault is a required security dependency: API-key HMAC and TOTP seed
	// encryption must be stable across replicas and fail closed.
	vaultClient, err := infra.NewVaultClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("configure Vault security services: %w", err)
	}
	service.SetHasher(vaultClient)
	service.SetMFASecretCipher(vaultClient)
	webhookPolicy := business.NewWebhookEndpointPolicy()
	service.SetWebhookSecurity(vaultClient, webhookPolicy)
	webAuthnRPID, webAuthnDisplayName, webAuthnOrigins, err := configuredWebAuthn()
	if err != nil {
		return nil, fmt.Errorf("configure WebAuthn: %w", err)
	}
	webAuthnEngine, err := infra.NewWebAuthnEngine(webAuthnRPID, webAuthnDisplayName, webAuthnOrigins)
	if err != nil {
		return nil, err
	}
	service.SetWebAuthnEngine(webAuthnEngine)
	if migrated, err := store.MigrateLegacyMFASecrets(ctx, vaultClient); err != nil {
		return nil, fmt.Errorf("migrate legacy MFA secrets: %w", err)
	} else if migrated > 0 {
		w.Info("encrypted legacy MFA secrets", wool.Field("count", migrated))
	}
	if migrated, disabled, err := store.MigrateLegacyWebhookSecrets(ctx, vaultClient); err != nil {
		return nil, fmt.Errorf("migrate legacy webhook secrets: %w", err)
	} else if migrated > 0 {
		w.Info("encrypted legacy webhook secrets",
			wool.Field("count", migrated),
			wool.Field("disabled_empty_secret_endpoints", disabled))
	}

	// Auth pipeline: IdentityResolver + JWTMinter + optional provider
	// validator/exchanger chain for the OAuth code flow.
	// SessionStore needs WithUserTx/WithControlPlane helpers (sessions is
	// RLS-protected per Phase 2H); pass *PostgresStore which
	// implements them.
	sessionStore := pgauth.NewSessionStore(store, sessionPolicy)
	resolver := pgauth.NewResolver(store)
	priv, err := loadSigningKey(ctx)
	if err != nil {
		return nil, err
	}
	minter := ed25519minter.New(ed25519minter.Config{
		Issuer:        "saas-starter",
		Audience:      "saas-starter",
		SessionPolicy: sessionPolicy,
	}, priv, sessionStore)

	service.SetIdentityResolver(resolver)
	service.SetJWTMinter(minter)
	// The standards-named HTTP endpoint must return a top-level JSON Web Key
	// Set. grpc-gateway otherwise wraps the document in JWKSResponse.keys_json,
	// which generic verifiers correctly reject.
	adapters.RegisterHTTPRoute(
		"/v1/auth/.well-known/jwks.json",
		adapters.NewJWKSHTTPHandler(service),
	)

	// Permissions plugin: configure signing keys before NewServer builds the
	// generated gRPC registrations. The ed25519 key is
	// the SAME one we use for JWT minting (saas-starter's cluster
	// identity); plugins running in this cluster verify both with
	// the matching public key. The HMAC fallback is read through the Codefly
	// SDK; the host populates it when spawning plugins. In
	// dev (no host, no env), v2 (ed25519) takes precedence so the
	// approve flow still works end-to-end.
	permissionsplugin.Default().
		WithEd25519Key([]byte(priv)).
		WithHMACSecret([]byte(codefly.ScopedAuthSecret())).
		WithWorkContextAuthority("saas-starter", minter.KeyID())

	// Server-side OAuth state signer. Seeded from the JWT private key so
	// state survives across api restarts and is consistent across
	// instances. Without this, OAuth callbacks rely solely on the FE's
	// sessionStorage check — fine for single-page-app threat models but
	// not defense-in-depth.
	service.SetOAuthStateSigner(auth.NewOAuthStateSigner(priv))

	// Authentication mode is explicit. A selected Codefly fixture implies
	// dev mode; otherwise AUTH_PROVIDER is required. Empty or incomplete
	// production configuration fails startup instead of silently enabling
	// caller-supplied identities.
	authProvider := configuredAuthProvider(selectedFixture)
	v, ex, err := buildProviderStack(authProvider, selectedFixture)
	if err != nil {
		return nil, fmt.Errorf("configure authentication: %w", err)
	}
	if authProvider == "dev" {
		service.SetDevelopmentTokenValidator(v)
	} else {
		oauthPolicy, err := buildOAuthRequestPolicy(authProvider)
		if err != nil {
			return nil, fmt.Errorf("configure OAuth request policy: %w", err)
		}
		service.SetOAuthRequestPolicy(oauthPolicy)
		service.SetTokenValidator(v)
		service.SetCodeExchanger(oidc.AsBusinessExchanger(ex))
	}

	// Audit persistence and matching webhook fan-out share one database
	// transaction. There is no process-local queue to saturate or lose on crash.
	auditEmitter, err := business.NewDurableAuditEmitter(store, store)
	if err != nil {
		return nil, err
	}
	service.SetAuditEmitter(auditEmitter)

	// Audit S3 exporter — polls audit_export_configs every 1 min,
	// uploads new events to each org's bucket as JSONL. No-op until
	// an org configures one via the /admin/audit-export form.
	auditExporter := business.NewAuditExporter(store)
	auditExporter.Start()

	// Every outbound path shares the generated generic job runtime. This
	// separate pool can only read endpoint configuration and project outcomes.
	webhookSender := business.NewWebhookSender(vaultClient, webhookPolicy)
	webhookProjectionPool, err := infra.NewWebhookProjectionPool(ctx)
	if err != nil {
		return nil, fmt.Errorf("configure outbound webhook projection database pool: %w", err)
	}
	webhookJobHandler, err := business.NewOutboundWebhookJobHandler(
		infra.NewPostgresWebhookProjection(webhookProjectionPool), webhookSender,
	)
	if err != nil {
		webhookProjectionPool.Close()
		return nil, err
	}
	webhookWorker, err := jobs.NewWorker(jobs.WorkerConfig{
		Store: jobStore, Queue: business.OutboundWebhookQueue,
		Handler: webhookJobHandler, RetryDelay: business.OutboundWebhookRetryDelay,
	})
	if err != nil {
		webhookProjectionPool.Close()
		return nil, err
	}

	entitlementChecker := business.NewDefaultEntitlementChecker(store)
	service.SetEntitlementChecker(entitlementChecker)

	featureChecker := business.NewDefaultFeatureChecker(store, entitlementChecker)
	service.SetFeatureChecker(featureChecker)

	// Cache wiring — optional. When the `cache` dependency is declared in
	// service.codefly.yaml and Redis is reachable, org-membership lookups
	// get a 30s TTL cache backed by Redis (per-tenant keyed as
	// "orgmember:<orgID>:<userID>"). When the dep is absent or Redis is
	// unreachable, NewRedisCache returns nil and the app runs without
	// caching — zero behavior change, just slower auth checks.
	var closeCache func() error
	if redisCache, c, rerr := infra.NewRedisCache(ctx); rerr == nil && redisCache != nil {
		orgCache := cache.NewOrgMembershipCache(redisCache)
		adapters.WithOrgMembershipCache(orgCache)
		service.SetMembershipInvalidator(adapters.NewCacheInvalidator())
		// Wire Redis-backed access-token revocation. Without this,
		// Logout only kills the refresh chain — old access tokens
		// remain valid until natural expiry (15 min default).
		minter.SetRevoker(cache.NewTokenRevoker(redisCache))
		// Per-org / per-API-key rate limiting. Falls back to
		// allow-all if redisCache is nil (no Redis available).
		adapters.WithRateLimiter(cache.NewRateLimiter(redisCache))
		closeCache = c
	}

	adapters.WithService(service)

	// Separate shared-secret guards for internal RPC admission and forwarded
	// gateway identity. Empty values fail closed; production deploys provide
	// independent high-entropy values to both accounts and auth-sidecar.
	adapters.SetInternalToken(workspaceEnv("internal-auth", "CODEFLY_INTERNAL_TOKEN"))
	adapters.SetGatewayToken(workspaceEnv("internal-auth", "CODEFLY_GATEWAY_TOKEN"))

	// /v1/status — public health probe surface. Probes run in parallel
	// with a 2s budget each; overall status is the worst result. The
	// FE /status page reads this; k8s liveness/readiness can too.
	adapters.RegisterStatusProbe(adapters.StatusProbe{
		Name: "postgres",
		Check: func(ctx context.Context) error {
			return store.Pool().Ping(ctx)
		},
	})
	if vaultClient != nil {
		adapters.RegisterStatusProbe(adapters.StatusProbe{
			Name:  "vault",
			Check: vaultClient.Health,
		})
	}
	adapters.RegisterHTTPRoute("/v1/status", adapters.NewStatusHTTPHandler(service))

	// Email production and transport are split by the generic outbox. Request
	// paths render templates and enqueue exact messages in their product
	// transaction; this worker is the only owner of the provider adapter.
	fromAddr := os.Getenv("EMAIL_FROM")
	if fromAddr == "" {
		fromAddr = "no-reply@localhost"
	}
	appBase := os.Getenv("APP_BASE_URL")
	if appBase == "" {
		appBase = "http://localhost:21931"
	}
	templateStore := infra.NewPostgresTemplateStore(store)
	requestEmailOutbox, err := email.NewOutbox(store, templateStore, fromAddr)
	if err != nil {
		return nil, err
	}
	service.SetEmailOutbox(requestEmailOutbox, appBase)
	workerEmailOutbox, err := email.NewOutbox(jobStore, templateStore, fromAddr)
	if err != nil {
		return nil, err
	}
	emailJobHandler, err := email.NewJobHandler(pickEmailSender(ctx))
	if err != nil {
		return nil, err
	}
	emailWorker, err := jobs.NewWorker(jobs.WorkerConfig{
		Store: jobStore, Queue: email.DeliveryQueue,
		Handler: emailJobHandler, RetryDelay: email.DeliveryRetryDelay,
	})
	if err != nil {
		return nil, err
	}

	// Billing: wire Stripe webhook handler at /v1/billing/webhook AND
	// the authenticated /v1/billing/checkout + /v1/billing/portal
	// endpoints. The sidecar's public-path allowlist covers the
	// webhook; checkout + portal are authenticated via forwarded
	// identity headers.
	var stripeWebhookWorker *jobs.Worker
	var billingWorkerPool interface{ Close() }
	if stripeKey := os.Getenv("STRIPE_API_KEY"); stripeKey != "" {
		whSecret := os.Getenv("STRIPE_WEBHOOK_SECRET")
		if whSecret == "" {
			return nil, fmt.Errorf("billing: STRIPE_WEBHOOK_SECRET is required when STRIPE_API_KEY is configured")
		}
		stripeClient, err := billing.New(billing.Config{APIKey: stripeKey})
		if err != nil {
			return nil, fmt.Errorf("billing: %w", err)
		}
		billingBaseURL, err := configuredBillingBaseURL()
		if err != nil {
			return nil, err
		}
		workerPool, err := infra.NewBillingWorkerPool(ctx)
		if err != nil {
			return nil, fmt.Errorf("billing: configure worker database pool: %w", err)
		}
		billingWorkerPool = workerPool
		workerStore := pgbilling.New(workerPool)
		service.SetBillingRedirects(business.BillingRedirects{
			CheckoutSuccessURL: billingBaseURL + "/admin/billing/success?session_id={CHECKOUT_SESSION_ID}",
			CheckoutCancelURL:  billingBaseURL + "/admin/billing",
			PortalReturnURL:    billingBaseURL + "/admin/billing",
		})

		billingNotifier := &billingEmailNotifier{
			outbox:     workerEmailOutbox,
			billingURL: billingBaseURL + "/admin/billing",
		}

		adapters.RegisterHTTPRoute("/v1/billing/webhook", billing.NewHandler(billing.HandlerDeps{
			Producer:      jobStore,
			WebhookSecret: whSecret,
		}))
		stripeWebhookJobHandler, err := billing.NewStripeWebhookJobHandler(
			billing.NewProcessor(billing.ProcessorDeps{
				Store:    workerStore,
				Client:   stripeClient,
				Notifier: billingNotifier,
			}),
		)
		if err != nil {
			workerPool.Close()
			billingWorkerPool = nil
			return nil, err
		}
		stripeWebhookWorker, err = jobs.NewWorker(jobs.WorkerConfig{
			Store:      jobStore,
			Queue:      billing.StripeWebhookQueue,
			Handler:    stripeWebhookJobHandler,
			RetryDelay: billing.StripeWebhookRetryDelay,
		})
		if err != nil {
			workerPool.Close()
			billingWorkerPool = nil
			return nil, err
		}
		service.SetBillingClient(stripeClient)
		adapters.RegisterHTTPRoute("/v1/billing/checkout", adapters.NewBillingHTTPHandler(service))
		adapters.RegisterHTTPRoute("/v1/billing/portal", adapters.NewBillingHTTPHandler(service))
	}

	// Start background data retention goroutine. Runs once on startup and
	// then every 24 hours, deleting records older than their configured
	// retention period.
	retentionCtx, retentionCancel := context.WithCancel(ctx)
	go func() {
		rw := wool.Get(retentionCtx).In("retention")
		// Run once immediately on startup.
		if deleted, err := service.RunRetention(retentionCtx); err != nil {
			rw.Warn("startup run failed", wool.ErrField(err))
		} else {
			for k, v := range deleted {
				if v > 0 {
					rw.Info("deleted records", wool.Field("count", v), wool.Field("kind", k))
				}
			}
		}

		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-retentionCtx.Done():
				return
			case <-ticker.C:
				if deleted, err := service.RunRetention(retentionCtx); err != nil {
					rw.Warn("scheduled run failed", wool.ErrField(err))
				} else {
					for k, v := range deleted {
						if v > 0 {
							rw.Info("deleted records", wool.Field("count", v), wool.Field("kind", k))
						}
					}
				}
			}
		}
	}()

	if selectedFixture != "" {
		err = fixtures.Seed(ctx, service, selectedFixture)
		if err != nil {
			retentionCancel()
			return nil, err
		}
	}
	if stripeWebhookWorker != nil {
		stripeWebhookWorker.Start(ctx)
	}
	if jobOperationsMonitor != nil {
		jobOperationsMonitor.Start(ctx)
	}
	if analyticsWorker != nil {
		analyticsWorker.Start(ctx)
	}
	emailWorker.Start(ctx)
	webhookWorker.Start(ctx)

	return func() {
		sw := wool.Get(ctx).In("shutdown")
		if stripeWebhookWorker != nil {
			sw.Info("stopping Stripe webhook worker")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := stripeWebhookWorker.Shutdown(shutdownCtx); err != nil {
				sw.Warn("Stripe webhook worker shutdown timed out", wool.ErrField(err))
			}
			cancel()
		}
		if analyticsWorker != nil {
			sw.Info("stopping product analytics export worker")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := analyticsWorker.Shutdown(shutdownCtx); err != nil {
				sw.Warn("product analytics worker shutdown timed out", wool.ErrField(err))
			}
			cancel()
		}
		if jobOperationsMonitor != nil {
			sw.Info("stopping durable job metrics monitor")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := jobOperationsMonitor.Shutdown(shutdownCtx); err != nil {
				sw.Warn("job metrics monitor shutdown timed out", wool.ErrField(err))
			}
			cancel()
		}
		sw.Info("stopping email delivery worker")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := emailWorker.Shutdown(shutdownCtx); err != nil {
			sw.Warn("email delivery worker shutdown timed out", wool.ErrField(err))
		}
		cancel()
		sw.Info("stopping outbound webhook worker")
		shutdownCtx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		if err := webhookWorker.Shutdown(shutdownCtx); err != nil {
			sw.Warn("outbound webhook worker shutdown timed out", wool.ErrField(err))
		}
		cancel()
		sw.Info("closing outbound webhook projection database pool")
		webhookProjectionPool.Close()
		sw.Info("closing generic job worker database pool")
		jobWorkerPool.Close()
		if billingWorkerPool != nil {
			sw.Info("closing Stripe projection database pool")
			billingWorkerPool.Close()
		}
		sw.Info("stopping retention goroutine")
		retentionCancel()
		sw.Info("closing audit emitter")
		auditEmitter.Close()
		sw.Info("audit emitter closed")
		sw.Info("stopping audit exporter")
		auditExporter.Close()
		if closeCache != nil {
			sw.Info("closing redis cache")
			if err := closeCache(); err != nil {
				sw.Warn("redis cache close failed", wool.ErrField(err))
			} else {
				sw.Info("redis cache closed")
			}
		}
		sw.Info("closing store")
		store.Close()
		sw.Info("store closed")
		if otelProvider != nil {
			sw.Info("flushing OTEL provider")
			// Bounded shutdown — exporter has its own flush window
			// (otlptrace's batcher). 5s is a sensible cap.
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := otelProvider.Shutdown(shutdownCtx); err != nil {
				sw.Warn("OTEL shutdown failed", wool.ErrField(err))
			}
		}
		if otelMetricProvider != nil {
			sw.Info("flushing OTEL metrics provider")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := otelMetricProvider.Shutdown(shutdownCtx); err != nil {
				sw.Warn("OTEL metrics shutdown failed", wool.ErrField(err))
			}
		}
	}, nil
}

func configuredAnalyticsSink() (analytics.Destination, bool, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PRODUCT_ANALYTICS_MODE"))) {
	case "", "disabled":
		return analytics.NoopSink{}, false, nil
	case "noop":
		return analytics.NoopSink{}, true, nil
	case "posthog":
		sink, err := analytics.NewPostHog(analytics.PostHogConfig{
			APIKey:         os.Getenv("POSTHOG_PROJECT_API_KEY"),
			PersonalAPIKey: os.Getenv("POSTHOG_PERSONAL_API_KEY"),
			ProjectID:      os.Getenv("POSTHOG_PROJECT_ID"),
			Host:           os.Getenv("POSTHOG_HOST"),
		})
		if err != nil {
			return nil, false, err
		}
		return sink, true, nil
	default:
		return nil, false, fmt.Errorf("PRODUCT_ANALYTICS_MODE must be disabled, noop, or posthog")
	}
}

func configuredMFAStepUpMaxAge() (time.Duration, error) {
	raw := strings.TrimSpace(workspaceEnv("security", "MFA_STEP_UP_MAX_AGE"))
	if raw == "" {
		return auth.DefaultRecentStepUpMaxAge, nil
	}
	maxAge, err := time.ParseDuration(raw)
	if err != nil || maxAge <= 0 || maxAge > 24*time.Hour {
		return 0, fmt.Errorf("MFA_STEP_UP_MAX_AGE must be a positive Go duration no greater than 24h")
	}
	return maxAge, nil
}

func configuredSessionPolicy() (auth.SessionPolicy, error) {
	policy := auth.DefaultSessionPolicy()
	parseDuration := func(name string, fallback time.Duration) (time.Duration, error) {
		raw := strings.TrimSpace(workspaceEnv("security", name))
		if raw == "" {
			return fallback, nil
		}
		value, err := time.ParseDuration(raw)
		if err != nil {
			return 0, fmt.Errorf("%s must be a valid Go duration", name)
		}
		return value, nil
	}

	var err error
	policy.AbsoluteLifetime, err = parseDuration("SESSION_ABSOLUTE_LIFETIME", policy.AbsoluteLifetime)
	if err != nil {
		return auth.SessionPolicy{}, err
	}
	policy.IdleTimeout, err = parseDuration("SESSION_IDLE_TIMEOUT", policy.IdleTimeout)
	if err != nil {
		return auth.SessionPolicy{}, err
	}
	if raw := strings.TrimSpace(workspaceEnv("security", "SESSION_MAX_ACTIVE_DEVICES")); raw != "" {
		policy.MaxActiveDevices, err = strconv.Atoi(raw)
		if err != nil {
			return auth.SessionPolicy{}, fmt.Errorf("SESSION_MAX_ACTIVE_DEVICES must be an integer")
		}
	}
	if err := policy.Validate(); err != nil {
		return auth.SessionPolicy{}, fmt.Errorf("invalid session policy: %w", err)
	}
	if policy.AbsoluteLifetime < time.Hour || policy.AbsoluteLifetime > 365*24*time.Hour {
		return auth.SessionPolicy{}, fmt.Errorf("SESSION_ABSOLUTE_LIFETIME must be between 1h and 8760h")
	}
	if policy.IdleTimeout < 5*time.Minute {
		return auth.SessionPolicy{}, fmt.Errorf("SESSION_IDLE_TIMEOUT must be at least 5m")
	}
	if policy.MaxActiveDevices > 100 {
		return auth.SessionPolicy{}, fmt.Errorf("SESSION_MAX_ACTIVE_DEVICES must not exceed 100")
	}
	return policy, nil
}

func configuredBillingBaseURL() (string, error) {
	raw := strings.TrimSpace(os.Getenv("APP_BASE_URL"))
	if raw == "" {
		if codefly.IsLocal() {
			return "http://localhost:21931", nil
		}
		return "", fmt.Errorf("billing: APP_BASE_URL is required when Stripe is configured")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawPath != "" ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", fmt.Errorf("billing: APP_BASE_URL must be an exact http(s) origin")
	}
	if parsed.Scheme != "https" && parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" {
		return "", fmt.Errorf("billing: APP_BASE_URL must use https outside local development")
	}
	return strings.TrimSuffix(raw, "/"), nil
}

func configuredWebAuthn() (rpID, displayName string, origins []string, err error) {
	rpID = strings.TrimSpace(workspaceEnv("security", "WEBAUTHN_RP_ID"))
	if rpID == "" || strings.ContainsAny(rpID, "/:") {
		return "", "", nil, fmt.Errorf("WEBAUTHN_RP_ID must be a host name without scheme or port")
	}
	displayName = strings.TrimSpace(workspaceEnv("security", "WEBAUTHN_RP_DISPLAY_NAME"))
	if displayName == "" {
		displayName = "SaaS Starter"
	}
	for _, raw := range strings.Split(workspaceEnv("security", "WEBAUTHN_RP_ORIGINS"), ",") {
		origin := strings.TrimSpace(raw)
		if origin == "" {
			continue
		}
		parsed, parseErr := url.Parse(origin)
		if parseErr != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", "", nil, fmt.Errorf("WEBAUTHN_RP_ORIGINS contains invalid origin %q", origin)
		}
		origins = append(origins, strings.TrimSuffix(origin, "/"))
	}
	if len(origins) == 0 {
		return "", "", nil, fmt.Errorf("WEBAUTHN_RP_ORIGINS must contain at least one exact browser origin")
	}
	return rpID, displayName, origins, nil
}

// buildProviderStack returns the (TokenValidator, Exchanger) pair
// matching AUTH_PROVIDER. Supported values:
//
//	dev        — DevValidator reading the dev-admin fixture seed. No
//	             exchanger (no OAuth code flow). Used for local iter.
//	workos     — oidc.Validator + oidc.Exchanger preconfigured for
//	             WorkOS via WORKOS_CLIENT_ID + WORKOS_CLIENT_SECRET.
//	auth0      — same shape, Auth0 preset. Requires AUTH0_DOMAIN +
//	             AUTH0_AUDIENCE + AUTH0_CLIENT_ID/SECRET.
//	google     — google sign-in. GOOGLE_CLIENT_ID + GOOGLE_CLIENT_SECRET.
//	(empty)    — invalid unless the Codefly SDK reports an explicit fixture.
//
// Empty, unknown, and incomplete provider configurations return an error so
// the service cannot start with an ambiguous authentication boundary.
// workspaceEnv reads a key from a named Codefly workspace configuration,
// including its secret namespace, and falls back to a plain process variable
// for deployments that do not use Codefly's configuration provider.
func workspaceEnv(configuration, key string) string {
	if value, err := codefly.For(codefly.Context()).WorkspaceValue(configuration, key); err == nil && value != "" {
		return value
	}
	return os.Getenv(key)
}

func workosEnv(key string) string { return workspaceEnv("workos", key) }

func configuredAuthProvider(selectedFixture string) string {
	if selectedFixture != "" {
		return "dev"
	}
	if provider := strings.ToLower(strings.TrimSpace(workosEnv("AUTH_PROVIDER"))); provider != "" {
		return provider
	}
	return ""
}

func hasConfiguredValue(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && !strings.EqualFold(value, "REPLACE_ME")
}

func configuredOAuthRedirectURIs() []string {
	raw := workosEnv("OAUTH_ALLOWED_REDIRECT_URIS")
	parts := strings.Split(raw, ",")
	redirectURIs := make([]string, 0, len(parts))
	for _, part := range parts {
		if redirectURI := strings.TrimSpace(part); redirectURI != "" {
			redirectURIs = append(redirectURIs, redirectURI)
		}
	}
	return redirectURIs
}

func buildOAuthRequestPolicy(provider string) (*auth.OAuthRequestPolicy, error) {
	return auth.NewOAuthRequestPolicy(provider, configuredOAuthRedirectURIs())
}

func buildProviderStack(provider, selectedFixture string) (auth.TokenValidator, *oidc.Exchanger, error) {
	switch provider {
	case "dev":
		if selectedFixture == "" {
			return nil, nil, fmt.Errorf("development authentication requires an explicit Codefly SDK-selected fixture")
		}
		fixturePath, err := fixtures.FixturePath(selectedFixture)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve development fixture: %w", err)
		}
		v, err := devvalidator.New(fixturePath)
		if err != nil {
			return nil, nil, fmt.Errorf("initialize development fixture validator: %w", err)
		}
		return v, nil, nil

	case "workos":
		clientID := workosEnv("WORKOS_CLIENT_ID")
		clientSecret := workosEnv("WORKOS_CLIENT_SECRET")
		if !hasConfiguredValue(clientID) || !hasConfiguredValue(clientSecret) {
			return nil, nil, fmt.Errorf("AUTH_PROVIDER=workos requires WORKOS_CLIENT_ID and WORKOS_CLIENT_SECRET")
		}
		cfg := oidc.WorkOSConfig(clientID)
		// Allow env override for self-hosted / WorkOS-compatible tenants.
		if iss := workosEnv("WORKOS_ISSUER"); iss != "" {
			cfg.Issuer = iss
		}
		if j := workosEnv("WORKOS_JWKS_URL"); j != "" {
			cfg.JWKSURL = j
		}
		v, err := oidc.New(cfg)
		if err != nil {
			return nil, nil, fmt.Errorf("initialize WorkOS validator: %w", err)
		}
		tokenURL := workosEnv("WORKOS_TOKEN_URL")
		if tokenURL == "" {
			tokenURL = "https://api.workos.com/user_management/authenticate"
		}
		ex, err := oidc.NewExchanger(oidc.ExchangerConfig{
			TokenURL:     tokenURL,
			ClientID:     clientID,
			ClientSecret: clientSecret,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("initialize WorkOS exchanger: %w", err)
		}
		return v, ex, nil

	case "auth0":
		domain := os.Getenv("AUTH0_DOMAIN")
		audience := os.Getenv("AUTH0_AUDIENCE")
		clientID := os.Getenv("AUTH0_CLIENT_ID")
		clientSecret := os.Getenv("AUTH0_CLIENT_SECRET")
		if !hasConfiguredValue(domain) || !hasConfiguredValue(clientID) || !hasConfiguredValue(clientSecret) {
			return nil, nil, fmt.Errorf("AUTH_PROVIDER=auth0 requires AUTH0_DOMAIN, AUTH0_CLIENT_ID, and AUTH0_CLIENT_SECRET")
		}
		v, err := oidc.New(oidc.Auth0Config(domain, audience))
		if err != nil {
			return nil, nil, fmt.Errorf("initialize Auth0 validator: %w", err)
		}
		ex, err := oidc.NewExchanger(oidc.ExchangerConfig{
			TokenURL:     fmt.Sprintf("https://%s/oauth/token", domain),
			ClientID:     clientID,
			ClientSecret: clientSecret,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("initialize Auth0 exchanger: %w", err)
		}
		return v, ex, nil

	case "google":
		clientID := os.Getenv("GOOGLE_CLIENT_ID")
		clientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
		if !hasConfiguredValue(clientID) || !hasConfiguredValue(clientSecret) {
			return nil, nil, fmt.Errorf("AUTH_PROVIDER=google requires GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET")
		}
		v, err := oidc.New(oidc.GoogleConfig(clientID))
		if err != nil {
			return nil, nil, fmt.Errorf("initialize Google validator: %w", err)
		}
		ex, err := oidc.NewExchanger(oidc.ExchangerConfig{
			TokenURL:     "https://oauth2.googleapis.com/token",
			ClientID:     clientID,
			ClientSecret: clientSecret,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("initialize Google exchanger: %w", err)
		}
		return v, ex, nil

	case "":
		return nil, nil, fmt.Errorf("AUTH_PROVIDER is required (use dev only with an explicit local fixture)")
	default:
		return nil, nil, fmt.Errorf("unsupported AUTH_PROVIDER %q", provider)
	}
}

// pickEmailSender chooses a Sender based on environment.
//
//	RESEND_API_KEY set → ResendSender (production)
//	otherwise          → LogSender writing through wool (dev)
func pickEmailSender(ctx context.Context) email.Sender {
	w := wool.Get(ctx).In("pickEmailSender")
	logFn := func(format string, args ...any) {
		w.Info(fmt.Sprintf(format, args...))
	}
	if key := os.Getenv("RESEND_API_KEY"); key != "" {
		s, err := email.NewResendSender(email.ResendConfig{APIKey: key})
		if err != nil {
			w.Warn("resend init failed — falling back to log sender", wool.ErrField(err))
			return email.NewLogSender(logFn)
		}
		return s
	}
	return email.NewLogSender(logFn)
}

// loadSigningKey returns the Ed25519 private key used to sign access and
// refresh tokens.
//
// Production: loads from Vault KV v2 (persistent across restarts).
// Local dev: falls back to an ephemeral key if Vault isn't reachable,
//
//	so `codefly run service frontend --fixture dev-admin` still works on
//	a fresh machine. The ephemeral fallback logs a warning.
func loadSigningKey(ctx context.Context) (ed25519core.PrivateKey, error) {
	vaultAddr, addrErr := codefly.For(ctx).Service("vault").Configuration("vault", "address")
	vaultToken, tokErr := codefly.For(ctx).Service("vault").Secret("vault", "token")
	if addrErr == nil && tokErr == nil && vaultAddr != "" && vaultToken != "" {
		priv, err := ed25519minter.LoadKeyFromVault(ctx, ed25519minter.VaultKeyLoaderConfig{
			Address: vaultAddr,
			Token:   vaultToken,
		})
		if err == nil {
			return priv, nil
		}
		wool.Get(ctx).In("loadSigningKey").Warn("could not load signing key from Vault — falling back to ephemeral", wool.ErrField(err))
	}
	_, priv, err := ed25519minter.GenerateKey()
	return priv, err
}

// billingEmailNotifier converts a completed billing projection into a second
// durable outbox command. It never owns or calls the email transport.
type billingEmailNotifier struct {
	outbox     *email.Outbox
	billingURL string
}

func (n *billingEmailNotifier) EnqueueBillingEmail(ctx context.Context, message billing.BillingEmail) error {
	if n == nil || n.outbox == nil {
		return nil
	}
	variables := make(map[string]string, len(message.Variables)+1)
	for key, value := range message.Variables {
		variables[key] = value
	}
	variables["billing_url"] = n.billingURL
	return n.outbox.EnqueueTemplate(ctx, email.TemplateRequest{
		DeliveryKey: message.DeliveryKey,
		Scope:       email.TenantScope(message.OrganizationID),
		Source:      "saas.billing",
		Template:    message.Template,
		To:          message.To,
		Variables:   variables,
	})
}
