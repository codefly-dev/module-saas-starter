package main

import (
	"accounts/fixtures"
	"accounts/pkg/abuse"
	"accounts/pkg/adapters"
	"accounts/pkg/analytics"
	"accounts/pkg/auth"
	devvalidator "accounts/pkg/auth/dev"
	ed25519minter "accounts/pkg/auth/ed25519"
	"accounts/pkg/auth/headerjwt"
	"accounts/pkg/auth/oidc"
	pgauth "accounts/pkg/auth/pg"
	workosauth "accounts/pkg/auth/workos"
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

	// OpenTelemetry always targets the in-graph collector. Codefly owns its
	// host and port; the accounts process never reads or hardcodes a collector
	// address. The collector configuration independently selects debug output
	// or an external OTLP/HTTP destination.
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
	collectorNetwork, err := codefly.For(ctx).
		Service("telemetry").
		Endpoint("grpc").
		API("grpc").
		ResolveNetworkInstance()
	if err != nil {
		return nil, fmt.Errorf("resolve telemetry collector through Codefly: %w", err)
	}
	p, oerr := wooltel.Enable(
		wooltel.WithServiceName("saas-starter-api"),
		wooltel.WithEndpoint(collectorNetwork.Host),
		wooltel.WithInsecure(),
	)
	if oerr != nil {
		return nil, fmt.Errorf("configure OTEL tracing: %w", oerr)
	}
	otelProvider = p
	w.Info("OTEL enabled",
		wool.Field("endpoint", collectorNetwork.Host),
		wool.Field("service.name", "saas-starter-api"))
	metricProvider, oerr := enableOTLPMetrics(ctx, "saas-starter-api", collectorNetwork.Host)
	if oerr != nil {
		return nil, fmt.Errorf("configure OTEL metrics: %w", oerr)
	}
	otelMetricProvider = metricProvider

	store, err := infra.NewPostgresStore(ctx)
	if err != nil {
		return nil, err
	}

	service, err := business.NewService(store)
	if err != nil {
		return nil, err
	}
	abuseVerifier, err := configuredAbuseVerifier()
	if err != nil {
		return nil, err
	}
	service.SetAbuseVerifier(abuseVerifier)
	if err := service.SetAcquisitionMode(os.Getenv("ACQUISITION_MODE")); err != nil {
		return nil, err
	}
	if configured := strings.TrimSpace(os.Getenv("WAITLIST_EMAIL_VERIFICATION")); configured != "" {
		required, parseErr := strconv.ParseBool(configured)
		if parseErr != nil {
			return nil, fmt.Errorf("configure waitlist email verification: %w", parseErr)
		}
		service.SetWaitlistEmailVerification(required)
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
	resolver.SetBootstrapAdminEmail(applicationEnv("BOOTSTRAP_ADMIN_EMAIL"))
	signupMode, err := auth.ParseSignupMode(identityEnv("IDENTITY_SIGNUP_MODE"))
	if err != nil {
		return nil, err
	}
	resolver.SetSignupMode(signupMode)
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

	// Authentication mode is explicit in the Codefly identity configuration.
	// A selected fixture is an optional data seed and cannot replace the
	// configured provider. Fixture authentication must itself be selected and
	// additionally requires an explicit fixture.
	authProvider := configuredAuthProvider(selectedFixture)
	v, ex, err := buildProviderStack(authProvider, selectedFixture)
	if err != nil {
		return nil, fmt.Errorf("configure authentication: %w", err)
	}
	if authProvider == "dev" {
		service.SetDevelopmentTokenValidator(v)
	} else if authProvider == "header-jwt" {
		// Gateway-pre-authenticated: no OAuth ceremony, no code exchange. The
		// login route consumes the configured identity header and hands its
		// value to the validator; sessions/refresh/MFA are unchanged afterwards.
		headerName := identityEnv("IDENTITY_HEADER_NAME")
		if !hasConfiguredValue(headerName) {
			return nil, fmt.Errorf("identity provider header-jwt requires IDENTITY_HEADER_NAME")
		}
		service.SetTokenValidator(v)
		adapters.SetHeaderJWTLoginHeader(headerName)
	} else {
		if authProvider == "workos" {
			// SSO administration is a WorkOS-specific optional adapter. Other
			// identity providers cannot accidentally activate it by exposing a
			// similarly named management credential.
			service.SetSSOManagementAPIKey(identityEnv("IDENTITY_MANAGEMENT_API_KEY"))
		}
		oauthPolicy, err := buildOAuthRequestPolicy(authProvider)
		if err != nil {
			return nil, fmt.Errorf("configure OAuth request policy: %w", err)
		}
		service.SetOAuthRequestPolicy(oauthPolicy)
		service.SetTokenValidator(v)
		service.SetCodeExchanger(ex)
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
	fromAddr := applicationEnv("EMAIL_FROM")
	if fromAddr == "" {
		fromAddr = "no-reply@localhost"
	}
	appBase, err := configuredApplicationBaseURL()
	if err != nil {
		return nil, err
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
	emailSender, err := configuredEmailSender(ctx)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("EMAIL_PROVIDER")), "resend") {
		resendWebhook, err := email.NewResendWebhookHandler(email.ResendWebhookConfig{
			SigningSecret: os.Getenv("RESEND_WEBHOOK_SECRET"),
			Recorder:      jobStore,
		})
		if err != nil {
			return nil, err
		}
		adapters.RegisterHTTPRoute("/v1/email/webhook/resend", resendWebhook)
	}
	emailJobHandler, err := email.NewJobHandler(emailSender)
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

	// Selecting the free plan is a first-party operation and must remain
	// available when Stripe is not configured (the default local/test setup).
	billingHTTPHandler := adapters.NewBillingHTTPHandler(service)
	adapters.RegisterHTTPRoute("/v1/billing/free-plan", billingHTTPHandler)

	// Billing: when Stripe is configured, wire its webhook plus the
	// authenticated checkout and portal endpoints. The sidecar's public-path
	// allowlist covers the webhook; user-facing actions are authenticated via
	// forwarded identity headers.
	var stripeWebhookWorker *jobs.Worker
	var billingWorkerPool interface{ Close() }
	billingProvider := strings.ToLower(strings.TrimSpace(os.Getenv("BILLING_PROVIDER")))
	switch billingProvider {
	case "", "disabled":
		if os.Getenv("STRIPE_API_KEY") != "" || os.Getenv("STRIPE_WEBHOOK_SECRET") != "" {
			return nil, fmt.Errorf("billing: Stripe credentials are present while BILLING_PROVIDER is disabled")
		}
	case "stripe":
		stripeKey := strings.TrimSpace(os.Getenv("STRIPE_API_KEY"))
		whSecret := strings.TrimSpace(os.Getenv("STRIPE_WEBHOOK_SECRET"))
		if stripeKey == "" || whSecret == "" {
			return nil, fmt.Errorf("billing: STRIPE_API_KEY and STRIPE_WEBHOOK_SECRET are required when BILLING_PROVIDER=stripe")
		}
		stripeClient, err := billing.New(billing.Config{
			APIKey:  stripeKey,
			BaseURL: strings.TrimSpace(os.Getenv("STRIPE_API_BASE")),
		})
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

		billingNotifier := &billingNotifier{
			outbox:        workerEmailOutbox,
			notifications: service,
			billingURL:    billingBaseURL + "/admin/billing",
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
		adapters.RegisterHTTPRoute("/v1/billing/checkout", billingHTTPHandler)
		adapters.RegisterHTTPRoute("/v1/billing/portal", billingHTTPHandler)
	default:
		return nil, fmt.Errorf("BILLING_PROVIDER must be disabled or stripe")
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
			APIHost:        os.Getenv("POSTHOG_API_HOST"),
		})
		if err != nil {
			return nil, false, err
		}
		return sink, true, nil
	default:
		return nil, false, fmt.Errorf("PRODUCT_ANALYTICS_MODE must be disabled, noop, or posthog")
	}
}

func configuredAbuseVerifier() (abuse.Verifier, error) {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("ABUSE_PROTECTION_MODE")))
	secret := strings.TrimSpace(os.Getenv("TURNSTILE_SECRET_KEY"))
	switch mode {
	case "", "disabled":
		if secret != "" {
			return nil, fmt.Errorf("abuse: Turnstile secret is present while ABUSE_PROTECTION_MODE is disabled")
		}
		return abuse.DisabledVerifier{}, nil
	case "turnstile":
		allowed := strings.Split(os.Getenv("TURNSTILE_ALLOWED_HOSTNAMES"), ",")
		return abuse.NewTurnstileVerifier(abuse.TurnstileConfig{
			SecretKey:        secret,
			VerifyURL:        strings.TrimSpace(os.Getenv("TURNSTILE_VERIFY_URL")),
			AllowedHostnames: allowed,
		})
	default:
		return nil, fmt.Errorf("ABUSE_PROTECTION_MODE must be disabled or turnstile")
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

func configuredApplicationBaseURL() (string, error) {
	raw := strings.TrimSpace(applicationEnv("APP_BASE_URL"))
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawPath != "" ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", fmt.Errorf("application: APP_BASE_URL must be an exact http(s) origin")
	}
	if parsed.Scheme != "https" && parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" {
		return "", fmt.Errorf("application: APP_BASE_URL must use https outside local development")
	}
	return strings.TrimSuffix(raw, "/"), nil
}

func configuredBillingBaseURL() (string, error) {
	return configuredApplicationBaseURL()
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
	return rpID, displayName, origins, nil
}

// buildProviderStack returns the (TokenValidator, Exchanger) pair
// selected by the Codefly `identity` workspace configuration. Supported values:
//
//	fixture    — DevValidator reading the explicitly selected Codefly fixture.
//	             No exchanger (no OAuth code flow). Used for local iteration.
//	workos     — oidc.Validator + oidc.Exchanger preconfigured for
//	             WorkOS through the same generic identity contract.
//	auth0      — generic OIDC flow with Auth0 defaults.
//	google     — generic OIDC flow with Google defaults.
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

// identityEnv is intentionally Codefly-only. Local dogfood, tests, and
// deployments all select a workspace configuration profile; raw process
// variables must not become a second, divergent identity authority.
func identityEnv(key string) string {
	value, _ := codefly.For(codefly.Context()).WorkspaceValue("identity", key)
	return value
}

// applicationEnv is also Codefly-only. Local product origins and bootstrap
// identity must come from the selected workspace configuration, never from an
// ambient shell file that can disagree with the browser runtime.
func applicationEnv(key string) string {
	value, _ := codefly.For(codefly.Context()).WorkspaceValue("application", key)
	return value
}

func configuredAuthProvider(selectedFixture string) string {
	provider := strings.ToLower(strings.TrimSpace(identityEnv("IDENTITY_PROVIDER")))
	switch provider {
	case "fixture", "dev":
		if selectedFixture == "" {
			return "fixture"
		}
		return "dev"
	default:
		return provider
	}
}

func hasConfiguredValue(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && !strings.EqualFold(value, "REPLACE_ME")
}

func configuredOAuthRedirectURIs() []string {
	raw := identityEnv("IDENTITY_ALLOWED_REDIRECT_URIS")
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

// buildDiscoveredOIDCStack configures an identity provider entirely from
// workspace configuration plus the provider's own OpenID metadata.
//
// The issuer, key set and token endpoint are facts the provider publishes at
// /.well-known/openid-configuration, so they are discovered rather than
// compiled in. Hardcoding them per provider is how this service ended up
// rejecting every WorkOS token with "issuer mismatch" after a successful code
// exchange — the constant was wrong in Go and no configuration could fix it.
//
// Discovery is skipped only when the operator has pinned everything it would
// supply: both IDENTITY_JWKS_URL and IDENTITY_TOKEN_URL, with IDENTITY_ISSUER as
// the expected `iss`. That is the escape hatch for air-gapped environments that
// cannot reach the well-known endpoint — otherwise a partial pin still discovers
// the rest, and any discovery outage fails startup closed rather than guessing.
func buildDiscoveredOIDCStack(provider string) (auth.TokenValidator, business.CodeExchanger, error) {
	clientID := identityEnv("IDENTITY_CLIENT_ID")
	clientSecret := identityEnv("IDENTITY_CLIENT_SECRET")
	issuer := identityEnv("IDENTITY_ISSUER")
	if !hasConfiguredValue(clientID) || !hasConfiguredValue(clientSecret) {
		return nil, nil, fmt.Errorf(
			"identity provider %s requires IDENTITY_CLIENT_ID and IDENTITY_CLIENT_SECRET", provider)
	}
	if !hasConfiguredValue(issuer) {
		return nil, nil, fmt.Errorf(
			"identity provider %s requires IDENTITY_ISSUER to discover provider metadata", provider)
	}

	// Start from any explicitly pinned endpoints; discovery fills only the gaps.
	expectedIssuer := issuer
	jwksURL := identityEnv("IDENTITY_JWKS_URL")
	tokenURL := identityEnv("IDENTITY_TOKEN_URL")
	if !hasConfiguredValue(jwksURL) || !hasConfiguredValue(tokenURL) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		discovered, err := oidc.Discover(ctx, issuer, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("discover %s provider metadata: %w", provider, err)
		}
		expectedIssuer = discovered.Issuer
		if !hasConfiguredValue(jwksURL) {
			jwksURL = discovered.JWKSURI
		}
		if !hasConfiguredValue(tokenURL) {
			tokenURL = discovered.TokenEndpoint
		}
	}

	cfg := oidc.Config{
		ProviderName:  provider,
		Issuer:        expectedIssuer,
		JWKSURL:       jwksURL,
		Audience:      identityEnv("IDENTITY_AUDIENCE"),
		OrgClaim:      identityEnvOrDefault("IDENTITY_ORG_CLAIM", "organization_id"),
		ClientIDClaim: identityEnv("IDENTITY_CLIENT_ID_CLAIM"),
		ClientID:      clientID,
		// Providers that return the verified profile in the token response
		// rather than in the signed token declare it here.
		AllowMissingEmail: identityEnv("IDENTITY_EMAIL_FROM_TOKEN_RESPONSE") == "true",
	}
	if claim := identityEnv("IDENTITY_EMAIL_CLAIM"); claim != "" {
		cfg.EmailClaim = claim
	}

	validator, err := oidc.New(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize %s validator: %w", provider, err)
	}

	if strings.TrimSpace(tokenURL) == "" {
		return nil, nil, fmt.Errorf(
			"identity provider %s published no token endpoint; set IDENTITY_TOKEN_URL", provider)
	}

	exchanger, err := workosauth.NewExchanger(workosauth.Config{
		TokenURL:     tokenURL,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Validator:    validator,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("initialize %s exchanger: %w", provider, err)
	}
	return validator, exchanger, nil
}

func identityEnvOrDefault(key, fallback string) string {
	if value := identityEnv(key); hasConfiguredValue(value) {
		return value
	}
	return fallback
}

func buildProviderStack(provider, selectedFixture string) (auth.TokenValidator, business.CodeExchanger, error) {
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
		return buildDiscoveredOIDCStack(provider)

	case "auth0":
		domain := identityEnv("IDENTITY_DOMAIN")
		audience := identityEnv("IDENTITY_AUDIENCE")
		clientID := identityEnv("IDENTITY_CLIENT_ID")
		clientSecret := identityEnv("IDENTITY_CLIENT_SECRET")
		if !hasConfiguredValue(domain) || !hasConfiguredValue(clientID) || !hasConfiguredValue(clientSecret) {
			return nil, nil, fmt.Errorf("identity provider auth0 requires IDENTITY_DOMAIN, IDENTITY_CLIENT_ID, and IDENTITY_CLIENT_SECRET")
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
		return v, oidc.AsBusinessExchanger(ex), nil

	case "google":
		clientID := identityEnv("IDENTITY_CLIENT_ID")
		clientSecret := identityEnv("IDENTITY_CLIENT_SECRET")
		if !hasConfiguredValue(clientID) || !hasConfiguredValue(clientSecret) {
			return nil, nil, fmt.Errorf("identity provider google requires IDENTITY_CLIENT_ID and IDENTITY_CLIENT_SECRET")
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
		return v, oidc.AsBusinessExchanger(ex), nil

	case "header-jwt":
		v, err := buildHeaderJWTValidator()
		if err != nil {
			return nil, nil, err
		}
		return v, nil, nil

	case "fixture":
		return nil, nil, fmt.Errorf("identity provider fixture requires an explicit Codefly fixture")
	case "":
		return nil, nil, fmt.Errorf("IDENTITY_PROVIDER is required in the Codefly identity workspace configuration")
	default:
		return nil, nil, fmt.Errorf("unsupported identity provider %q", provider)
	}
}

// buildHeaderJWTValidator configures the gateway-pre-authenticated validator
// from the Codefly identity configuration. Audience is mandatory; JWKS is
// mandatory unless the operator has deliberately opted into perimeter-trust
// decode via IDENTITY_PERIMETER_TRUST_DECODE.
func buildHeaderJWTValidator() (auth.TokenValidator, error) {
	v, err := headerjwt.New(headerjwt.Config{
		ProviderName:         identityEnvOrDefault("IDENTITY_PROVIDER_NAME", "header-jwt"),
		JWKSURL:              identityEnv("IDENTITY_JWKS_URL"),
		Audience:             identityEnv("IDENTITY_AUDIENCE"),
		Issuer:               identityEnv("IDENTITY_ISSUER"),
		SubjectClaim:         identityEnv("IDENTITY_SUBJECT_CLAIM"),
		EmailClaim:           identityEnv("IDENTITY_EMAIL_CLAIM"),
		EmailVerifiedClaim:   identityEnv("IDENTITY_EMAIL_VERIFIED_CLAIM"),
		NameClaims:           identityEnvList("IDENTITY_NAME_CLAIMS"),
		GroupClaim:           identityEnv("IDENTITY_GROUP_CLAIM"),
		AllowedGroups:        identityEnvList("IDENTITY_ALLOWED_GROUPS"),
		PerimeterTrustDecode: identityEnv("IDENTITY_PERIMETER_TRUST_DECODE") == "true",
	})
	if err != nil {
		return nil, fmt.Errorf("initialize header-jwt validator: %w", err)
	}
	return v, nil
}

// identityEnvList reads a comma-separated identity configuration value into a
// trimmed, non-empty slice.
func identityEnvList(key string) []string {
	raw := identityEnv(key)
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// configuredEmailSender makes provider selection explicit. Production never
// silently downgrades to a log sink because one Resend value is missing.
func configuredEmailSender(ctx context.Context) (email.Sender, error) {
	w := wool.Get(ctx).In("pickEmailSender")
	logFn := func(format string, args ...any) {
		w.Info(fmt.Sprintf(format, args...))
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("EMAIL_PROVIDER"))) {
	case "", "log":
		if os.Getenv("RESEND_API_KEY") != "" || os.Getenv("RESEND_WEBHOOK_SECRET") != "" {
			return nil, fmt.Errorf("email: Resend credentials are present while EMAIL_PROVIDER is log")
		}
		return email.NewLogSender(logFn), nil
	case "resend":
		key := strings.TrimSpace(os.Getenv("RESEND_API_KEY"))
		webhookSecret := strings.TrimSpace(os.Getenv("RESEND_WEBHOOK_SECRET"))
		if key == "" || webhookSecret == "" {
			return nil, fmt.Errorf("email: RESEND_API_KEY and RESEND_WEBHOOK_SECRET are required when EMAIL_PROVIDER=resend")
		}
		s, err := email.NewResendSender(email.ResendConfig{
			APIKey:  key,
			BaseURL: strings.TrimSpace(os.Getenv("RESEND_API_BASE")),
		})
		if err != nil {
			return nil, err
		}
		return s, nil
	default:
		return nil, fmt.Errorf("EMAIL_PROVIDER must be log or resend")
	}
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

// billingNotifier converts a completed billing projection into channel-specific
// delivery commands.
type billingNotifier struct {
	outbox        *email.Outbox
	notifications *business.Service
	billingURL    string
}

func (n *billingNotifier) EnqueueBillingEmail(ctx context.Context, message billing.BillingEmail) error {
	if n == nil || n.outbox == nil {
		return fmt.Errorf("billing: email notifier is not configured")
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

func (n *billingNotifier) CreateBillingNotification(
	ctx context.Context,
	message billing.BillingNotification,
) error {
	if n == nil || n.notifications == nil {
		return fmt.Errorf("billing: in-app notifier is not configured")
	}
	_, err := n.notifications.CreateNotification(ctx, business.CreateNotificationInput{
		UserID:         message.RecipientID,
		OrgID:          message.OrganizationID,
		Title:          message.Title,
		Body:           message.Body,
		Type:           "billing",
		ActionURL:      message.ActionURL,
		Category:       business.NotificationCategoryBilling,
		IdempotencyKey: message.DeliveryKey,
	})
	return err
}
