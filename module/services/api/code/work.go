package main

import (
	"api/fixtures"
	"api/pkg/adapters"
	"api/pkg/auth"
	devvalidator "api/pkg/auth/dev"
	ed25519minter "api/pkg/auth/ed25519"
	"api/pkg/auth/oidc"
	pgauth "api/pkg/auth/pg"
	"api/pkg/billing"
	pgbilling "api/pkg/billing/pg"
	"api/pkg/business"
	"api/pkg/email"
	"api/pkg/infra"
	"context"
	ed25519core "crypto/ed25519"
	"fmt"
	"log"
	"os"
	"time"

	codefly "github.com/codefly-dev/sdk-go"
)

func init() {
	WithWork(doWork)
}

func doWork(ctx context.Context) (Clean, error) {
	store, err := infra.NewPostgresStore(ctx)
	if err != nil {
		return nil, err
	}

	service, err := business.NewService(store)
	if err != nil {
		return nil, err
	}

	// Vault-backed API key hasher (optional — only in environments with Vault).
	vaultClient, err := infra.NewVaultClient(ctx)
	if err == nil {
		service.SetHasher(vaultClient)
	}

	// Auth pipeline: IdentityResolver + JWTMinter + optional provider
	// validator/exchanger chain for the OAuth code flow.
	sessionStore := pgauth.NewSessionStore(store.Pool())
	resolver := pgauth.NewResolver(store.Pool())
	priv, err := loadSigningKey(ctx)
	if err != nil {
		return nil, err
	}
	minter := ed25519minter.New(ed25519minter.Config{
		Issuer:   "saas-starter",
		Audience: "saas-starter",
	}, priv, sessionStore)

	service.SetIdentityResolver(resolver)
	service.SetJWTMinter(minter)

	// Provider validator + exchanger based on AUTH_PROVIDER. For
	// AUTH_PROVIDER=workos|auth0|google we build an oidc.Validator with
	// the matching preset plus a matching Exchanger. When unset we stay
	// on the dev / pre-validated path.
	if v, ex := buildProviderStack(); v != nil {
		service.SetTokenValidator(v)
		if ex != nil {
			service.SetCodeExchanger(oidc.AsBusinessExchanger(ex))
		}
	}

	auditEmitter := business.NewAsyncAuditEmitter(store, 1024)
	service.SetAuditEmitter(auditEmitter)

	entitlementChecker := business.NewDefaultEntitlementChecker(store)
	service.SetEntitlementChecker(entitlementChecker)

	featureChecker := business.NewDefaultFeatureChecker(store, entitlementChecker)
	service.SetFeatureChecker(featureChecker)

	adapters.WithService(service)

	// Billing: wire Stripe webhook handler at /v1/billing/webhook AND
	// the authenticated /v1/billing/checkout + /v1/billing/portal
	// endpoints. The sidecar's public-path allowlist covers the
	// webhook; checkout + portal are authenticated via forwarded
	// identity headers.
	if stripeKey := os.Getenv("STRIPE_API_KEY"); stripeKey != "" {
		whSecret := os.Getenv("STRIPE_WEBHOOK_SECRET")
		if whSecret == "" {
			fmt.Fprintln(os.Stderr, "WARNING: STRIPE_API_KEY set but STRIPE_WEBHOOK_SECRET missing — webhooks will be rejected")
		}
		stripeClient, err := billing.New(billing.Config{APIKey: stripeKey})
		if err != nil {
			return nil, fmt.Errorf("billing: %w", err)
		}
		billingStore := pgbilling.New(store.Pool())

		// Build the billing email notifier if templates are available.
		var billingNotifier billing.EmailNotifier
		if service.TemplateSender() != nil {
			billingNotifier = &billingEmailNotifier{ts: service.TemplateSender()}
		}

		adapters.RegisterHTTPRoute("/v1/billing/webhook", billing.NewHandler(billing.HandlerDeps{
			Store:         billingStore,
			Client:        stripeClient,
			WebhookSecret: whSecret,
			Notifier:      billingNotifier,
		}))
		service.SetBillingClient(stripeClient)
		adapters.RegisterHTTPRoute("/v1/billing/checkout", adapters.NewBillingHTTPHandler(service))
		adapters.RegisterHTTPRoute("/v1/billing/portal", adapters.NewBillingHTTPHandler(service))
	}

	// Email sender selection: Resend if RESEND_API_KEY is set, log-only
	// otherwise (so dev still sees what would be sent without setup).
	sender := pickEmailSender()
	if sender != nil {
		fromAddr := os.Getenv("EMAIL_FROM")
		if fromAddr == "" {
			fromAddr = "no-reply@localhost"
		}
		appBase := os.Getenv("APP_BASE_URL")
		if appBase == "" {
			appBase = "http://localhost:21931"
		}
		service.SetEmailSender(sender, fromAddr, appBase)

		// Template-backed email: wraps the raw Sender with the DB-backed
		// template store so business logic can use SendTemplate for the
		// seeded templates (welcome, invitation, payment_failed,
		// invoice_ready, trial_ending, gdpr_export_ready).
		templateStore := infra.NewPostgresTemplateStore(store)
		templateSender := email.NewTemplateSender(sender, templateStore)
		service.SetTemplateSender(templateSender)
	}

	// Start background data retention goroutine. Runs once on startup and
	// then every 24 hours, deleting records older than their configured
	// retention period.
	retentionCtx, retentionCancel := context.WithCancel(ctx)
	go func() {
		// Run once immediately on startup.
		if deleted, err := service.RunRetention(retentionCtx); err != nil {
			log.Printf("retention startup run: %v", err)
		} else {
			for k, v := range deleted {
				if v > 0 {
					log.Printf("retention: deleted %d %s records", v, k)
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
					log.Printf("retention run: %v", err)
				} else {
					for k, v := range deleted {
						if v > 0 {
							log.Printf("retention: deleted %d %s records", v, k)
						}
					}
				}
			}
		}
	}()

	if codefly.WithFixture("simple") {
		err = fixtures.Simple(ctx, service)
		if err != nil {
			retentionCancel()
			return nil, err
		}
	}

	if codefly.WithFixture("dev-admin") {
		err = fixtures.DevAdmin(ctx, service)
		if err != nil {
			retentionCancel()
			return nil, err
		}
	}

	return func() {
		log.Println("api shutdown: stopping retention goroutine...")
		retentionCancel()
		log.Println("api shutdown: closing audit emitter...")
		auditEmitter.Close()
		log.Println("api shutdown: audit emitter closed")
		log.Println("api shutdown: closing store...")
		store.Close()
		log.Println("api shutdown: store closed")
	}, nil
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
//	(empty)    — same as dev, for backwards compat with fixture tests.
//
// Returns (nil, nil) if the provider is unknown or not configured.
// workosEnv reads a key from the codefly workspace configuration for "workos",
// falling back to plain env var for backwards compatibility.
func workosEnv(key string) string {
	// Codefly injects workspace configs as CODEFLY__WORKSPACE_CONFIGURATION__WORKOS__KEY
	if v := os.Getenv("CODEFLY__WORKSPACE_CONFIGURATION__WORKOS__" + key); v != "" {
		return v
	}
	// Secret variant
	if v := os.Getenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__WORKOS__" + key); v != "" {
		return v
	}
	// Fallback to plain env var
	return os.Getenv(key)
}

func buildProviderStack() (auth.TokenValidator, *oidc.Exchanger) {
	switch workosEnv("AUTH_PROVIDER") {
	case "dev", "":
		v, err := devvalidator.New("")
		if err != nil {
			return nil, nil
		}
		return v, nil

	case "workos":
		clientID := workosEnv("WORKOS_CLIENT_ID")
		clientSecret := workosEnv("WORKOS_CLIENT_SECRET")
		if clientID == "" || clientSecret == "" {
			fmt.Fprintln(os.Stderr, "WARNING: AUTH_PROVIDER=workos but WORKOS_CLIENT_ID / WORKOS_CLIENT_SECRET missing")
			return nil, nil
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
			fmt.Fprintf(os.Stderr, "WARNING: workos validator: %v\n", err)
			return nil, nil
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
			fmt.Fprintf(os.Stderr, "WARNING: workos exchanger: %v\n", err)
			return v, nil
		}
		return v, ex

	case "auth0":
		domain := os.Getenv("AUTH0_DOMAIN")
		audience := os.Getenv("AUTH0_AUDIENCE")
		clientID := os.Getenv("AUTH0_CLIENT_ID")
		clientSecret := os.Getenv("AUTH0_CLIENT_SECRET")
		if domain == "" || clientID == "" || clientSecret == "" {
			fmt.Fprintln(os.Stderr, "WARNING: AUTH_PROVIDER=auth0 but AUTH0_DOMAIN / AUTH0_CLIENT_ID / AUTH0_CLIENT_SECRET missing")
			return nil, nil
		}
		v, err := oidc.New(oidc.Auth0Config(domain, audience))
		if err != nil {
			fmt.Fprintf(os.Stderr, "WARNING: auth0 validator: %v\n", err)
			return nil, nil
		}
		ex, err := oidc.NewExchanger(oidc.ExchangerConfig{
			TokenURL:     fmt.Sprintf("https://%s/oauth/token", domain),
			ClientID:     clientID,
			ClientSecret: clientSecret,
		})
		if err != nil {
			return v, nil
		}
		return v, ex

	case "google":
		clientID := os.Getenv("GOOGLE_CLIENT_ID")
		clientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
		if clientID == "" || clientSecret == "" {
			fmt.Fprintln(os.Stderr, "WARNING: AUTH_PROVIDER=google but GOOGLE_CLIENT_ID / GOOGLE_CLIENT_SECRET missing")
			return nil, nil
		}
		v, err := oidc.New(oidc.GoogleConfig(clientID))
		if err != nil {
			return nil, nil
		}
		ex, err := oidc.NewExchanger(oidc.ExchangerConfig{
			TokenURL:     "https://oauth2.googleapis.com/token",
			ClientID:     clientID,
			ClientSecret: clientSecret,
		})
		if err != nil {
			return v, nil
		}
		return v, ex

	default:
		fmt.Fprintf(os.Stderr, "WARNING: unknown AUTH_PROVIDER=%q\n", os.Getenv("AUTH_PROVIDER"))
		return nil, nil
	}
}

// pickEmailSender chooses a Sender based on environment.
//
//	RESEND_API_KEY set → ResendSender (production)
//	otherwise          → LogSender writing to stderr (dev)
func pickEmailSender() email.Sender {
	if key := os.Getenv("RESEND_API_KEY"); key != "" {
		s, err := email.NewResendSender(email.ResendConfig{APIKey: key})
		if err != nil {
			fmt.Fprintf(os.Stderr, "WARNING: resend init failed: %v — falling back to log sender\n", err)
			return email.NewLogSender(func(format string, args ...any) {
				fmt.Fprintf(os.Stderr, format, args...)
			})
		}
		return s
	}
	return email.NewLogSender(func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format, args...)
	})
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
		fmt.Fprintf(os.Stderr, "WARNING: could not load signing key from Vault (%v) — falling back to ephemeral\n", err)
	}
	_, priv, err := ed25519minter.GenerateKey()
	return priv, err
}

// billingEmailNotifier adapts the email.TemplateSender to the
// billing.EmailNotifier interface so webhook handlers can send
// templated emails without depending on the business package.
type billingEmailNotifier struct {
	ts *email.TemplateSender
}

func (n *billingEmailNotifier) SendBillingEmail(ctx context.Context, templateName, toEmail string, vars map[string]string) error {
	_, err := n.ts.SendTemplate(ctx, templateName, toEmail, vars)
	return err
}
