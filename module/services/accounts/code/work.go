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
	"accounts/pkg/datasource"
	"accounts/pkg/email"
	"accounts/pkg/eventcatalog"
	"accounts/pkg/githubconnector"
	"accounts/pkg/infra"
	"accounts/pkg/jobs"
	"accounts/pkg/keyservice"
	"accounts/pkg/membership"
	"accounts/pkg/metrics"
	"accounts/pkg/permissionsplugin"
	"accounts/pkg/redisstate"
	"context"
	ed25519core "crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/codefly-dev/core/wool"
	wooltel "github.com/codefly-dev/core/wool/otel"
	codefly "github.com/codefly-dev/sdk-go"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	otelmetric "go.opentelemetry.io/otel/metric"
)

func init() {
	WithWork(doWork)
}

func doWork(ctx context.Context) (Clean, error) {
	// Configuration this process cannot run without is checked before any
	// dependency is touched. It used to be checked deep in the startup, after
	// the database and Vault were already up, which meant a missing value cost
	// a full bring-up before anyone was told — and made the check unreachable
	// from a test without standing both of those up, so deleting the call
	// site was invisible.
	if err := requireStartupConfiguration(ctx, codefly.IsLocal()); err != nil {
		return nil, err
	}
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

	// When external observability is configured, OpenTelemetry always targets
	// the in-graph collector. Codefly owns its host and port; the accounts
	// process never reads or hardcodes a collector address.
	var otelProvider *wooltel.Provider
	var otelMetricProvider *otelMetrics
	if observabilityEnabled() {
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
		metricProvider, oerr := enableOTELMetrics(ctx, "saas-starter-api", collectorNetwork.Host)
		if oerr != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = otelProvider.Shutdown(shutdownCtx)
			cancel()
			return nil, fmt.Errorf("configure OTEL metrics: %w", oerr)
		}
		otelMetricProvider = metricProvider
		// codefly:gateway-route-exempt the OTEL scrape endpoint; the collector reaches it over the mesh and it must never be public
		adapters.RegisterHTTPRoute("/metrics", otelMetricProvider.Handler())
	}

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
	_, abuseDisabled := abuseVerifier.(abuse.DisabledVerifier)
	if abuseDisabled {
		w.Warn("ABUSE PROTECTION DISABLED — anonymous endpoints (Authenticate, RegisterUser, JoinWaitlist, SendMagicLink) are guarded by rate limiting alone; set ABUSE_PROTECTION_MODE=turnstile before serving production traffic")
	}

	// Per-IP rate limiting attributes anonymous traffic to a source address.
	// TRUSTED_PROXY_CIDRS lists the proxies whose X-Forwarded-For we honor;
	// an invalid entry fails boot rather than silently trusting a spoofable
	// header.
	trustedProxies, err := adapters.ParseTrustedProxyCIDRs(workspaceEnv("security", "TRUSTED_PROXY_CIDRS"))
	if err != nil {
		return nil, err
	}
	adapters.WithTrustedProxies(trustedProxies)
	if len(trustedProxies) == 0 {
		w.Warn("TRUSTED_PROXY_CIDRS unset — behind an ingress, per-IP rate limiting attributes all X-Forwarded-For traffic to the proxy's own IP (one shared bucket); set it to the ingress CIDRs to bucket by real client IP")
	}
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
	// Durable job-operations metrics are enabled only when OTEL metrics are, i.e.
	// otelMetricProvider != nil (observabilityEnabled()). With observability off
	// the global meter is a no-op, so building the monitor would poll the job
	// store every interval to feed instruments nothing can read; keep it nil and
	// let the Start/Shutdown nil-checks below skip it entirely.
	jobOperationsMonitor, err := newDurableJobMetricsMonitor(otelMetricProvider != nil, jobStore)
	if err != nil {
		return nil, fmt.Errorf("configure durable job metrics: %w", err)
	}
	service.SetJobOperations(jobStore)
	// Domain-event administration (#494 P3) reads the same isolated worker pool:
	// per-type counters and relay lag from domain_events, dead-letters from the
	// inbox, and live subscriptions across every principal — payload-free, and on
	// app_job_worker so request traffic never gains that cross-tenant reach.
	service.SetEventOperations(infra.NewPostgresEventOperations(jobWorkerPool))
	service.SetWebhookJobProducer(store)

	// Module-facing capability surface (issue #463): a request-scoped
	// transactional producer for tenant/subject enqueue, the privileged worker
	// store for claim/lease/finalize and global inbox enqueue, and the registry
	// declaring which queues each module service principal may use. The registry
	// is deployment-provided; an unset value denies every caller (fail-closed).
	modulePrincipals, err := business.ParseModulePrincipalRegistry(workspaceEnv("module-capabilities", "MODULE_PRINCIPALS"))
	if err != nil {
		return nil, fmt.Errorf("parse module principal registry: %w", err)
	}
	service.SetModuleCapabilities(store, jobStore, modulePrincipals)
	// Explicit organization delegation is separate from module identity. A
	// projected policy is refreshed by the delivery system and read per call.
	installerHandler, err := configuredModuleInstaller(service)
	if err != nil {
		return nil, fmt.Errorf("configure module installer policy: %w", err)
	}
	if installerHandler != nil {
		// codefly:gateway-route-exempt the module-facing capability surface; a caller presents a module Work Context and reaches it over the mesh, never the public edge
		adapters.RegisterHTTPRoute("/v1/module-installations/", installerHandler)
	}

	// Domain-event pub/sub (issue #493): the reference events.Transport over the
	// durable jobs platform. A module publish joins its WithOrgTx transaction so
	// the insert is the transactional outbox and the tenant gate re-checks under
	// app_tenant; the relay worker then drains domain_events after commit, fanning
	// each event out to matching subscriptions on the app_job_worker pool
	// (BYPASSRLS, so it resolves events and subscriptions across every tenant).
	// The relay is also the outbound-webhook fan-out: a webhook endpoint is an
	// event_subscriptions row with delivery = webhook (#488), and this is the
	// dispatcher it is delivered through.
	eventTransport := infra.NewPostgresEventTransport(
		jobStore, jobWorkerPool, "events-relay-"+uuid.NewString(), time.Minute,
		infra.WithWebhookRelay(infra.NewPostgresWebhookRelay(store)),
	)
	service.SetModuleEventTransport(eventTransport)
	eventRelayWorker := infra.NewEventRelayWorker(eventTransport, 0)

	// The follow bridge (FOLLOWS.md) is host-internal, which is what keeps it
	// inside the boundary: the composed catalog declares which resources are
	// followable, the host subscribes itself over exactly those types on its own
	// reserved queue, and the worker resolves access through the store directly
	// rather than through a module-facing RPC. Declaring nothing leaves every
	// piece of this inert.
	declared := eventcatalog.Followable()
	followables := make([]business.FollowableResource, 0, len(declared))
	for _, followable := range declared {
		followables = append(followables, business.FollowableResource{
			ResourceType: followable.ResourceType,
			Events:       followable.Events,
		})
	}
	service.SetFollowables(followables)
	// Materialization is idempotent, so a restart converges on the rows that
	// already exist. It deliberately does not fail startup: VerifyEventWiring
	// short-circuits on a wired transport and never reaches the database, so this
	// is the only write at boot, and returning its error would turn a momentary
	// database outage into a crash-loop for the whole service. Leaving the host
	// unsubscribed would silently drop every followable event, so the failure is
	// loud and retried in the background rather than tolerated.
	if err := service.MaterializeFollowSubscriptions(ctx); err != nil {
		wool.Get(ctx).In("follows").Error(
			"cannot materialize follow subscriptions; retrying in the background",
			wool.ErrField(err))
		go retryFollowSubscriptions(ctx, service)
	}
	followFanoutWorker, err := jobs.NewWorker(jobs.WorkerConfig{
		Store:      jobStore,
		Queue:      business.FollowFanoutQueue,
		Handler:    service.FollowFanoutHandler(),
		RetryDelay: business.FollowFanoutRetryDelay,
	})
	if err != nil {
		return nil, err
	}

	// Fail fast if a deployment ever ends up with live subscriptions but no
	// transport: without one, every publish is a silent no-op and subscribers
	// receive nothing. The transport is wired unconditionally just above, so this
	// is a regression guard — but it converts a future mis-wiring from invisible
	// event loss into a startup error.
	if err := service.VerifyEventWiring(ctx); err != nil {
		return nil, err
	}
	// The symmetric guard for the other half of fan-out: a transport with no
	// outbound dispatcher relays to module queues and silently never to webhook
	// endpoints, which is the same invisible loss one layer over.
	if err := eventTransport.RequireWebhookRelay(); err != nil {
		return nil, err
	}

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

	// The key service is a required security dependency: the API-key keyed hash
	// and every sealed credential must be stable across replicas and fail
	// closed. Which service holds the keys — Vault in-cluster, the cell's cloud
	// key service on a hosted deployment — is selected in the `key-service`
	// configuration group and refused here when it cannot work.
	keys, err := keyservice.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("configure key service: %w", err)
	}
	cipher := keys.Cipher
	service.SetHasher(cipher)
	service.SetMFASecretCipher(cipher)
	service.SetOrgIdentityProviderCipher(cipher)
	service.SetConnectorCipher(cipher)
	service.SetGitHubConnector(githubconnector.NewConnector(
		githubconnector.WithBaseURL(os.Getenv("GITHUB_API_BASE_URL"))))
	// Datasource connector (issue #274): per-source credentials are sealed by the
	// key service, and pulled files are enqueued onto the durable inbox seam the
	// documents module consumes. GITHUB_API_BASE_URL overrides api.github.com for
	// GitHub Enterprise or tests.
	service.SetDatasourceConnector(cipher, jobStore, os.Getenv("GITHUB_API_BASE_URL"))
	// Every provider credential's operations are metered in one window shared by
	// every replica, so a sync a person starts is served before background work.
	service.SetDatasourceBudgetStore(store)
	// The App registration is deployment custody: the signing key is read here
	// and never copied onto a source record. Unset leaves sources on their own
	// stored fine-grained PAT.
	service.SetGitHubAppRegistration(
		workspaceEnv("github-app", "GITHUB_APP_ID"),
		workspaceEnv("github-app", "GITHUB_APP_PRIVATE_KEY"),
		workspaceEnv("github-app", "GITHUB_APP_SLUG"),
		workspaceEnv("github-app", "GITHUB_APP_WEBHOOK_SECRET"),
	)
	// The OAuth client is what attributes an installation to the person who
	// installed it. Without it the host could only prove an installation exists,
	// which is true of every tenant's installation, so onboarding stays off.
	service.SetGitHubAppOAuth(
		workspaceEnv("github-app", "GITHUB_APP_CLIENT_ID"),
		workspaceEnv("github-app", "GITHUB_APP_CLIENT_SECRET"),
	)
	webhookPolicy := business.NewWebhookEndpointPolicy()
	service.SetWebhookSecurity(cipher, webhookPolicy)
	webAuthnRPID, webAuthnDisplayName, webAuthnOrigins, err := configuredWebAuthn()
	if err != nil {
		return nil, fmt.Errorf("configure WebAuthn: %w", err)
	}
	webAuthnEngine, err := infra.NewWebAuthnEngine(webAuthnRPID, webAuthnDisplayName, webAuthnOrigins)
	if err != nil {
		return nil, err
	}
	service.SetWebAuthnEngine(webAuthnEngine)
	if migrated, err := store.MigrateLegacyMFASecrets(ctx, cipher); err != nil {
		return nil, fmt.Errorf("migrate legacy MFA secrets: %w", err)
	} else if migrated > 0 {
		w.Info("encrypted legacy MFA secrets", wool.Field("count", migrated))
	}
	if migrated, disabled, err := store.MigrateLegacyWebhookSecrets(ctx, cipher); err != nil {
		return nil, fmt.Errorf("migrate legacy webhook secrets: %w", err)
	} else if migrated > 0 {
		w.Info("encrypted legacy webhook secrets",
			wool.Field("count", migrated),
			wool.Field("disabled_empty_secret_endpoints", disabled))
	}
	// A key-service cutover: re-seal every enveloped column under the selected
	// backend, and report what still references the one being migrated away
	// from. This runs only while a previous backend is configured, which is
	// exactly the cutover window — a settled deployment does no work here.
	//
	// Reads already work from the moment both backends are bound, because every
	// envelope names the backend that sealed it. This exists for the other half:
	// withdrawing the outgoing backend is safe only once nothing references it,
	// and the remaining count is what an operator reads to know that. The sweep
	// is restart-safe and idempotent, so an interrupted run resumes and two
	// replicas cannot fight over a row.
	if previous := cipher.PreviousTag(); previous != "" {
		outcomes, err := store.ResealEnvelopes(ctx, cipher)
		resealed, remaining, detail := infra.ResealReport(outcomes)
		if err != nil {
			return nil, fmt.Errorf("re-seal stored credentials under the selected key service: %w", err)
		}
		w.Info("re-sealed stored credentials under the selected key service",
			wool.Field("resealed", resealed),
			wool.Field("previous_backend", previous),
			wool.Field("still_referencing_another_backend", len(remaining) > 0))
		if len(remaining) > 0 {
			// Named, not merely counted: an operator who withdraws the previous
			// backend now makes these unreadable, and "which rows" is the
			// difference between a retry and an incident.
			w.Warn("stored credentials still reference a key service other than the selected one — do not withdraw it yet",
				wool.Field("columns", strings.Join(detail, "; ")))
		}
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
	authProvider := configuredAuthProvider(selectedFixture)
	if err := requireLocalForDevFixtureProvider(authProvider, codefly.IsLocal()); err != nil {
		return nil, err
	}
	priv, err := loadSigningKey(ctx, keys, devFixtureAuthProvider(authProvider), codefly.IsLocal())
	if err != nil {
		return nil, err
	}
	// Default fail-closed: a token whose revocation status can't be read is
	// denied. Operators fronting accounts directly (no auth-gateway) can opt into
	// fail-open to keep the direct verify path serving through a revocation-store
	// outage, trading a revoked token's remaining-TTL exposure for availability —
	// the same choice the auth-gateway ext_authz check exposes via SIDECAR_REVOCATION_FAIL_OPEN.
	revocationFailOpen := strings.EqualFold(strings.TrimSpace(workspaceEnv("security", "ACCOUNTS_REVOCATION_FAIL_OPEN")), "true")
	if revocationFailOpen {
		wool.Get(ctx).Warn("ACCOUNTS_REVOCATION_FAIL_OPEN enabled: a revocation-store outage will admit possibly-revoked access tokens on the direct verify path until they expire")
	}
	// One issuer, resolved once, for the published RFC 8414 metadata AND for the
	// `iss` of every token minted — the only way the two can be proved equal. A
	// deployment with no configured base URL keeps the pre-metadata literal and
	// publishes no metadata, so the two cannot disagree there either.
	tokenIssuer, err := configuredTokenIssuer()
	if err != nil {
		return nil, err
	}
	var migratingIssuers []string
	if tokenIssuer != business.LegacyTokenIssuer {
		// Accepted, not minted: every unexpired token and every session about to
		// rotate into one still carries the literal, so refusing it at the moment
		// the URL starts being minted would sign out every live session.
		migratingIssuers = []string{business.LegacyTokenIssuer}
		wool.Get(ctx).Info("access tokens now name the host's own URL as issuer; the previous literal stays accepted until outstanding tokens expire",
			wool.Field("issuer", tokenIssuer))
	}
	minter := ed25519minter.New(ed25519minter.Config{
		Issuer:                     tokenIssuer,
		Audience:                   "saas-starter",
		AdditionalAcceptedIssuers:  migratingIssuers,
		SessionPolicy:              sessionPolicy,
		AdditionalVerificationKeys: previousSigningKeys(ctx),
		RevocationFailOpen:         revocationFailOpen,
	}, priv, sessionStore)
	service.SetOAuthIssuer(oauthIssuerOf(tokenIssuer))

	service.SetIdentityResolver(resolver)
	service.SetJWTMinter(minter)
	// The standards-named HTTP endpoint must return a top-level JSON Web Key
	// Set. grpc-gateway otherwise wraps the document in JWKSResponse.keys_json,
	// which generic verifiers correctly reject.
	adapters.RegisterHTTPRoute(
		"/v1/auth/.well-known/jwks.json",
		adapters.NewJWKSHTTPHandler(service),
	)
	// TODO(#952, Unit D): remove the registration-secret configuration together
	// with the mint handlers in pkg/adapters/module_capabilities_server.go, which
	// is protected from edits in this unit. Gateway token exchanges are deleted.
	// The remaining mint RPC still consumes this declaration. Its credential has
	// no gateway registration endpoint to admit it after the cold cutover.
	moduleRegistrationSecrets, err := business.ParseRegistrationSecrets(
		workspaceEnv("federation", "MODULE_REGISTRATION_SECRETS"))
	if err != nil {
		return nil, fmt.Errorf("read module registration secrets: %w", err)
	}
	service.SetModuleRegistrar(minter, moduleRegistrationSecrets)
	if err := configureModuleIdentity(service); err != nil {
		return nil, err
	}

	// Keep the remaining solution mint's reader until its protected handler is
	// removed in the same change. Validate configuration before serving it.
	solutionRegistrationSecrets := func() string {
		return workspaceEnv("federation", "SOLUTION_REGISTRATION_SECRETS")
	}
	if _, err := business.ParseRegistrationSecrets(solutionRegistrationSecrets()); err != nil {
		return nil, fmt.Errorf("read solution registration secrets: %w", err)
	}
	service.SetSolutionRegistrar(minter, solutionRegistrationSecrets)

	// Declared solution presence (issue #952). Delivery POSTs one signed
	// SolutionHostBinding carrier per solution instance to the delivery
	// endpoint; this host verifies it on receipt and persists it, and the
	// reconciler reads its desired set from that durable inbox on every pass,
	// asks Core whether each document may be applied, and reconciles what it
	// admits into the durable declaration registry.
	//
	// Unset SOLUTION_HOST_COORDINATE leaves the whole surface off — no
	// reconciler and no delivery endpoint — and nothing on this host is
	// declared. A coordinate lets this host refuse a document delivered to the wrong
	// place, and core's check is the only thing standing between this host and
	// another host's desired state.
	solutionHostBindingReconciler, err := configuredSolutionHostBindingReconciler(service, store)
	if err != nil {
		return nil, fmt.Errorf("configure solution host binding reconciler: %w", err)
	}

	// The policy log: append → receipt → commit, before authority is narrowed.
	//
	// THIS DEPLOYMENT HAS NO LOG TRANSPORT, and that is stated by wiring none
	// rather than by wiring something that refuses. The difference matters now
	// that the serving gate is on the request path: a host holding a log it can
	// never reach never refreshes `reached_at`, so the staleness window closes
	// and the host stops answering anything — a total outage dressed as
	// fail-closed. A host holding NO log has nothing unreconciled to honour, so
	// `MayServe` admits and the host serves.
	//
	// What it cannot do is NARROW authority. `WithPolicyLoggedNarrowing` refuses
	// outright, so uninstalling a solution, closing a withdrawn solution
	// target, revoking a scope grant and removing a team membership all refuse
	// with ErrPolicyLogUnreachable until a transport exists. That is the
	// protocol's own answer — a narrowing nothing witnessed is one a restore
	// silently undoes — and it is loud rather than silent, below.
	//
	// WHY THERE IS NO TRANSPORT. The warehouse the log lives in cannot issue the
	// receipt the protocol requires. `PolicyLog.Append` must return a token the
	// LOG minted and a monotonic sequence the LOG assigned, because the receipt
	// is what separates "the log witnessed this" from "this host decided it". A
	// BigQuery dataset assigns neither to a writer: the non-job write paths
	// return no ordinal, ingestion time is not readable until the streaming
	// buffer flushes, and the writer role deliberately holds no read and no
	// job-creation permission, so it cannot read its own row back to learn
	// either. Closing that needs a receipt-issuing appender inside the
	// warehouse's trust domain, which is infrastructure this service does not
	// own. Anything else would be this host minting its own receipt — the exact
	// tautology the protocol exists to prevent.
	service.SetPolicyLog(nil, nil)
	w.Warn("no policy log transport is configured: this host SERVES normally and REFUSES every " +
		"narrowing of authority (solution uninstall, solution-target close, scope-grant revocation, " +
		"team-membership removal), because a narrowing nothing witnessed is one a restore undoes silently")

	// The serving gate, on the request path. Registered for both transports'
	// authorization interceptors; with no log wired it admits, which is what
	// MayServe answers for a host that has narrowed nothing through the
	// protocol.
	adapters.RegisterPolicyLogServingGate(service.RequireServing)

	// Enforcement at use. Every module capability path re-reads the live
	// installation, producer epoch and binding revision through these, and
	// AuthorizeModuleCapability refuses outright when they are absent — a host
	// that cannot re-read cannot honour a narrowing, so it must not fall back to
	// the declared ceiling alone.
	service.SetModuleAuthorityReads(store, nil)

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
	stateSigner, err := auth.NewOAuthStateSigner(priv)
	if err != nil {
		return nil, fmt.Errorf("configure oauth state signer: %w", err)
	}
	service.SetOAuthStateSigner(stateSigner)

	// Registered first-party clients (issue #853). Wired regardless of the
	// configured identity provider: a client signs in on the host's login page
	// whatever authenticates the person behind it, so the registry is not part
	// of any one provider's stack. An unset declaration registers no client,
	// which refuses the flow rather than disabling a check.
	clientRegistry, err := auth.NewClientRegistry(identityEnv("IDENTITY_REGISTERED_CLIENTS"))
	if err != nil {
		return nil, fmt.Errorf("configure registered clients: %w", err)
	}
	service.SetClientRegistry(clientRegistry)

	// Client ID Metadata Document clients (issue #1003). A client whose
	// client_id is an https URL publishes its own registration there; the host
	// fetches and validates it, and writes nothing durable. Whether this host
	// trusts that mechanism is a DEPLOYMENT decision, not a per-tenant one —
	// see the handbook's decisions/registered-clients.md — and an unset
	// declaration refuses every such client, exactly as an unset registry
	// registers none.
	clientMetadataPolicy, err := auth.NewClientMetadataPolicy(
		identityEnv("IDENTITY_CLIENT_METADATA_DOCUMENTS"))
	if err != nil {
		return nil, fmt.Errorf("configure client metadata documents: %w", err)
	}
	service.SetClientMetadataResolver(auth.NewClientMetadataResolver(clientMetadataPolicy))

	// The OAuth 2.1 / MCP authorization-server surface. Raw HTTP rather than
	// transcoded RPCs because the token endpoint's form encoding and error
	// bodies are the contract a standards-written client reads; see
	// pkg/adapters/oauth_http.go.
	// Each path on its own, spelled out rather than looped: RegisterHTTPRoute
	// matches by prefix, so registering the `/v1/oauth2/` namespace would claim
	// every path under it and promise the gateway routes something below it —
	// and the correspondence gate in module/tools can only check a call site it
	// can resolve statically, which a loop over a slice is not.
	oauthHandler := adapters.NewOAuthHTTPHandler(service)
	adapters.RegisterHTTPRoute(adapters.OAuthMetadataPath, oauthHandler)
	adapters.RegisterHTTPRoute(adapters.OAuthAuthorizeValidatePath, oauthHandler)
	adapters.RegisterHTTPRoute(adapters.OAuthAuthorizeGrantPath, oauthHandler)
	adapters.RegisterHTTPRoute(adapters.OAuthTokenPath, oauthHandler)

	// Authentication mode is explicit in the Codefly identity configuration.
	// A selected fixture is an optional data seed and cannot replace the
	// configured provider. Fixture authentication must itself be selected and
	// additionally requires an explicit fixture.
	v, ex, err := buildProviderStack(authProvider, selectedFixture)
	if err != nil {
		return nil, fmt.Errorf("configure authentication: %w", err)
	}
	switch authProvider {
	case "dev":
		service.SetDevelopmentTokenValidator(v)
	case "header-jwt":
		// Gateway-pre-authenticated: no OAuth ceremony, no code exchange. The
		// login route consumes the configured identity header and hands its
		// value to the validator; sessions/refresh/MFA are unchanged afterwards.
		headerName := identityEnv("IDENTITY_HEADER_NAME")
		if !hasConfiguredValue(headerName) {
			return nil, fmt.Errorf("identity provider header-jwt requires IDENTITY_HEADER_NAME")
		}
		service.SetHeaderJWTTokenValidator(v)
		adapters.SetHeaderJWTLoginHeader(headerName)
	default:
		// user_identities.provider is a foreign key into the identity_providers
		// catalog. Verify the configured provider is registered now so an
		// unseeded name fails at startup with a precise error instead of a raw
		// FK violation at the user's first login.
		registered, err := store.ProviderRegistered(ctx, authProvider)
		if err != nil {
			return nil, fmt.Errorf("verify identity provider registration: %w", err)
		}
		if !registered {
			return nil, fmt.Errorf(
				"identity provider %q is not registered in identity_providers; add a database migration seeding it", authProvider)
		}
		if authProvider == "workos" {
			// SSO administration is a WorkOS-specific optional adapter. Other
			// identity providers cannot accidentally activate it by exposing a
			// similarly named management credential.
			service.SetSSOManagementAPIKey(identityProviderSecret("IDENTITY_MANAGEMENT_API_KEY"))
		}
		oauthPolicy, err := buildOAuthRequestPolicy(authProvider)
		if err != nil {
			return nil, fmt.Errorf("configure OAuth request policy: %w", err)
		}
		service.SetOAuthRequestPolicy(oauthPolicy)
		service.SetTokenValidator(v)
		service.SetCodeExchanger(ex)

		// Per-org identity provider registry (issue #107). Orgs with an active
		// row in org_identity_providers resolve to their own stack; every other
		// org falls back to this global default (v, ex). Stacks build lazily on
		// first use and are cache-invalidated when their configuration changes.
		service.SetIdentityProviderRegistry(
			newIdentityProviderRegistry(store, cipher, authProvider, v, ex))
	}

	// Audit persistence and matching webhook fan-out share one database
	// transaction. There is no process-local queue to saturate or lose on crash.
	// AUDIT_SINK=both additionally tees each org-scoped audit row to an external
	// warehouse, fed asynchronously from the durable outbox — Postgres stays the
	// atomic source of truth (an external destination cannot join its tx).
	auditSinkMode, externalAuditSink, err := configuredAuditSink()
	if err != nil {
		return nil, err
	}
	var auditEmitterOpts []business.DurableAuditEmitterOption
	if auditSinkMode == auditSinkBoth {
		auditEmitterOpts = append(auditEmitterOpts, business.WithExternalTee())
	}
	// Every org-scoped audit record publishes its external domain event in the
	// same transaction; that event is what the relay fans out to the endpoints
	// subscribed to its type.
	auditEmitterOpts = append(auditEmitterOpts, business.WithDomainEventTransport(eventTransport))
	auditEmitter, err := business.NewDurableAuditEmitter(store, store, auditEmitterOpts...)
	if err != nil {
		return nil, err
	}
	service.SetAuditEmitter(auditEmitter)
	// Refuse to serve behind an emitter that cannot write on the caller's
	// transaction: every security mutation commits its audit row and webhook
	// fan-out inside its own transaction, and an emitter without EmitTx would let
	// those mutations succeed unrecorded.
	if err := service.VerifyAuditWiring(); err != nil {
		return nil, err
	}

	var auditExportWorker *jobs.Worker
	if auditSinkMode == auditSinkBoth {
		// The tee carries org-scoped events only. Control-plane / platform-admin
		// audit events (NULL org) are Postgres-only: the job platform reserves
		// global-scope enqueue for the privileged worker pool, which opens its own
		// transaction and so cannot commit atomically with the audit row. Surface
		// this so operators don't assume the warehouse holds platform events.
		w.Warn("AUDIT_SINK=both tees only org-scoped audit events to the external sink; control-plane/platform-admin (NULL-org) events remain Postgres-only")
		auditExportHandler, err := business.NewAuditExportJobHandler(externalAuditSink, store)
		if err != nil {
			return nil, err
		}
		auditExportWorker, err = jobs.NewWorker(jobs.WorkerConfig{
			Store:      jobStore,
			Queue:      business.AuditExportQueue,
			Handler:    auditExportHandler,
			RetryDelay: business.AuditExportRetryDelay,
		})
		if err != nil {
			return nil, err
		}
	}

	// Reconcile the audit event-type registry projection from the code catalog
	// and provision the current + upcoming monthly partitions so audit writes
	// always have a target. Best-effort: a transient failure here must not
	// block boot; the retention tick re-provisions partitions on its cycle.
	// A catalog that collides with a solution-declared type is not transient —
	// this release cannot own that name without reinterpreting rows already
	// written under the solution's schema — so it fails boot.
	if err := store.WithControlPlane(ctx, func(ctx context.Context) error {
		return store.SyncAuditEventTypes(ctx, business.AuditEventCatalog())
	}); err != nil {
		if errors.Is(err, business.ErrAuditCatalogCollision) {
			return nil, fmt.Errorf("audit event-type registry sync: %w", err)
		}
		wool.Get(ctx).Warn("audit event-type registry sync failed", wool.ErrField(err))
	}
	if err := store.WithControlPlane(ctx, func(ctx context.Context) error {
		return store.EnsureAuditPartitions(ctx, 3)
	}); err != nil {
		wool.Get(ctx).Warn("audit partition provisioning failed", wool.ErrField(err))
	}

	// Every outbound path shares the generated generic job runtime. This
	// separate pool can only read endpoint configuration and project outcomes.
	webhookSender := business.NewWebhookSender(cipher, webhookPolicy)
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

	// Redis wiring. accounts declares the `cache` dependency, and one client on
	// it carries two different things:
	//
	//   - org membership, a CACHE of the store: a codefly.dev/cache stack
	//     (in-process tier, the shared Redis layer, the store as origin) that
	//     every membership mutation invalidates across replicas;
	//   - revocation markers, OAuth nonces and rate-limit counters, which are
	//     authoritative STATE with no origin behind them (pkg/redisstate) and
	//     fail closed on a Redis error. That server must run noeviction.
	//
	// A connection that is missing, unreadable or malformed fails boot: it is a
	// configuration error, and running on would drop revocation silently. A
	// server that cannot be REACHED is not: the membership stack retries for a
	// bounded time, then runs store-only (every membership read goes to the
	// store, which is never stale), loudly, while revocation and nonces keep
	// failing closed per request until Redis answers.
	var closeCache func() error
	rateLimiterWired := false
	redisClient, err := infra.NewRedisClient(ctx)
	if err != nil {
		return nil, err
	}
	membershipCache, err := membership.Connect(ctx, infra.MembershipCacheLayer(redisClient), store, membership.BootRetry,
		membership.WithEviction(infra.LegacyMembershipKeyEvictor(redisClient)))
	if err != nil {
		_ = redisClient.Close()
		return nil, fmt.Errorf("org membership cache: %w", err)
	}
	if cause := membershipCache.StoreOnly(); cause != nil {
		w.Error("ORG MEMBERSHIP CACHE IS STORE-ONLY — Redis could not be reached at boot, so every membership check reads the store until this replica restarts", wool.ErrField(cause))
	}
	registerMembershipCacheHealth(membershipCache)
	adapters.WithOrgMembershipCache(membershipCache)
	service.SetMembershipInvalidator(adapters.NewCacheInvalidator())
	state := redisstate.NewRedis(redisClient)
	// Wire Redis-backed access-token revocation. Without this,
	// Logout only kills the refresh chain — old access tokens
	// remain valid until natural expiry (15 min default).
	minter.SetRevoker(redisstate.NewTokenRevoker(state))
	// Redis-backed OAuth-state one-shot list so a captured state can't be
	// replayed within its TTL across replicas (the in-memory default only
	// covers a single process).
	stateSigner.SetNonceConsumer(redisstate.NewOAuthNonceConsumer(state))
	// Per-org / per-API-key rate limiting. Falls back to
	// allow-all if the Redis call fails.
	adapters.WithRateLimiter(redisstate.NewRateLimiter(state))
	rateLimiterWired = true
	closeCache = func() error {
		membershipCache.Close()
		return redisClient.Close()
	}
	if anonymousEndpointsUnprotected(abuseDisabled, rateLimiterWired) {
		w.Warn("ANONYMOUS ENDPOINTS UNPROTECTED — abuse protection is disabled AND no Redis rate limiter is wired, so Authenticate/RegisterUser/JoinWaitlist/SendMagicLink have NO app-layer throttle; enable ABUSE_PROTECTION_MODE=turnstile or wire the cache dependency before serving production traffic")
	}

	adapters.WithService(service)

	// Local development surfaces the underlying Authenticate failure reason for
	// debugging; every deployed environment returns generic auth errors so the
	// identity/enumeration oracle stays closed (#208).
	adapters.SetExposeAuthErrorDetail(codefly.IsLocal())
	// A browser discards a Secure cookie on a plaintext loopback origin, so local
	// development needs the exemption; nothing else does, and the default keeps
	// Secure.
	adapters.SetAllowInsecureRefreshCookie(codefly.IsLocal())

	// Separate shared-secret guards for internal RPC admission and forwarded
	// gateway identity. Empty values fail closed; production deploys provide
	// independent high-entropy values to both accounts and auth-gateway.
	adapters.SetInternalToken(workspaceEnv("internal-auth", "CODEFLY_INTERNAL_TOKEN"))
	// A previous internal token stays valid alongside the current one during an
	// overlapping rotation window, so callers can be migrated without a flag day.
	adapters.SetInternalTokenRotation(workspaceEnv("internal-auth", "CODEFLY_INTERNAL_TOKEN_PREVIOUS"))
	// The gateway credential lives in its own group, which only accounts and
	// auth-gateway declare: internal-auth also reaches the frontend, and the
	// holder of this credential can assert any identity to accounts. The
	// previous value is accepted beside it only while a rotation rolls the
	// gateway, and only until its expiry (TRUST_BOUNDARY.md).
	gatewayCredentials, err := configuredGatewayTrust(time.Now())
	if err != nil {
		return nil, err
	}
	adapters.SetGatewayToken(gatewayCredentials.current)
	adapters.SetPreviousGatewayToken(gatewayCredentials.previous)
	adapters.SetPreviousGatewayTokenExpiresAt(gatewayCredentials.previousExpiresAt)
	// One delivered key per purpose, never derived from the perimeter credential:
	// see business.SetDatasourceKeys. A key a deployed runtime may not use is
	// passed as ABSENT, which leaves its purpose unavailable — the same answer as
	// an unprovisioned key, and the reason the loader does not need a second one.
	service.SetDatasourceKeys(
		usableDatasourceKey("DATASOURCE_CONTENT_TICKET_KEY", codefly.IsLocal()),
		usableDatasourceKey("DATASOURCE_ACCOUNT_LINK_KEY", codefly.IsLocal()),
	)

	centralEnforcement, err := configuredCentralEnforcement()
	if err != nil {
		return nil, err
	}
	adapters.SetCentralEnforcementMode(centralEnforcement)

	// /v1/status — public health probe surface. Probes run in parallel
	// with a 2s budget each; overall status is the worst result. The
	// FE /status page reads this; k8s liveness/readiness can too.
	adapters.RegisterStatusProbe(adapters.StatusProbe{
		Name: "postgres",
		Check: func(ctx context.Context) error {
			return store.Pool().Ping(ctx)
		},
	})
	// A deployment on the cloud key service has no Vault at all, so there is no
	// probe to register: registering one that always failed would report a
	// dependency this deployment deliberately does not have.
	if keys.VaultHealth != nil {
		adapters.RegisterStatusProbe(adapters.StatusProbe{
			Name:  "vault",
			Check: keys.VaultHealth,
		})
	}
	adapters.RegisterHTTPRoute("/v1/status", adapters.NewStatusHTTPHandler(service))
	// The live companion to the follow bridge above: the same declared followable
	// resources read forward by a connected client. It declares nothing of its
	// own, so a composition that declares no followable resource leaves the stream
	// open and silent rather than leaking undeclared journal traffic.
	adapters.RegisterHTTPRoute(adapters.SubscriptionStreamPath, adapters.NewSubscriptionStreamHandler(service))

	// Email production and transport are split by the generic outbox. Request
	// paths render templates and enqueue exact messages in their product
	// transaction; this worker is the only owner of the provider adapter. With
	// EMAIL_PROVIDER=disabled there is no outbox and no worker: every request path
	// already treats a nil outbox as "this deployment sends no email", and
	// invitations are then handed to the inviting administrator as a link.
	orgCreationPolicy, err := business.ParseOrganizationCreationPolicy(applicationEnv("ORGANIZATION_CREATION"))
	if err != nil {
		return nil, fmt.Errorf("application: %w", err)
	}
	service.SetOrganizationCreationPolicy(orgCreationPolicy)

	emailConfig, err := configuredEmail(ctx, codefly.IsLocal())
	if err != nil {
		return nil, err
	}
	appBase, err := configuredApplicationBaseURL()
	if err != nil {
		return nil, err
	}
	// The one origin this deployment treats as its own: what binds an OAuth
	// redirect, an authenticator's relying party and every emailed link. From here
	// on a forwarded origin is honoured only when it equals this one, so no hop can
	// substitute a caller's choice (pkg/auth.WithVerifiedPublicOrigin).
	auth.SetConfiguredPublicOrigin(appBase)
	var workerEmailOutbox *email.Outbox
	var emailWorker *jobs.Worker
	if emailConfig.sender == nil {
		// No outbox, but links handed to an administrator still need the
		// canonical origin.
		service.SetEmailOutbox(nil, appBase)
		w.Warn("EMAIL DELIVERY DISABLED — EMAIL_PROVIDER=disabled: invitations, magic links and notifications send no email; an invitation's accept link is returned to the administrator who issues it")
	} else {
		templateStore := infra.NewPostgresTemplateStore(store)
		requestEmailOutbox, err := email.NewOutbox(store, templateStore, emailConfig.from)
		if err != nil {
			return nil, err
		}
		service.SetEmailOutbox(requestEmailOutbox, appBase)
		workerEmailOutbox, err = email.NewOutbox(jobStore, templateStore, emailConfig.from)
		if err != nil {
			return nil, err
		}
		if resend, ok := emailConfig.sender.(*email.ResendSender); ok {
			resendWebhook, err := resend.DeliveryWebhook(jobStore)
			if err != nil {
				return nil, err
			}
			adapters.RegisterHTTPRoute(email.ResendWebhookPath, resendWebhook)
		}
		emailJobHandler, err := email.NewJobHandler(emailConfig.sender)
		if err != nil {
			return nil, err
		}
		emailWorker, err = jobs.NewWorker(jobs.WorkerConfig{
			Store: jobStore, Queue: email.DeliveryQueue,
			Handler: emailJobHandler, RetryDelay: email.DeliveryRetryDelay,
		})
		if err != nil {
			return nil, err
		}
	}

	// Selecting the free plan is a first-party operation and must remain
	// available when Stripe is not configured (the default local/test setup).
	billingHTTPHandler := adapters.NewBillingHTTPHandler(service)
	adapters.RegisterHTTPRoute("/v1/billing/free-plan", billingHTTPHandler)

	// Billing: when Stripe is configured, wire its webhook plus the
	// authenticated checkout and portal endpoints. The auth-gateway's public-path
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

	// Datasource ingestion (issues #274/#275): the leased worker that performs
	// off-request repo pulls runs whenever datasources are usable. It leases sync
	// requests enqueued by DatasourceService.SyncSource and walks the repo without
	// a request deadline, so a large repo can't time out the RPC and gets retry
	// backoff long enough to outlast a GitHub rate-limit window.
	datasourceSyncWorker, err := jobs.NewWorker(jobs.WorkerConfig{
		Store:      jobStore,
		Queue:      business.DatasourceSyncRequestQueue,
		Handler:    service.NewDatasourceSyncJobHandler(),
		RetryDelay: business.DatasourceSyncRetryDelay,
	})
	if err != nil {
		return nil, err
	}

	// Privacy export/deletion (issue #539): the leased worker that drives a
	// configured privacy adapter. It runs whether or not one is wired — the
	// starter ships none — so a job enqueued by a runtime that had an adapter
	// dead-letters visibly here instead of sitting pending after the adapter is
	// removed.
	privacyWorker, err := jobs.NewWorker(jobs.WorkerConfig{
		Store:      jobStore,
		Queue:      business.PrivacyWorkflowQueue,
		Handler:    service.NewPrivacyJobHandler(),
		RetryDelay: business.PrivacyWorkflowRetryDelay,
	})
	if err != nil {
		return nil, err
	}

	// The change-set compiler (issue #487): a leased worker that turns each raw
	// GitHub delivery — and each periodic/forced reconcile request — into a set of
	// per-file ingest ops, holding the source's decrypted token so all GitHub
	// calls carry it. The queue's per-source FIFO ordering key keeps one delivery
	// in flight per source, so a source's cursor is read and advanced without a
	// second delivery racing it.
	datasourceDeliveryWorker, err := jobs.NewWorker(jobs.WorkerConfig{
		Store:      jobStore,
		Queue:      business.DatasourceDeliveryQueue,
		Handler:    service.NewDatasourceDeliveryJobHandler(),
		RetryDelay: business.DatasourceSyncRetryDelay,
	})
	if err != nil {
		return nil, err
	}

	// The GitHub App installation reconciler (issue #691): it leases each
	// verified App-level delivery and re-derives the affected sources' access
	// from GitHub, rather than acting on what the delivery claimed.
	datasourceInstallationWorker, err := jobs.NewWorker(jobs.WorkerConfig{
		Store:      jobStore,
		Queue:      business.DatasourceInstallationQueue,
		Handler:    service.NewDatasourceInstallationJobHandler(),
		RetryDelay: business.DatasourceSyncRetryDelay,
	})
	if err != nil {
		return nil, err
	}

	// The inbound GitHub push webhook is an unauthenticated, un-rate-limited edge
	// that resolves a per-source signing secret from the credential store (a
	// control-plane DB read) on every request. It is opt-in per deployment so a
	// deployment not using datasources exposes no such surface; once enabled, a
	// source added through the SDK is live with no redeploy. Receipt verifies
	// X-Hub-Signature-256 and persists the raw delivery to the jobs inbox under
	// the GitHub delivery id; the documents ingest service consumes it — saas owns
	// connection, documents owns ingest. The switch is a declared key of the
	// github-app configuration group, so a deployment turns it on from its own
	// configuration rather than from an environment variable nothing declares.
	//
	// Whether it is mounted is also told to the service, from here rather than
	// from where the rest of the datasource configuration is read, so the fact a
	// client is shown ("this source has live delivery") cannot drift from whether
	// an endpoint exists to deliver it. A per-source signing secret verifies
	// nothing when nothing is listening.
	perSourceWebhookMounted := strings.EqualFold(
		strings.TrimSpace(workspaceEnv("github-app", "DATASOURCE_GITHUB_WEBHOOK_ENABLED")), "true")
	service.SetDatasourceWebhookReceiverMounted(perSourceWebhookMounted)
	if perSourceWebhookMounted {
		adapters.RegisterHTTPRoute(datasource.GitHubWebhookPath, datasource.NewHandler(
			datasource.GitHubWebhookPath,
			datasource.HandlerDeps{Producer: jobStore, Sources: datasourceSourceResolver{svc: service}},
		))
		w.Info("GitHub datasource webhook enabled")
	}

	// The App's own deliveries arrive at a second, App-wide endpoint, signed
	// with the registration's own secret. `installation` and
	// `installation_repositories` are delivered only to the App registration's
	// webhook URL, so neither the per-source path nor a per-source secret can
	// receive them. `push` is here for a related reason (issue #734): an
	// App-backed source authenticates through the installation and stores no
	// push secret, so the per-source receiver could never verify its pushes.
	// Mounting is gated on an actual registration, so a deployment that
	// registered no App exposes no such surface.
	if service.GitHubAppWebhookConfigured() {
		adapters.RegisterHTTPRoute(datasource.GitHubAppWebhookPath, datasource.NewAppHandler(
			datasource.AppHandlerDeps{Producer: jobStore, Registration: service},
		))
		w.Info("GitHub App webhook enabled")
	} else if service.GitHubAppConfigured() {
		// Half a registration is the dangerous shape: sources mint App tokens and
		// look healthy, while GitHub's lifecycle deliveries land on a route that
		// does not exist. Revocation then stays invisible for the life of a
		// cached token with nothing anywhere to say why.
		w.Warn("GitHub App registered without a webhook secret; installation lifecycle events cannot be verified and will not be received")
	}
	// Say the whole state once, at boot, whichever way it came out. "Is the
	// webhook configured on this environment?" was previously answerable only by
	// reading configuration on the box: a deployment with neither endpoint
	// mounted logged nothing at all, and every source on it looked the same as a
	// source on a deployment with both. A source is still kept current by the
	// periodic reconcile, so this is a statement of latency, not of breakage.
	if !service.GitHubAppWebhookConfigured() && !perSourceWebhookMounted {
		w.Info("no GitHub push endpoint is configured on this deployment; datasource content is refreshed by the periodic reconcile and by Sync now only")
	}

	// Start background data retention goroutine. Runs once on startup and
	// then every 24 hours, deleting records older than their configured
	// retention period.
	retentionCtx, retentionCancel := context.WithCancel(ctx)
	go func() {
		rw := wool.Get(retentionCtx).In("retention")
		runRetention := func() {
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
		// Single-use replay markers expire with their token (≤ 15 min), so they
		// are swept far more often than the daily retention pass or they would
		// pile up for a day before reclamation.
		sweepReplay := func() {
			if n, err := service.PurgeExpiredWorkContextReplays(retentionCtx); err != nil {
				rw.Warn("replay marker sweep failed", wool.ErrField(err))
			} else if n > 0 {
				rw.Info("deleted records", wool.Field("count", n), wool.Field("kind", "work_context_replay"))
			}
		}

		// An export artifact stays reachable only for its authorized window; the
		// sweep asks the adapter to delete the stored object and drops the
		// reference once that window has closed.
		sweepPrivacyArtifacts := func() {
			if n, err := service.PurgeExpiredPrivacyArtifacts(retentionCtx); err != nil {
				rw.Warn("expired privacy artifact sweep failed", wool.ErrField(err))
			} else if n > 0 {
				rw.Info("deleted records", wool.Field("count", n), wool.Field("kind", "privacy_export_artifact"))
			}
		}

		// Datasource reconcile (issue #487 §6): the production safety net for lost
		// webhooks and for local development without a public tunnel. Each sweep
		// enqueues a reconcile job for every active GitHub source whose schedule has
		// elapsed; the job resolves the head and snapshots only if it moved, so the
		// sweep does no GitHub work itself.
		sweepReconcile := func() {
			if n, err := service.RunDatasourceReconcile(retentionCtx); err != nil {
				rw.Warn("datasource reconcile sweep failed", wool.ErrField(err))
			} else if n > 0 {
				rw.Info("enqueued datasource reconciles", wool.Field("count", n))
			}
		}

		// GitHub App installation re-check (issue #691): the same safety net, for
		// the half that webhooks alone cannot cover. Parking a source removes it
		// from the reconcile sweep above, so a restored installation whose
		// `unsuspend` delivery GitHub failed to hand over would leave that source
		// parked for good. This re-verifies installations that still hold one.
		// The sweep runs on the same tick, but each installation is only enqueued
		// once per re-check window, so the tick does not set the rate.
		sweepInstallationRecheck := func() {
			if n, err := service.RunGitHubInstallationRecheck(retentionCtx); err != nil {
				rw.Warn("github installation recheck sweep failed", wool.ErrField(err))
			} else if n > 0 {
				rw.Info("enqueued github installation rechecks", wool.Field("count", n))
			}
		}

		// Run once immediately on startup.
		runRetention()
		sweepReplay()
		sweepPrivacyArtifacts()
		sweepReconcile()
		sweepInstallationRecheck()

		retentionTicker := time.NewTicker(24 * time.Hour)
		replayTicker := time.NewTicker(time.Hour)
		reconcileTicker := time.NewTicker(time.Minute)
		defer retentionTicker.Stop()
		defer replayTicker.Stop()
		defer reconcileTicker.Stop()
		for {
			select {
			case <-retentionCtx.Done():
				return
			case <-retentionTicker.C:
				runRetention()
			case <-replayTicker.C:
				sweepReplay()
				sweepPrivacyArtifacts()
			case <-reconcileTicker.C:
				sweepReconcile()
				sweepInstallationRecheck()
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
	if solutionHostBindingReconciler != nil {
		solutionHostBindingReconciler.Start(ctx)
	}
	if analyticsWorker != nil {
		analyticsWorker.Start(ctx)
	}
	if auditExportWorker != nil {
		auditExportWorker.Start(ctx)
	}
	if emailWorker != nil {
		emailWorker.Start(ctx)
	}
	webhookWorker.Start(ctx)
	datasourceSyncWorker.Start(ctx)
	privacyWorker.Start(ctx)
	datasourceDeliveryWorker.Start(ctx)
	datasourceInstallationWorker.Start(ctx)
	eventRelayWorker.Start(ctx)
	followFanoutWorker.Start(ctx)

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
		if auditExportWorker != nil {
			sw.Info("stopping audit export worker")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := auditExportWorker.Shutdown(shutdownCtx); err != nil {
				sw.Warn("audit export worker shutdown timed out", wool.ErrField(err))
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
		if solutionHostBindingReconciler != nil {
			sw.Info("stopping solution host binding reconciler")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := solutionHostBindingReconciler.Shutdown(shutdownCtx); err != nil {
				sw.Warn("solution host binding reconciler shutdown timed out", wool.ErrField(err))
			}
			cancel()
		}
		if emailWorker != nil {
			sw.Info("stopping email delivery worker")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := emailWorker.Shutdown(shutdownCtx); err != nil {
				sw.Warn("email delivery worker shutdown timed out", wool.ErrField(err))
			}
			cancel()
		}
		sw.Info("stopping outbound webhook worker")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := webhookWorker.Shutdown(shutdownCtx); err != nil {
			sw.Warn("outbound webhook worker shutdown timed out", wool.ErrField(err))
		}
		cancel()
		sw.Info("stopping privacy workflow worker")
		shutdownCtx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		if err := privacyWorker.Shutdown(shutdownCtx); err != nil {
			sw.Warn("privacy workflow worker shutdown timed out", wool.ErrField(err))
		}
		cancel()
		sw.Info("stopping datasource sync worker")
		shutdownCtx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		if err := datasourceSyncWorker.Shutdown(shutdownCtx); err != nil {
			sw.Warn("datasource sync worker shutdown timed out", wool.ErrField(err))
		}
		cancel()
		sw.Info("stopping datasource delivery worker")
		shutdownCtx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		if err := datasourceDeliveryWorker.Shutdown(shutdownCtx); err != nil {
			sw.Warn("datasource delivery worker shutdown timed out", wool.ErrField(err))
		}
		cancel()
		sw.Info("stopping datasource installation worker")
		shutdownCtx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		if err := datasourceInstallationWorker.Shutdown(shutdownCtx); err != nil {
			sw.Warn("datasource installation worker shutdown timed out", wool.ErrField(err))
		}
		cancel()
		sw.Info("stopping domain-event relay worker")
		shutdownCtx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		if err := eventRelayWorker.Shutdown(shutdownCtx); err != nil {
			sw.Warn("domain-event relay worker shutdown timed out", wool.ErrField(err))
		}
		cancel()
		sw.Info("stopping follow fan-out worker")
		shutdownCtx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		if err := followFanoutWorker.Shutdown(shutdownCtx); err != nil {
			sw.Warn("follow fan-out worker shutdown timed out", wool.ErrField(err))
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

type auditSinkMode int

const (
	auditSinkPostgres auditSinkMode = iota
	auditSinkBoth
)

// configuredAuditSink selects the audit backend. "postgres" (default) keeps the
// durable emitter alone. "both" adds an asynchronous tee to an external
// warehouse while Postgres stays the atomic source of truth. "external" is
// rejected: an external destination cannot join the audit transaction, so it
// cannot replace the durable emitter without forfeiting audit/webhook
// atomicity — a deliberate posture we refuse to let a config value break.
func configuredAuditSink() (auditSinkMode, business.ExternalAuditSink, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AUDIT_SINK"))) {
	case "", "postgres":
		return auditSinkPostgres, nil, nil
	case "both":
		sink, err := configuredExternalAuditSink()
		if err != nil {
			return auditSinkPostgres, nil, err
		}
		return auditSinkBoth, sink, nil
	case "external":
		return auditSinkPostgres, nil, fmt.Errorf(
			"AUDIT_SINK=external is not permitted: Postgres is the atomic source of truth and an external destination cannot join its transaction; use AUDIT_SINK=both to tee to the external sink")
	default:
		return auditSinkPostgres, nil, fmt.Errorf("AUDIT_SINK must be postgres or both")
	}
}

func configuredExternalAuditSink() (business.ExternalAuditSink, error) {
	endpoint := strings.TrimSpace(os.Getenv("AUDIT_EXTERNAL_URL"))
	if endpoint == "" {
		return nil, fmt.Errorf("AUDIT_SINK=both requires AUDIT_EXTERNAL_URL for the external audit destination")
	}
	return business.NewHTTPAuditSink(business.HTTPAuditSinkConfig{
		Endpoint: endpoint,
		Token:    strings.TrimSpace(os.Getenv("AUDIT_EXTERNAL_TOKEN")),
	})
}

// anonymousEndpointsUnprotected reports whether the four anonymous endpoints
// have NO app-layer throttle: abuse protection disabled AND no rate limiter
// wired (no Redis). Either guard alone is a backstop; only the combination
// leaves them open.
// retryFollowSubscriptions re-attempts follow subscription materialization
// until it lands. A host with no subscription receives no followable event, and
// that loss is invisible at runtime — this is what closes the gap left by a
// database outage during boot without holding the service hostage to it. It
// backs off so a sustained outage does not become sustained load, and reports
// every attempt so the gap stays visible while it is open.
func retryFollowSubscriptions(ctx context.Context, service *business.Service) {
	const maxDelay = 5 * time.Minute
	delay := 5 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if err := service.MaterializeFollowSubscriptions(ctx); err != nil {
			wool.Get(ctx).In("follows").Warn(
				"retrying follow subscription materialization", wool.ErrField(err))
			if delay < maxDelay {
				delay *= 2
			}
			continue
		}
		wool.Get(ctx).In("follows").Info("follow subscriptions materialized after retry")
		return
	}
}

func anonymousEndpointsUnprotected(abuseDisabled, rateLimiterWired bool) bool {
	return abuseDisabled && !rateLimiterWired
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

// configuredCentralEnforcement selects request-time central RBAC admission from
// the security configuration. "enforce" turns hard-deny on; "shadow" (and an
// unset value) keeps the classify-only fallback. Any other value fails the boot
// so a typo cannot silently leave enforcement off.
func configuredCentralEnforcement() (bool, error) {
	switch strings.ToLower(strings.TrimSpace(workspaceEnv("security", "RBAC_CENTRAL_ENFORCEMENT"))) {
	case "", "shadow":
		return false, nil
	case "enforce":
		return true, nil
	default:
		return false, fmt.Errorf(`RBAC_CENTRAL_ENFORCEMENT must be "shadow" or "enforce"`)
	}
}

// maxGatewayRotationWindow is the furthest ahead a previous gateway credential
// may expire. The window only has to outlast one auth-gateway rollout, and
// until it closes the old value can assert any identity to accounts.
const maxGatewayRotationWindow = 24 * time.Hour

// gatewayTrust is the gateway credential accounts accepts, and during a
// rotation the previous one with the instant it stops being accepted.
type gatewayTrust struct {
	current           string
	previous          string
	previousExpiresAt time.Time
}

// configuredGatewayTrust reads the gateway-trust group. A previous credential
// must differ from the current one and carry an RFC 3339
// CODEFLY_GATEWAY_TOKEN_PREVIOUS_EXPIRES_AT after now and at most
// maxGatewayRotationWindow ahead; anything else fails the boot, so a
// rotated-out credential cannot keep its authority because nobody cleared the
// slot. An expiry with no previous credential bounds nothing and is ignored.
func configuredGatewayTrust(now time.Time) (gatewayTrust, error) {
	trust := gatewayTrust{
		current:  workspaceEnv("gateway-trust", "CODEFLY_GATEWAY_TOKEN"),
		previous: workspaceEnv("gateway-trust", "CODEFLY_GATEWAY_TOKEN_PREVIOUS"),
	}
	if trust.previous == "" {
		return trust, nil
	}
	if trust.previous == trust.current {
		return gatewayTrust{}, fmt.Errorf("gateway-trust: CODEFLY_GATEWAY_TOKEN_PREVIOUS must differ from CODEFLY_GATEWAY_TOKEN")
	}
	raw := strings.TrimSpace(workspaceEnv("gateway-trust", "CODEFLY_GATEWAY_TOKEN_PREVIOUS_EXPIRES_AT"))
	if raw == "" {
		return gatewayTrust{}, fmt.Errorf("gateway-trust: CODEFLY_GATEWAY_TOKEN_PREVIOUS_EXPIRES_AT is required while CODEFLY_GATEWAY_TOKEN_PREVIOUS is set")
	}
	expiresAt, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return gatewayTrust{}, fmt.Errorf("gateway-trust: CODEFLY_GATEWAY_TOKEN_PREVIOUS_EXPIRES_AT must be an RFC 3339 timestamp")
	}
	if !now.Before(expiresAt) {
		return gatewayTrust{}, fmt.Errorf("gateway-trust: CODEFLY_GATEWAY_TOKEN_PREVIOUS_EXPIRES_AT has passed; clear CODEFLY_GATEWAY_TOKEN_PREVIOUS")
	}
	if expiresAt.After(now.Add(maxGatewayRotationWindow)) {
		return gatewayTrust{}, fmt.Errorf("gateway-trust: CODEFLY_GATEWAY_TOKEN_PREVIOUS_EXPIRES_AT must be no more than %.0fh ahead", maxGatewayRotationWindow.Hours())
	}
	trust.previousExpiresAt = expiresAt
	return trust, nil
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

// configuredTokenIssuer is the `iss` every access token carries: the
// operator-configured public base URL, or the pre-metadata literal when no base
// URL is configured.
//
// RFC 8414 §2 requires an authorization server's issuer to be an https URL, so
// a deployment that publishes metadata must mint that URL — otherwise a client
// that discovered the metadata and then verified `iss` against it refuses every
// token. A deployment with no base URL publishes no metadata (the document
// would name URLs it cannot vouch for), so the literal is safe there: there is
// no published issuer for it to contradict.
func configuredTokenIssuer() (string, error) {
	base, err := configuredApplicationBaseURL()
	if err != nil {
		return "", err
	}
	if base == "" {
		return business.LegacyTokenIssuer, nil
	}
	return base, nil
}

// oauthIssuerOf is the issuer the authorization server PUBLISHES, which is the
// token issuer unless that is the literal — a literal is not a usable RFC 8414
// issuer, and publishing one would send a conforming client to
// `saas-starter/.well-known/...`. Empty means no metadata is published at all.
func oauthIssuerOf(tokenIssuer string) string {
	if tokenIssuer == business.LegacyTokenIssuer {
		return ""
	}
	return tokenIssuer
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
//	oidc       — any spec-compliant OpenID Connect provider (Okta,
//	             PingFederate, Entra, Keycloak, …), configured entirely
//	             from discovery plus the IDENTITY_* contract.
//	auth0      — generic OIDC flow with Auth0 defaults.
//	google     — generic OIDC flow with Google defaults.
//
// Any other non-empty value is a generic OpenID Connect provider named by
// IDENTITY_PROVIDER (so two enterprise IdPs occupy distinct user_identities
// (provider, provider_id) namespaces) but only when the operator opts in with
// IDENTITY_GENERIC_OIDC=true. Empty, incomplete, and undeclared-unknown
// configurations return an error so the service cannot start with an ambiguous
// authentication boundary.
// workspaceEnv reads a key from a named Codefly workspace configuration,
// including its secret namespace, and falls back to a plain process variable
// for deployments that do not use Codefly's configuration provider.
// configuredSolutionHostBindingReconciler builds the declared-presence
// reconciler and mounts the delivery endpoint, or returns nil when this host
// answers for no coordinate (issue #952).
//
// SOLUTION_HOST_COORDINATE is the ONE declaration that turns the surface on,
// and it is the coordinate the operator declared on the environment the renderer
// read. It is never derived here, because a coordinate this host invented would
// match nothing delivery ever wrote. With no coordinate there is no
// declared-presence surface at all: no reconciler, no delivery endpoint, and
// no new solution presence can be reconciled.
//
// THE SOURCE IS THE DURABLE INBOX AND NOTHING ELSE. It was a directory — a
// projected ConfigMap volume named by a workspace setting — and both that gate
// and the directory reader are gone. The directory could not be the desired set, for a
// reason that is a defect rather than a preference: a mount is the CURRENT set,
// so a document that arrived, was recorded as desired, failed to apply and then
// disappeared from the mount was never retried. The host had recorded that
// delivery wanted something and had no way to want it again. The mount also
// dropped the carrier, so a restore could not re-verify what it had accepted.
//
// Worse, while the gate existed the inbox was DEAD CODE in every deployment:
// unset meant no reconciler, no verifier and no delivery endpoint, and set meant
// the source was the mount — so the table migration 24 creates was written by
// the endpoint and read by nothing. Deleting the gate is what makes the inbox
// the only path, and there is now exactly one: a carrier is POSTed, verified on
// receipt, persisted, and re-verified by every reconcile pass that reads it.
//
// SOLUTION_HOST_BINDING_INTERVAL is optional and exists for a deployment that
// wants a tighter convergence bound than the default.
//
// WHERE the trust root and the allowlist are read from is NOT configurable. It
// is `infra.SolutionHostTrustAnchorPath`, a constant, and there is deliberately
// no environment value for it — see that constant for why an env var was a hole
// rather than a convenience. In short: workspace environment is delivered by the
// composition, so an overridable path lets a composition choose the anchor its
// own documents are checked against, and no amount of checking downstream
// recovers from that.
//
// The allowlist and the domains it grants come from ONE document at that path.
// When the domains lived in this environment instead, a deployer who could set
// the environment could widen what an accepted signer speaks for without
// touching the policy that was supposed to be independent of them.
func configuredSolutionHostBindingReconciler(
	service *business.Service, store *infra.PostgresStore,
) (*business.SolutionHostBindingReconciler, error) {
	coordinate := strings.TrimSpace(workspaceEnv("federation", "SOLUTION_HOST_COORDINATE"))
	if coordinate == "" {
		// NO DECLARED-PRESENCE SURFACE ALSO MEANS NO EXECUTION BINDING, and
		// that is the coherent answer rather than an oversight. The approved
		// build is read from delivered authority documents, and a host with no
		// coordinate has no delivery endpoint to receive one — so it approves
		// nothing, `SetExecutionBinding` is never called, and `BindExecution`
		// answers ErrExecutionUnbound for every caller. A module cannot mint
		// here, which is the fail-closed direction: the alternative is a host
		// that cannot establish what anyone is running and mints anyway.
		return nil, nil
	}
	// The ownership domains this host accepts delivery from. Required with the
	// coordinate for the same reason the coordinate is: Core refuses a document
	// from an unstated domain, and without the declaration any delivery could
	// claim an unseen binding ID under a domain of its own choosing and own it
	// from then on — the applied record cannot bound a binding's FIRST
	// generation, because there is nothing yet to compare against.
	var domains []string
	for _, domain := range strings.Split(workspaceEnv("federation", "SOLUTION_HOST_OWNERSHIP_DOMAINS"), ",") {
		if domain = strings.TrimSpace(domain); domain != "" {
			domains = append(domains, domain)
		}
	}
	if len(domains) == 0 {
		return nil, fmt.Errorf("SOLUTION_HOST_COORDINATE is declared without SOLUTION_HOST_OWNERSHIP_DOMAINS, so this host would accept a binding claimed under any domain a writer chose")
	}
	// THE TRUST ANCHOR MUST BE THERE, and its absence refuses the boot.
	//
	// Checked by name, first, and before anything else about verification is
	// read. A host that cannot verify a carrier must not start and claim to be
	// verifying: refusing per document instead makes "this host has no trust
	// root" and "delivery is shipping something bad" the same observable, and
	// those are the two facts an operator most needs to tell apart.
	//
	// First rather than somewhere inside the verifier, because the refusal an
	// operator reads has to name the thing that is missing. A policy value read
	// before this answered "SOLUTION_HOST_TRUST_POLICY is required" for a host
	// whose real problem was an unmounted anchor.
	if err := infra.RequireSolutionHostTrustAnchor(); err != nil {
		return nil, err
	}
	// The verifier owns the signer-to-domain mapping, and that is a change from
	// reading it out of SOLUTION_HOST_SIGNER_DOMAINS.
	//
	// Two reasons, both of which the adversarial reviews named. The env var's
	// key was a bare certificate SAN — no issuer, no repository, no ref — which
	// bakes a weaker allowlist shape into configuration: a SAN alone is accepted
	// from any issuer, and the same workflow path exists in every fork. And the
	// allowlist and the thing it authorizes were separable, so a deployer who
	// could set the environment could widen what an accepted signer speaks for
	// without touching the independently-delivered policy at all.
	//
	// Now both come from one document at a FIXED path, which a platform-owned
	// delivery path writes and neither delivery writer can.
	verifier, err := infra.NewSolutionHostBundleVerifier(
		infra.SolutionHostTrustPolicy(strings.TrimSpace(workspaceEnv("federation", "SOLUTION_HOST_TRUST_POLICY"))))
	if err != nil {
		return nil, err
	}
	// Every domain the policy grants a signer must be one this host accepts at
	// all. Refused BY NAME rather than intersected silently: a policy naming a
	// domain SOLUTION_HOST_OWNERSHIP_DOMAINS does not list is a disagreement
	// between two independently delivered documents, and quietly dropping the
	// entry reads exactly like a policy that works until the binding it was
	// meant to admit is withheld.
	domainsBySigner := verifier.SignerDomains()
	accepted := make(map[string]bool, len(domains))
	for _, domain := range domains {
		accepted[domain] = true
	}
	for signer, granted := range domainsBySigner {
		for _, domain := range granted {
			if !accepted[domain] {
				return nil, fmt.Errorf(
					"the verification policy lets signer %q deliver under ownership domain %q, which SOLUTION_HOST_OWNERSHIP_DOMAINS does not accept",
					signer, domain)
			}
		}
	}
	interval := business.SolutionHostBindingReconcileInterval
	if raw := strings.TrimSpace(workspaceEnv("federation", "SOLUTION_HOST_BINDING_INTERVAL")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return nil, fmt.Errorf("SOLUTION_HOST_BINDING_INTERVAL %q is not a positive duration", raw)
		}
		interval = parsed
	}
	// The ONLY way a document reaches this host is the delivery endpoint, so a
	// host that cannot review a carrier's credential has no input at all.
	// Saying so at boot beats a reconciler that polls an inbox nothing can ever
	// write to.
	kubernetes, err := infra.NewKubernetesClient()
	if err != nil {
		return nil, fmt.Errorf("the declared-presence surface has no input: a delivered carrier is authorised by TokenReview, and this host cannot reach its api server: %w", err)
	}
	service.SetSolutionDelivery(verifier, store, infra.NewSolutionDeliveryCarrierCheck(kubernetes), domainsBySigner)
	// Deliberately not routed at the gateway, and the asymmetry with the
	// credential mint is the reason. `POST /platform/_credential` is brokered by
	// the gateway because a solution runtime is an independently deployed
	// workload that must not reach accounts' internal listener. A delivery Job
	// is not that: it runs in-cluster as one of two known service accounts, so
	// routing it through the edge would put accounts-audience tokens across the
	// perimeter for no gain.
	//
	// codefly:gateway-route-exempt delivery is in-cluster and reaches accounts directly, never the public edge
	adapters.RegisterHTTPRoute(adapters.SolutionDeliveryPrefix, adapters.NewSolutionDeliveryHTTPHandler(service))

	// The AUTHORITY CEILING, from the same platform-owned anchor as the trust
	// root and for the same two reasons: a document must never carry its own
	// ceiling, and a ceiling the composition could point at is a ceiling the
	// composition chose.
	//
	// Absence and damage are different answers. No envelope delivered is a
	// complete deployment that answers no authority question — presence
	// reconciles and every activation refuses by name — while an envelope that
	// IS delivered and cannot be read refuses the boot, because reading a
	// damaged ceiling as "no ceiling" turns a corrupted file into a silently
	// narrower system and reading it permissively turns it into a wider one.
	envelope, _, err := infra.ReadSolutionAuthorityEnvelope()
	if err != nil {
		return nil, err
	}
	reconciler, err := business.NewSolutionHostBindingReconciler(service, business.SolutionHostBindingReconcilerConfig{
		Source:          business.NewDeliveredSolutionHostBindings(service),
		Verifier:        verifier,
		Coordinate:      coordinate,
		Domains:         domains,
		DomainsBySigner: domainsBySigner,
		Interval:        interval,
		Envelope:        envelope,
	})
	if err != nil {
		return nil, err
	}

	// EXECUTION-BOUND MINTING, wired here because this is where both of its
	// independent sources exist.
	//
	// THIS IS THE LINE THAT WAS MISSING. `BindExecution`, the three digest
	// types, the monotonicity guard and the TokenReview client were all built,
	// guarded and unit-tested, and `SetExecutionBinding` was called from
	// nowhere — so on a running host the reviewer was nil, `BindExecution`
	// answered ErrExecutionUnbound for every caller, and the whole mechanism was
	// unreachable. A check nothing calls is indistinguishable from a check that
	// passes, which is why this is wired rather than merely available.
	//
	// THE TWO SOURCES ARE DELIBERATELY DIFFERENT ONES, and that is the entire
	// design. The reviewer is the Kubernetes API, keyed by a pod UID that came
	// from a TokenReview of a token the caller could not forge: it answers what
	// the caller IS RUNNING. The authority answers what THIS HOST APPROVES, read
	// from signed documents the caller has no influence over. Filling both sides
	// from one source compares a value to itself and passes for every caller,
	// including the superseded pod the check exists to refuse.
	//
	// It is the SAME client that reviews a delivery carrier, which is right
	// rather than merely convenient: one api server, one in-cluster credential,
	// one place a lost credential stops both halves at once instead of leaving
	// one of them silently answering.
	service.SetExecutionBinding(kubernetes, reconciler.ApprovedBuilds())
	return reconciler, nil
}

func workspaceEnv(configuration, key string) string {
	if value, err := codefly.For(codefly.Context()).WorkspaceValue(configuration, key); err == nil && value != "" {
		return value
	}
	return os.Getenv(key)
}

func observabilityEnabled() bool {
	return strings.TrimSpace(workspaceEnv("observability", "OTEL_EXPORTER_OTLP_ENDPOINT")) != ""
}

// newDurableJobMetricsMonitor builds the durable job-operations metrics monitor
// only when OTEL metrics are enabled (metricsEnabled mirrors a non-nil
// otelMetricProvider, i.e. observabilityEnabled()). When metrics are disabled the
// global meter is a no-op, so a monitor would poll the job store every interval
// only to record into instruments nothing can read; returning a nil monitor lets
// the caller's Start/Shutdown nil-checks skip that background work entirely.
func newDurableJobMetricsMonitor(metricsEnabled bool, source jobs.Operations) (*jobs.OperationsMonitor, error) {
	if !metricsEnabled {
		return nil, nil
	}
	return jobs.NewOperationsMonitor(
		source,
		otel.Meter("github.com/codefly-dev/module-saas-starter/job-operations"),
		30*time.Second,
	)
}

// identityEnv is intentionally Codefly-only. Local dogfood, tests, and
// deployments all select a workspace configuration profile; raw process
// variables must not become a second, divergent identity authority.
func identityEnv(key string) string {
	value, _ := codefly.For(codefly.Context()).WorkspaceValue("identity", key)
	return value
}

// identityProviderSecret reads a credential that authenticates THIS HOST to the
// identity provider, from the `identity-provider` group only accounts declares.
//
// They used to live in `identity`, which the frontend declares too — it renders
// the login page from that group's public facts, the issuer and the client id —
// so a deployment putting the provider's credentials there delivered them to the
// frontend as well, which reads neither. A public-facing process holding the
// provider's administration credential widens what a frontend compromise is worth,
// for nothing.
//
// The read falls back to `identity` while a deployment still holds them there, and
// says so once per key: the group a value is delivered under is the composition's
// to change, and a hard cutover here would take sign-in down on every cell between
// this landing and that edit. The fallback goes when the cells have moved — until
// then, the warning is how an operator knows one has not.
func identityProviderSecret(key string) string {
	destination, _ := codefly.For(codefly.Context()).WorkspaceValue("identity-provider", key)
	legacy := identityEnv(key)

	// A PROVISIONED destination value always wins.
	//
	// "Provisioned" has to exclude the placeholder this module ships in the new
	// group's local defaults, and that is the whole of what this gets right:
	// hasConfiguredValue rejects only the literal REPLACE_ME, so the shipped
	// default read as configured and overrode a real value still delivered under
	// `identity` — defeating the migration fallback precisely on the cells that
	// have not migrated yet, which are the only ones that need it.
	if isProvisionedCredential(destination) {
		return destination
	}
	if isProvisionedCredential(legacy) {
		reportIdentityProviderSecretStillInIdentityGroup(key)
		return legacy
	}
	// Neither group holds a provisioned value: local development, where the
	// shipped placeholder IS the intended value. Prefer the destination group so a
	// developer edits the group this service now declares.
	if strings.TrimSpace(destination) != "" {
		return destination
	}
	return legacy
}

// isProvisionedCredential reports whether a value is one an operator supplied, as
// opposed to a placeholder this module ships or an unset key. It is the selection
// rule for a credential that is moving between configuration groups: a shipped
// default must not count as the destination being ready.
func isProvisionedCredential(value string) bool {
	return hasConfiguredValue(value) && !looksLikeShippedPlaceholder(value)
}

var identityProviderSecretWarned sync.Map

func reportIdentityProviderSecretStillInIdentityGroup(key string) {
	if _, loaded := identityProviderSecretWarned.LoadOrStore(key, true); loaded {
		return
	}
	wool.Get(context.Background()).In("identityProviderSecret").Warn(
		"an identity-provider credential is still delivered under the `identity` group, which the frontend also declares; move it to `identity-provider`, which only this service declares",
		wool.Field("key", key))
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
	// WorkOS validates its own access token, which binds to the application via a
	// non-standard client_id claim rather than aud == client id (see the oidc
	// validator and the WorkOS exchanger). Defaulting the audience to the client
	// id here would reject every WorkOS access token, so the binding stays
	// operator-configured (Audience or ClientIDClaim) as it is today.
	validator, tokenURL, clientID, clientSecret, err := discoverOIDCValidator(provider, false)
	if err != nil {
		return nil, nil, err
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

// buildGenericOIDCStack configures any spec-compliant OpenID Connect provider
// (Okta, PingFederate, Azure AD/Entra, Keycloak, …) from discovery plus the
// Codefly `identity` workspace configuration. It shares WorkOS's discovery and
// JWKS validation but pairs them with the standard OAuth 2.0 code-grant
// exchanger rather than the WorkOS authenticate adapter, which reads the
// verified email from a WorkOS-specific response shape no other IdP returns.
//
// provider is the configured IDENTITY_PROVIDER value; it is recorded on each
// identity as user_identities.provider. Two distinct enterprise IdPs avoid
// colliding in that namespace by selecting distinct IDENTITY_PROVIDER values
// (e.g. "okta", "ping"), each routed here. Using the same string the browser
// sends and the OAuth request policy enforces is required: authenticateWithCode
// rejects a login whose token provider disagrees with the request provider.
func buildGenericOIDCStack(provider string) (auth.TokenValidator, business.CodeExchanger, error) {
	// The generic stack validates a standard OIDC id_token (preferred over the
	// access token in authenticateWithCode), whose aud is the client id, so a
	// minimally-configured provider can default its audience binding to it.
	validator, tokenURL, clientID, clientSecret, err := discoverOIDCValidator(provider, true)
	if err != nil {
		return nil, nil, err
	}
	exchanger, err := oidc.NewExchanger(oidc.ExchangerConfig{
		TokenURL:     tokenURL,
		ClientID:     clientID,
		ClientSecret: clientSecret,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("initialize %s exchanger: %w", provider, err)
	}
	return validator, oidc.AsBusinessExchanger(exchanger), nil
}

// discoverOIDCValidator builds the JWKS validator and resolves the token
// endpoint shared by every discovery-driven provider. provider is the
// configured IDENTITY_PROVIDER value: it is written to Claims.Provider (and thus
// user_identities.provider) and named in error messages. It is deliberately the
// same string the browser sends and the OAuth request policy enforces, so the
// two can never disagree at login.
func discoverOIDCValidator(provider string, defaultAudienceToClientID bool) (validator auth.TokenValidator, tokenURL, clientID, clientSecret string, err error) {
	clientID = identityEnv("IDENTITY_CLIENT_ID")
	clientSecret = identityProviderSecret("IDENTITY_CLIENT_SECRET")
	issuer := identityEnv("IDENTITY_ISSUER")
	if !hasConfiguredValue(clientID) || !hasConfiguredValue(clientSecret) {
		return nil, "", "", "", fmt.Errorf(
			"identity provider %s requires IDENTITY_CLIENT_ID and IDENTITY_CLIENT_SECRET", provider)
	}
	if !hasConfiguredValue(issuer) {
		return nil, "", "", "", fmt.Errorf(
			"identity provider %s requires IDENTITY_ISSUER to discover provider metadata", provider)
	}

	// Start from any explicitly pinned endpoints; discovery fills only the gaps.
	expectedIssuer := issuer
	jwksURL := identityEnv("IDENTITY_JWKS_URL")
	tokenURL = identityEnv("IDENTITY_TOKEN_URL")
	if !hasConfiguredValue(jwksURL) || !hasConfiguredValue(tokenURL) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		discovered, err := oidc.Discover(ctx, issuer, nil)
		if err != nil {
			return nil, "", "", "", fmt.Errorf("discover %s provider metadata: %w", provider, err)
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

	// Standard OIDC binds an id_token to its relying party through `aud` ==
	// client id. A minimally-configured provider supplies neither an explicit
	// IDENTITY_AUDIENCE nor a non-standard IDENTITY_CLIENT_ID_CLAIM, so default
	// the audience binding to the client id: without it oidc.New fails closed
	// ("an audience binding is required"), and demanding the non-standard
	// client_id claim rejects id_tokens (e.g. WorkOS AuthKit) that never carry it.
	// Only the generic id_token path opts in — WorkOS validates an access token
	// whose aud is not the client id, so it keeps requiring an explicit binding.
	if defaultAudienceToClientID && !hasConfiguredValue(cfg.Audience) && !hasConfiguredValue(cfg.ClientIDClaim) {
		cfg.Audience = clientID
	}

	v, err := oidc.New(cfg)
	if err != nil {
		return nil, "", "", "", fmt.Errorf("initialize %s validator: %w", provider, err)
	}

	if strings.TrimSpace(tokenURL) == "" {
		return nil, "", "", "", fmt.Errorf(
			"identity provider %s published no token endpoint; set IDENTITY_TOKEN_URL", provider)
	}
	return v, tokenURL, clientID, clientSecret, nil
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

	case "oidc":
		return buildGenericOIDCStack(provider)

	case "auth0":
		domain := identityEnv("IDENTITY_DOMAIN")
		audience := identityEnv("IDENTITY_AUDIENCE")
		clientID := identityEnv("IDENTITY_CLIENT_ID")
		clientSecret := identityProviderSecret("IDENTITY_CLIENT_SECRET")
		if !hasConfiguredValue(domain) || !hasConfiguredValue(clientID) || !hasConfiguredValue(clientSecret) || !hasConfiguredValue(audience) {
			return nil, nil, fmt.Errorf("identity provider auth0 requires IDENTITY_DOMAIN, IDENTITY_AUDIENCE, IDENTITY_CLIENT_ID, and IDENTITY_CLIENT_SECRET")
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
		clientSecret := identityProviderSecret("IDENTITY_CLIENT_SECRET")
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
		// A non-preset name is a generic OpenID Connect provider only when the
		// operator explicitly declares it one. Without that opt-in an unrecognized
		// value fails startup closed — so a typo of a preset (e.g. "wrokos") is
		// rejected here instead of silently building a generic stack that would
		// mismatch the intended provider's token shape and fail at first login.
		if identityEnv("IDENTITY_GENERIC_OIDC") != "true" {
			return nil, nil, fmt.Errorf(
				"unsupported identity provider %q; set IDENTITY_GENERIC_OIDC=true to configure it as a generic OpenID Connect provider", provider)
		}
		return buildGenericOIDCStack(provider)
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

// emailConfiguration is what the email group resolves to: the sender address
// and the provider, or a nil sender when the deployment sends no email.
type emailConfiguration struct {
	from   string
	sender email.Sender
}

// emailEnv is Codefly-only, like identityEnv: the provider, the sender address
// and the provider's secrets all come from the `email` workspace configuration
// group. A raw process variable is not a second authority — reading
// EMAIL_PROVIDER from the process environment is what let a deployed cell that
// configured the group fall back to the log sink without a word.
func emailEnv(key string) string {
	value, _ := codefly.For(codefly.Context()).WorkspaceValue("email", key)
	return strings.TrimSpace(value)
}

// configuredEmail makes provider selection explicit and refuses every
// configuration that would silently not deliver. Outside the local environment
// an unset provider and the log sink are both refused: the log sink reports
// success, prints each message — accept tokens included — to the service log,
// and leaves invitations "queued" forever. A deployment that really sends no
// email says so with EMAIL_PROVIDER=disabled. Each factory validates its own
// secrets and fails closed; adding a provider is one Register call.
func configuredEmail(ctx context.Context, isLocal bool) (emailConfiguration, error) {
	if applicationEnv("EMAIL_FROM") != "" {
		return emailConfiguration{}, fmt.Errorf("email: EMAIL_FROM is set in the application configuration group; it belongs to the email group")
	}
	provider := strings.ToLower(emailEnv("EMAIL_PROVIDER"))
	switch {
	case provider == "" && isLocal:
		provider = "log"
	case provider == "":
		return emailConfiguration{}, fmt.Errorf("email: EMAIL_PROVIDER is required outside the local environment (resend, or disabled to run without email)")
	case provider == "log" && !isLocal:
		return emailConfiguration{}, fmt.Errorf("email: EMAIL_PROVIDER=log only prints messages and is refused outside the local environment (use resend, or disabled to run without email)")
	}
	from := emailEnv("EMAIL_FROM")
	if from == "" {
		if provider == "resend" && !isLocal {
			return emailConfiguration{}, fmt.Errorf("email: EMAIL_FROM is required when EMAIL_PROVIDER=resend outside the local environment")
		}
		from = "no-reply@localhost"
	}

	registry := email.NewRegistry()
	registry.Register("disabled", disabledEmailFactory)
	registry.Register("log", logEmailFactory)
	registry.Register("resend", resendEmailFactory)
	sender, err := registry.Select(ctx, provider)
	if err != nil {
		return emailConfiguration{}, err
	}
	return emailConfiguration{from: from, sender: sender}, nil
}

func resendCredentialsPresent() bool {
	return emailEnv("RESEND_API_KEY") != "" || emailEnv("RESEND_WEBHOOK_SECRET") != ""
}

// disabledEmailFactory selects no sender. It is a registered provider rather
// than an absence so that "this deployment sends no email" is a decision
// someone wrote down, not a default nobody noticed.
func disabledEmailFactory(context.Context) (email.Sender, error) {
	if resendCredentialsPresent() {
		return nil, fmt.Errorf("email: Resend credentials are present while EMAIL_PROVIDER is disabled")
	}
	return nil, nil
}

func logEmailFactory(ctx context.Context) (email.Sender, error) {
	if resendCredentialsPresent() {
		return nil, fmt.Errorf("email: Resend credentials are present while EMAIL_PROVIDER is log")
	}
	w := wool.Get(ctx).In("pickEmailSender")
	return email.NewLogSender(func(format string, args ...any) {
		w.Info(fmt.Sprintf(format, args...))
	}), nil
}

func resendEmailFactory(_ context.Context) (email.Sender, error) {
	key := emailEnv("RESEND_API_KEY")
	webhookSecret := emailEnv("RESEND_WEBHOOK_SECRET")
	if key == "" || webhookSecret == "" {
		return nil, fmt.Errorf("email: RESEND_API_KEY and RESEND_WEBHOOK_SECRET are required when EMAIL_PROVIDER=resend")
	}
	return email.NewResendSender(email.ResendConfig{
		APIKey:        key,
		BaseURL:       emailEnv("RESEND_API_BASE"),
		WebhookSecret: webhookSecret,
	})
}

// previousSigningKeys returns any rotated-out signing keys the minter should
// still accept for verification, so access tokens signed by them keep verifying
// during an overlapping rotation window. JWT_PREVIOUS_PUBLIC_KEYS is a
// comma-separated list of base64 Ed25519 public keys; empty (the steady state)
// yields none. A malformed entry is logged and skipped rather than failing
// startup.
func previousSigningKeys(ctx context.Context) []ed25519core.PublicKey {
	var keys []ed25519core.PublicKey
	for _, encoded := range strings.Split(workspaceEnv("internal-auth", "JWT_PREVIOUS_PUBLIC_KEYS"), ",") {
		encoded = strings.TrimSpace(encoded)
		if encoded == "" {
			continue
		}
		pub, err := decodeEd25519PublicKey(encoded)
		if err != nil {
			wool.Get(ctx).In("previousSigningKeys").Warn("ignoring malformed previous signing key", wool.ErrField(err))
			continue
		}
		keys = append(keys, pub)
	}
	return keys
}

// decodeEd25519PublicKey accepts a standard or raw-url base64 Ed25519 public
// key. Operators paste keys in either form; both decode to the same 32 bytes.
func decodeEd25519PublicKey(encoded string) (ed25519core.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		raw, err = base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("decode public key: %w", err)
		}
	}
	if len(raw) != ed25519core.PublicKeySize {
		return nil, fmt.Errorf("wrong public key size: got %d want %d", len(raw), ed25519core.PublicKeySize)
	}
	return ed25519core.PublicKey(raw), nil
}

// devFixtureAuthProvider reports whether authProvider is one of the local
// fixture-backed modes. Only these may fall back to an ephemeral signing key;
// with any real identity provider the key must load from Vault.
func devFixtureAuthProvider(authProvider string) bool {
	return authProvider == "fixture" || authProvider == "dev"
}

// requireLocalForDevFixtureProvider refuses to start with the dev or fixture
// identity provider outside the local environment. Those providers accept
// unauthenticated identities by design; selecting one in a deployed environment
// (via IDENTITY_PROVIDER=dev/fixture) would turn the whole authentication
// boundary off, so it fails closed at startup instead.
func requireLocalForDevFixtureProvider(authProvider string, isLocal bool) error {
	if devFixtureAuthProvider(authProvider) && !isLocal {
		return fmt.Errorf("identity provider %q is only permitted in the local environment", authProvider)
	}
	return nil
}

// loadSigningKey returns the host's Ed25519 access-token signing key, from
// whichever backend the `key-service` group selected for it.
//
// The key must persist across restarts and be identical across replicas: it
// signs JWTs, seeds the OAuth-state signer, and is the public key the gateway's
// ext_authz check and permissions plugin pin. A deployed environment refuses to
// boot if the load fails — an ephemeral key would make each replica sign
// differently, break existing sessions, and desynchronise the pinned key. This
// fails closed rather than fail-open-to-broken.
//
// On the `vault` backend the key's custody is the cell's, never accounts': the
// platform's identity-seeding command writes the keypair to Vault once,
// create-only, from the cell's durable seed, so a re-seed restores the *same*
// keypair and the `kid` the gateway pinned does not move
// (module/KEY_ROTATION.md, "Custody of the signing key"). accounts therefore
// never generates one outside the local environment: a self-minted key would
// silently diverge from that seed, invalidating every live session and leaving
// two cells signing differently.
//
// The refusal quotes VAULT_KEY_CUSTODY from the `vault` configuration group
// when the cell sets it, so the operator reading a crash loop sees their own
// seeding command rather than a sentence about one. This module never names
// that command itself: a module names nothing above it.
//
// The `kms` backend cannot hold this key at all, and keyservice.Selection
// refuses it there by name: its material never leaves the service, while the
// Work Context signer, the delegation minter and the OAuth state signer are each
// handed the key itself. So there is no custody command to name on that backend
// because the backend is not reachable for this key — see ReadVaultSigningKey.
//
// allowEphemeral is set only in dev/fixture mode, where a freshly generated key
// lets `codefly run service frontend --fixture dev-admin` work on a machine with
// no Vault. The fallback logs a warning. isLocal is checked here as well as at
// the provider gate: the two conditions are enforced in different functions, and
// an argument that spans two functions is one a later edit can quietly break.
func loadSigningKey(
	ctx context.Context,
	keys *keyservice.Binding,
	allowEphemeral, isLocal bool,
) (ed25519core.PrivateKey, error) {
	ephemeral := allowEphemeral && isLocal
	key, err := keys.SigningKey(ctx, ephemeral)
	switch {
	case err == nil:
		return key, nil
	case errors.Is(err, keyservice.ErrNoSigningKey):
		// The cause travels with the sentinel, so a local developer whose Vault
		// is misconfigured is told WHY it did not answer rather than being left
		// to guess from "no signing key is bound".
		wool.Get(ctx).In("loadSigningKey").Warn(
			"could not load the signing key — falling back to ephemeral", wool.ErrField(err))
		return keyservice.GenerateSigningKey()
	case keys.SigningBackend == keyservice.BackendVault:
		return nil, fmt.Errorf("%w — seed it from the cell's durable identity seed%s; accounts never mints its own outside the local environment", err, keyCustodyHint())
	default:
		return nil, err
	}
}

// requireStartupConfiguration refuses to start on configuration that cannot
// work, before the process acquires anything. It is the one place a boot-time
// configuration requirement is registered, so each is enforced at a moment a
// deployment can still be fixed rather than discovered from a crash loop — and
// so a test can reach all of them with nothing running.
func requireStartupConfiguration(ctx context.Context, isLocal bool) error {
	selection, err := keyservice.Selection(ctx, isLocal)
	if err != nil {
		return err
	}
	if err := requireKeyCustody(selection.SigningBackend, isLocal); err != nil {
		return err
	}
	if err := requirePerimeterCredentials(isLocal); err != nil {
		return err
	}
	return requireApplicationBaseURL(isLocal)
}

// requireApplicationBaseURL refuses to start a deployed runtime without the
// origin it treats as its own.
//
// It is the value that binds an OAuth redirect, an authenticator's relying party
// and every emailed link. Unset, each of those was bound to whatever origin the
// request's trusted hop forwarded — which that hop derived from the caller's own
// forwarding header — so a caller chose the host a sign-in returned to and the host
// an emailed link pointed at. A cell without it is a cell whose verified origin is
// a request parameter, so it refuses here, where a deployment can still be fixed,
// rather than serving a sign-in bound to somewhere else.
// usableDatasourceKey resolves one datasource message-authentication key, or nil
// when a deployed runtime may not use what is configured.
//
// The shipped local defaults for this group carry placeholder markers, exactly as
// every other secret group's do, and this module's repository is public. A deployed
// runtime holding one would be signing with a published value, so it is treated as
// ABSENT rather than refused at boot: unlike a perimeter credential, a datasource
// key is not required for the service to serve, and the purpose it signs for
// already has a defined unavailable state. Refusing to start would turn an
// unprovisioned optional purpose into an outage.
//
// Local development uses the shipped default, which is its intended value.
func usableDatasourceKey(key string, isLocal bool) []byte {
	value := strings.TrimSpace(workspaceEnv("datasource-keys", key))
	if value == "" {
		return nil
	}
	if isLocal {
		return []byte(value)
	}
	if looksLikeShippedPlaceholder(value) {
		wool.Get(context.Background()).In("usableDatasourceKey").Warn(
			"a datasource key carries a placeholder this module ships in its public local defaults; "+
				"the purpose it signs for is unavailable until the key is provisioned for this cell",
			wool.Field("key", "datasource-keys/"+key))
		return nil
	}
	if len(value) < minimumPerimeterCredentialLength {
		wool.Get(context.Background()).In("usableDatasourceKey").Warn(
			"a datasource key is shorter than the minimum for a signing key; "+
				"the purpose it signs for is unavailable until it is replaced",
			wool.Field("key", "datasource-keys/"+key),
			wool.Field("minimum", minimumPerimeterCredentialLength))
		return nil
	}
	return []byte(value)
}

func requireApplicationBaseURL(isLocal bool) error {
	if isLocal {
		return nil
	}
	configured, err := configuredApplicationBaseURL()
	if err != nil {
		return err
	}
	if configured == "" {
		return fmt.Errorf("application: APP_BASE_URL is required outside the local environment: " +
			"set it in the `application` configuration group to this cell's public origin, which is " +
			"what binds an OAuth redirect, the authenticator relying-party origin and every emailed link")
	}
	return nil
}

// requireKeyCustody refuses to start outside the local environment without the
// cell's seeding command.
//
// It is required rather than optional because of how the October 2026 incident
// actually went: an operator met `load signing key from Vault: vault http 404`,
// nothing on the failure path said where the key was supposed to come from, and
// they generated a fresh keypair by hand — which invalidated every live session
// and left the durable seed and Vault disagreeing. A diagnostic that is present
// only when somebody remembered to configure it is absent exactly when the
// incident happens, so the configuration is checked at boot, when a deployment
// can still be fixed, rather than discovered during one.
//
// This module cannot name the command itself: it names nothing above it, and
// the verb belongs to the cell. So the composition supplies it and this only
// insists that it did.
func requireKeyCustody(signingBackend keyservice.Backend, isLocal bool) error {
	// Only the `vault` backend has a seeding command to name. A Cloud KMS key
	// version is provisioned as cloud resources and read by name, so there is
	// no key for an operator to write and nothing for this diagnostic to quote;
	// insisting on it there would be a configuration requirement that cannot be
	// satisfied.
	if isLocal || signingBackend != keyservice.BackendVault {
		return nil
	}
	if strings.TrimSpace(workspaceEnv("vault", "VAULT_KEY_CUSTODY")) != "" {
		return nil
	}
	return fmt.Errorf("vault: VAULT_KEY_CUSTODY is required outside the local environment: set it in the `vault` configuration group to the command this cell uses to seed secret/data/jwt-signing-key, so an operator meeting a missing-key refusal is told what to run rather than left to mint a key by hand (module/KEY_ROTATION.md, \"Custody of the signing key\")")
}

// keyCustodyHint renders the cell's own seeding command into the refusal, from
// VAULT_KEY_CUSTODY in the `vault` configuration group. It is a free-text
// operator note, so it is bounded and stripped of newlines before it reaches a
// log line: an unbounded value from configuration would otherwise let whoever
// writes the group forge log entries around the refusal.
//
// The empty branch is reachable only in the local environment, where
// requireKeyCustody does not insist on the key: a local run has no cell and no
// seeding command.
func keyCustodyHint() string {
	custody := strings.TrimSpace(workspaceEnv("vault", "VAULT_KEY_CUSTODY"))
	if custody == "" {
		return " (module/KEY_ROTATION.md, \"Custody of the signing key\"; the cell's runbook names the command, and VAULT_KEY_CUSTODY in the `vault` configuration group puts it in this message)"
	}
	custody = strings.Join(strings.Fields(custody), " ")
	if len(custody) > 200 {
		custody = custody[:200] + "…"
	}
	return " — " + custody
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

func configureModuleIdentity(service *business.Service) error {
	secrets, err := business.ParseRegistrationSecrets(workspaceEnv("federation", "MODULE_IDENTITY_SECRETS"))
	if err != nil {
		return fmt.Errorf("read module identity secrets: %w", err)
	}
	service.SetModuleIdentitySecrets(secrets)
	return nil
}

// registerMembershipCacheHealth makes a store-only membership cache visible
// beyond the boot log: a degraded `membership-cache` component on /v1/status
// (still 200 — the replica serves, from the store) and the
// saas.accounts.membership_cache.store_only gauge (1 while store-only).
func registerMembershipCacheHealth(c *membership.Cache) {
	adapters.RegisterStatusProbe(adapters.StatusProbe{
		Name:  "membership-cache",
		Check: func(context.Context) error { return c.StoreOnly() },
	})
	gauge, err := otel.Meter("github.com/codefly-dev/module-saas-starter/accounts").Int64ObservableGauge(
		"saas.accounts.membership_cache.store_only",
		otelmetric.WithDescription("1 while org membership reads bypass the cache and go to the store because Redis was unreachable at boot."),
	)
	if err != nil {
		return
	}
	_, _ = otel.Meter("github.com/codefly-dev/module-saas-starter/accounts").RegisterCallback(
		func(_ context.Context, o otelmetric.Observer) error {
			var storeOnly int64
			if c.StoreOnly() != nil {
				storeOnly = 1
			}
			o.ObserveInt64(gauge, storeOnly)
			return nil
		}, gauge)
}
