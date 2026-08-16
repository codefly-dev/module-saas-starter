package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"accounts/pkg/auth"

	codefly "github.com/codefly-dev/sdk-go"
	scopedpostgres "github.com/codefly-dev/service-postgres/libs/go"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/wool"

	"github.com/jackc/pgx/v5/pgxpool"

	"accounts/pkg/business"
)

type Close func()

type PostgresStore struct {
	Close
	pool     *pgxpool.Pool
	database *scopedpostgres.Factory
}

// beforeConnectHook resolves a fresh password for every pool connection attempt.
// It is the pgxpool.Config.BeforeConnect signature, aliased so one hook wires
// identically into the legacy pool and the reader/writer capability pools.
type beforeConnectHook = func(context.Context, *pgx.ConnConfig) error

const scopedBoundaryOperationTimeout = 5 * time.Second

func NewPostgresStore(ctx context.Context) (*PostgresStore, error) {
	w := wool.Get(ctx).In("NewPostgresStore")
	readOnlyConnection, err := codefly.For(ctx).Service("store").Secret("postgres", "read-only-connection")
	if err != nil {
		return nil, w.Wrapf(err, "failed to get read-only connection string")
	}
	readWriteConnection, err := codefly.For(ctx).Service("store").Secret("postgres", "read-write-connection")
	if err != nil {
		return nil, w.Wrapf(err, "failed to get read-write connection string")
	}

	// One token-resolution path for every pool. In external-identity mode the
	// database password is a rotating token; the reader, writer, and legacy
	// pools all attach this same hook so a reconnect after token expiry presents
	// a current one. nil in local password mode — every pool keeps the password
	// embedded in its connection URL.
	hook := tokenFileBeforeConnect()

	database, closeDatabase, err := openScopedBoundary(ctx, readOnlyConnection, readWriteConnection, hook)
	if err != nil {
		return nil, w.Wrapf(err, "failed to open authenticated Postgres boundary")
	}

	legacyConfig, err := configureConnection(readWriteConnection, hook)
	if err != nil {
		closeDatabase()
		return nil, w.Wrapf(err, "failed to parse read-write connection string")
	}
	store, err := newPostgresStoreFromConfig(ctx, legacyConfig)
	if err != nil {
		closeDatabase()
		return nil, err
	}
	closeLegacy := store.Close
	store.Close = func() {
		closeLegacy()
		closeDatabase()
	}
	store.database = database
	return store, nil
}

// openScopedBoundary builds the authenticated reader/writer capability boundary.
// It mirrors service-postgres's Open — distinct non-owner reader/writer roles,
// startup ping, single-shot closer — but attaches the token-rotation
// BeforeConnect hook to both capability pools, which Open (v0.0.107) cannot do:
// its pools are built internally and it exposes no connection hook. Without this
// the RLS read/write path would keep authenticating with the token captured at
// startup and fail once it expired. Revert to scopedpostgres.Open once the
// library accepts a BeforeConnect (service-postgres#55).
func openScopedBoundary(ctx context.Context, readOnlyConnection, readWriteConnection string, hook beforeConnectHook) (*scopedpostgres.Factory, func(), error) {
	ctx, cancel := context.WithTimeout(ctx, scopedBoundaryOperationTimeout)
	defer cancel()

	readerConfig, err := configureConnection(readOnlyConnection, hook)
	if err != nil {
		return nil, nil, fmt.Errorf("parse read-only Postgres capability: %w", err)
	}
	writerConfig, err := configureConnection(readWriteConnection, hook)
	if err != nil {
		return nil, nil, fmt.Errorf("parse read-write Postgres capability: %w", err)
	}
	// Distinct non-owner reader/writer roles are the physical separation the RLS
	// boundary depends on; keep the check service-postgres's Open enforced.
	readerUser := strings.TrimSpace(readerConfig.ConnConfig.User)
	writerUser := strings.TrimSpace(writerConfig.ConnConfig.User)
	if readerUser == "" || writerUser == "" || readerUser == writerUser {
		return nil, nil, errors.New("read-only and read-write Postgres capabilities must use distinct database roles")
	}

	readerPool, err := openCapabilityPool(ctx, "read-only", readerConfig)
	if err != nil {
		return nil, nil, err
	}
	writerPool, err := openCapabilityPool(ctx, "read-write", writerConfig)
	if err != nil {
		readerPool.Close()
		return nil, nil, err
	}
	closePools := func() {
		readerPool.Close()
		writerPool.Close()
	}
	factory, err := scopedpostgres.NewFactory(
		readerPool,
		writerPool,
		postgresAuthenticator{},
		scopedpostgres.WithScopeSettings("app.current_org_id", "app.current_user_id"),
		scopedpostgres.WithOperationTimeout(scopedBoundaryOperationTimeout),
	)
	if err != nil {
		closePools()
		return nil, nil, err
	}
	var once sync.Once
	return factory, func() { once.Do(closePools) }, nil
}

// openCapabilityPool opens one capability pool and fails fast if it cannot reach
// Postgres, so a bad credential or unreachable server surfaces at startup rather
// than on the first query. The caller owns closing every pool opened before a
// later one fails.
func openCapabilityPool(ctx context.Context, label string, config *pgxpool.Config) (*pgxpool.Pool, error) {
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open %s Postgres capability: %w", label, err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping %s Postgres capability: %w", label, err)
	}
	return pool, nil
}

// configureConnection parses a Codefly connection secret into a pool config and
// attaches the token-rotation hook. It is the single place a pool's password
// source is wired, so every pool — legacy, reader, writer — resolves the token
// identically. A nil hook leaves the URL-embedded password in force.
func configureConnection(connectionURL string, hook beforeConnectHook) (*pgxpool.Config, error) {
	config, err := pgxpool.ParseConfig(connectionURL)
	if err != nil {
		return nil, err
	}
	config.BeforeConnect = hook
	return config, nil
}

// NewPostgresStoreFromURL creates a legacy store from one explicit connection
// URL for local tools and integration tests. Production must use
// NewPostgresStore, which requires service-postgres's distinct reader/writer
// capabilities and verified identity boundary.
//
// This path intentionally does NOT attach the token-rotation hook: callers pass
// a fully specified URL (e.g. an operator's admin credential to
// role-catalog-import) and must not have that credential silently replaced by a
// process-wide token file. Token rotation is wired in NewPostgresStore, which
// owns the production pools.
//
// Connection-level role selection: Codefly's Postgres service exports a
// non-owner read-write principal whose explicit application-role memberships
// come from runtime-read-write-roles. We install a BeforeAcquire hook that
// SET ROLEs every checked-out
// connection to `app_tenant` (a non-superuser, non-BYPASSRLS role
// created by migration 24). After this:
//
//   - Default queries on the connection run as app_tenant. RLS fires.
//     Un-wrapped Store calls on per-tenant tables return zero rows
//     when no app.current_org_id is set — fail-closed.
//   - WithOrgTx adds `SET LOCAL app.current_org_id = $orgID` inside
//     a tx so the policy filters to the right tenant.
//   - WithControlPlane assumes the named app_control_plane role for
//     cross-tenant work. The session principal must have explicit membership.
//
// AfterRelease is the safety net: when the connection returns to the
// pool we run RESET ROLE in case any caller somehow left the role
// in an unexpected state. BeforeAcquire re-applies SET ROLE on the
// next checkout.
//
// If `app_tenant` doesn't exist (e.g., migration 24 hasn't run yet
// because we're calling this from a tooling path), the SET ROLE in
// BeforeAcquire fails and the checkout returns an error. That's the
// loud-failure mode we want — silent fallback to superuser would
// re-introduce the gap.
func NewPostgresStoreFromURL(ctx context.Context, connectionURL string) (*PostgresStore, error) {
	w := wool.Get(ctx).In("NewPostgresStore")

	poolConfig, err := pgxpool.ParseConfig(connectionURL)
	if err != nil {
		return nil, w.Wrapf(err, "failed to parse connection string")
	}
	return newPostgresStoreFromConfig(ctx, poolConfig)
}

// newPostgresStoreFromConfig installs the app_tenant role hooks on an
// already-parsed pool config and opens the pool. The caller owns whether a
// token-rotation BeforeConnect hook is attached.
func newPostgresStoreFromConfig(ctx context.Context, poolConfig *pgxpool.Config) (*PostgresStore, error) {
	w := wool.Get(ctx).In("NewPostgresStore")

	poolConfig.PrepareConn = func(ctx context.Context, conn *pgx.Conn) (bool, error) {
		// SET ROLE app_tenant — persists for the life of the user's
		// hold on this connection. Tx-scoped role assumptions inside
		// WithControlPlane layer on top and revert on
		// commit/rollback automatically.
		if _, err := conn.Exec(ctx, "SET ROLE app_tenant"); err != nil {
			wool.Get(ctx).In("PrepareConn").Debug("SET ROLE app_tenant failed", wool.ErrField(err))
			return false, nil // destroy this connection; the query retries on a fresh one
		}
		return true, nil
	}
	poolConfig.AfterRelease = func(conn *pgx.Conn) bool {
		// Best-effort RESET ROLE before returning to pool, in case a
		// caller left the role in an odd state. Failure here just
		// drops the connection — pool will re-create.
		if _, err := conn.Exec(context.Background(), "RESET ROLE"); err != nil {
			return false
		}
		return true
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, w.Wrapf(err, "failed to connect to database")
	}
	return &PostgresStore{
		Close: pool.Close,
		pool:  pool,
	}, nil
}

// databaseTokenFileEnv names the file an external-identity sidecar rewrites with
// a fresh database access token (e.g. an Entra oss-rdbms token) on a rotation
// interval shorter than the token's ~1h lifetime. External-identity deployments
// set it; local password deployments leave it unset.
const databaseTokenFileEnv = "POSTGRES_TOKEN_FILE"

// tokenFileBeforeConnect returns a pgx BeforeConnect hook that re-reads the
// rotating token file for every connection attempt, so a pool reconnect after
// the previous token expired presents a current one instead of the stale
// password captured when the pool was built. It returns nil when no token file
// is configured, leaving the password embedded in the connection URL in force —
// local password mode and external-identity mode share this single path, gated
// only by the environment.
func tokenFileBeforeConnect() beforeConnectHook {
	path := strings.TrimSpace(os.Getenv(databaseTokenFileEnv))
	if path == "" {
		return nil
	}
	return func(ctx context.Context, connConfig *pgx.ConnConfig) error {
		token, err := readTokenFile(ctx, path)
		if err != nil {
			return err
		}
		connConfig.Password = token
		return nil
	}
}

// readTokenFile reads the whole rotating token file, bounded by ctx so a stalled
// token volume cannot block pool connection acquisition past the caller's
// deadline. The sidecar contract (infra-base #59) is to publish each new token
// with an atomic rename; without it a read could observe a partial write, which
// this cannot detect and which would surface as an authentication failure.
func readTokenFile(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	type result struct {
		token string
		err   error
	}
	// Buffered so the reader goroutine can always send and exit even if we have
	// already returned on ctx.Done — no goroutine leak except an unkillable
	// syscall on a wedged filesystem, which no user-space code can bound.
	done := make(chan result, 1)
	go func() {
		raw, err := os.ReadFile(path)
		if err != nil {
			done <- result{err: fmt.Errorf("read database token file %q: %w", path, err)}
			return
		}
		token := strings.TrimSpace(string(raw))
		if token == "" {
			done <- result{err: fmt.Errorf("database token file %q is empty", path)}
			return
		}
		done <- result{token: token}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-done:
		return r.token, r.err
	}
}

var _ business.Store = (*PostgresStore)(nil)

// Pool exposes the underlying pgxpool for callers that need direct access
// (e.g. the pkg/auth/pg package which implements its own interfaces over
// raw SQL). Prefer using the Store methods when possible.
func (s *PostgresStore) Pool() *pgxpool.Pool { return s.pool }

// ProviderRegistered reports whether an identity provider id exists in the
// identity_providers reference catalog. user_identities.provider is a foreign
// key into that catalog, so an unregistered provider cannot create identities;
// startup checks this to fail closed before the first login rather than after.
func (s *PostgresStore) ProviderRegistered(ctx context.Context, providerID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM identity_providers WHERE provider_id = $1)`,
		providerID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("query identity provider registration: %w", err)
	}
	return exists, nil
}

func (s *PostgresStore) RunInTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	// Begin transaction
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}

	// Defer a rollback in case anything fails
	defer func() { _ = tx.Rollback(ctx) }()

	// Create a new context with the transaction
	txCtx := context.WithValue(ctx, "tx", tx) //nolint:staticcheck // shared transaction context key

	// Run the provided function
	if err := fn(txCtx); err != nil {
		// If there's an error, rollback and return the error
		return err
	}

	// If everything succeeded, commit the transaction
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	return nil
}

type QueryExecutor interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ReadQueryExecutor is the repository surface shared by legacy transactions
// and service-postgres ReadTx. It intentionally cannot mutate.
type ReadQueryExecutor interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (s *PostgresStore) readAs(ctx context.Context, tenantID, userID string, fn func(context.Context, ReadQueryExecutor) error) error {
	if s.database == nil {
		return errors.New("authenticated Postgres boundary is unavailable")
	}
	if err := auth.RequireVerifiedDatabaseScope(ctx, tenantID, userID); err != nil {
		return err
	}
	verifiedTenantID, verifiedUserID, ok := auth.VerifiedDatabaseIdentity(ctx)
	if !ok {
		return auth.ErrVerifiedDatabaseIdentityRequired
	}
	// service-postgres supports opaque IDs and therefore compares exact strings.
	// Accounts accepts equivalent UUID spellings at its domain boundary, then
	// passes the canonical verified values into the generic capability checks.
	if err := s.database.RequireTenant(ctx, verifiedTenantID); err != nil {
		return err
	}
	if err := s.database.RequireUser(ctx, verifiedUserID); err != nil {
		return err
	}
	reader, err := s.database.Reader(ctx)
	if err != nil {
		return err
	}
	return reader.InTransaction(ctx, func(ctx context.Context, tx scopedpostgres.ReadTx) error {
		return fn(ctx, tx)
	})
}

func (s *PostgresStore) getQueryExecutor(ctx context.Context) QueryExecutor {
	tx, ok := ctx.Value("tx").(pgx.Tx)
	if ok {
		return tx
	}
	return s.pool
}

func (s *PostgresStore) GetUserByIdentity(ctx context.Context, identity *gen.UserIdentity) (*gen.User, error) {
	w := wool.Get(ctx).In("GetUserByIdentity")
	executor := s.getQueryExecutor(ctx)

	var user gen.User
	query := `
        SELECT u.uuid, u.primary_email, u.created_at, u.updated_at, u.last_login, 
               u.status, u.profile, u.email_verified
        FROM users u
        JOIN user_identities ui ON u.uuid = ui.user_uuid
        WHERE ui.provider = $1 AND ui.provider_id = $2`

	var (
		createdAt time.Time
		updatedAt time.Time
		lastLogin *time.Time
		profile   []byte // for JSONB
		status    string
	)

	err := executor.QueryRow(ctx, query, identity.Provider, identity.ProviderId).Scan(
		&user.Uuid,
		&user.PrimaryEmail,
		&createdAt,
		&updatedAt,
		&lastLogin,
		&status,
		&profile,
		&user.EmailVerified,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, w.Wrapf(err, "failed to scan user")
	}

	// Convert timestamps to protobuf
	user.CreatedAt = timestamppb.New(createdAt)
	user.UpdatedAt = timestamppb.New(updatedAt)
	if lastLogin != nil {
		user.LastLogin = timestamppb.New(*lastLogin)
	}

	// Parse status
	user.Status = parseUserStatus(status)

	// Parse profile JSONB
	if len(profile) > 0 {
		profileMap := make(map[string]string)
		if err := json.Unmarshal(profile, &profileMap); err != nil {
			return nil, w.Wrapf(err, "failed to unmarshal profile")
		}
		user.Profile = profileMap
	}

	return &user, nil
}
func (s *PostgresStore) RegisterUser(ctx context.Context, user *gen.User, identity *gen.UserIdentity) error {
	w := wool.Get(ctx).In("RegisterUser")

	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{
		IsoLevel: pgx.Serializable,
	}, func(tx pgx.Tx) error {
		// Registration is pre-auth (no user/org context yet) and writes
		// users + user_identities + the personal org — all RLS-protected.
		// Registration is an explicit control-plane capability because no
		// tenant or user scope exists until these rows have been created.
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+controlPlaneDatabaseRole); err != nil {
			return w.Wrapf(err, "assume control-plane role for registration")
		}
		ctx = context.WithValue(ctx, "tx", tx) //nolint:staticcheck // shared transaction context key
		executor := s.getQueryExecutor(ctx)

		// First check if this identity already exists
		var existingUserUUID string
		err := executor.QueryRow(ctx, `
            SELECT user_uuid 
            FROM user_identities 
            WHERE provider = $1 AND provider_id = $2`,
			identity.Provider,
			identity.ProviderId,
		).Scan(&existingUserUUID)

		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return w.Wrapf(err, "failed to check existing identity")
		}

		// If identity exists, return AlreadyExists error
		if existingUserUUID != "" {
			return status.Errorf(codes.AlreadyExists,
				"user already exists with provider %s and id %s",
				identity.Provider, identity.ProviderId)
		}

		// If it's a new identity, check if email is already registered
		var existingEmailUserUUID string
		err = executor.QueryRow(ctx, `
            SELECT uuid 
            FROM users 
            WHERE primary_email = $1`,
			user.PrimaryEmail,
		).Scan(&existingEmailUserUUID)

		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return w.Wrapf(err, "failed to check existing email")
		}

		// If email exists, might want to handle linking instead of error
		if existingEmailUserUUID != "" {
			return status.Errorf(codes.AlreadyExists,
				"email %s is already registered",
				user.PrimaryEmail)
		}

		// Create new user
		profileJSON, err := json.Marshal(user.Profile)
		if err != nil {
			return w.Wrapf(err, "failed to marshal profile")
		}

		_, err = executor.Exec(ctx, `
            INSERT INTO users (
                uuid, primary_email, created_at, updated_at, status,
                profile, email_verified
            ) VALUES (
                $1, $2, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, $3,
                $4, $5
            )`,
			user.Uuid,
			user.PrimaryEmail,
			userStatusToString(user.Status),
			profileJSON,
			identity.EmailVerified, // Use identity's email verification status
		)
		if err != nil {
			return w.Wrapf(err, "failed to insert user")
		}

		// Create the identity
		providerDataJSON, err := json.Marshal(identity.ProviderData)
		if err != nil {
			return w.Wrapf(err, "failed to marshal provider data")
		}

		_, err = executor.Exec(ctx, `
            INSERT INTO user_identities (
                uuid, user_uuid, provider, provider_id, provider_email,
                created_at, provider_data, email_verified
            ) VALUES (
                $1, $2, $3, $4, $5,
                CURRENT_TIMESTAMP, $6, $7
            )`,
			identity.Uuid,
			user.Uuid,
			identity.Provider,
			identity.ProviderId,
			identity.ProviderEmail,
			providerDataJSON,
			identity.EmailVerified,
		)
		if err != nil {
			return w.Wrapf(err, "failed to insert identity")
		}

		return nil
	})
}

func (s *PostgresStore) LinkIdentity(ctx context.Context, userUUID string, identity *gen.UserIdentity) error {
	w := wool.Get(ctx).In("LinkIdentity")

	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{
		IsoLevel: pgx.Serializable,
	}, func(tx pgx.Tx) error {
		ctx = context.WithValue(ctx, "tx", tx) //nolint:staticcheck // shared transaction context key
		executor := s.getQueryExecutor(ctx)

		// Check if identity already exists
		var existingUserUUID string
		err := executor.QueryRow(ctx, `
            SELECT user_uuid 
            FROM user_identities 
            WHERE provider = $1 AND provider_id = $2`,
			identity.Provider,
			identity.ProviderId,
		).Scan(&existingUserUUID)

		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return w.Wrapf(err, "failed to check existing identity")
		}

		if existingUserUUID != "" {
			return status.Errorf(codes.AlreadyExists,
				"identity already exists with provider %s and id %s",
				identity.Provider, identity.ProviderId)
		}

		// Create the new identity
		providerDataJSON, err := json.Marshal(identity.ProviderData)
		if err != nil {
			return w.Wrapf(err, "failed to marshal provider data")
		}

		_, err = executor.Exec(ctx, `
            INSERT INTO user_identities (
                uuid, user_uuid, provider, provider_id, provider_email,
                created_at, provider_data, email_verified
            ) VALUES (
                $1, $2, $3, $4, $5,
                CURRENT_TIMESTAMP, $6, $7
            )`,
			identity.Uuid,
			userUUID,
			identity.Provider,
			identity.ProviderId,
			identity.ProviderEmail,
			providerDataJSON,
			identity.EmailVerified,
		)
		if err != nil {
			return w.Wrapf(err, "failed to insert identity")
		}

		return nil
	})
}

func (s *PostgresStore) ClearAll(ctx context.Context) error {
	w := wool.Get(ctx).In("ClearAll")

	// Most tables here are RLS-protected (Phase 2B-2F). A bare DELETE
	// under app_tenant with no app.current_org_id set returns ZERO
	// rows — that's fail-closed for production but sabotages test
	// cleanup. Wrap in WithControlPlane so the deletes actually fire across
	// every tenant.
	//
	// Use DELETE instead of TRUNCATE to avoid CASCADE wiping roles
	// table. Errors are intentionally swallowed per-statement:
	// ClearAll runs in test cleanup against a possibly partial
	// schema, and we want best-effort even if some tables haven't
	// been migrated yet.
	return s.WithControlPlane(ctx, func(ctx context.Context) error {
		executor := s.getQueryExecutor(ctx)
		for _, stmt := range []string{
			// gdpr_requests.user_id intentionally has no ON DELETE CASCADE;
			// remove durable privacy jobs before their subjects.
			"DELETE FROM gdpr_requests",
			"DELETE FROM role_assignments",
			"DELETE FROM role_permissions WHERE role_id IN (SELECT id FROM roles WHERE NOT built_in)",
			"DELETE FROM roles WHERE NOT built_in",
			"DELETE FROM team_members",
			"DELETE FROM teams",
			"DELETE FROM organization_members",
			"DELETE FROM organizations",
			"DELETE FROM user_identities",
			"DELETE FROM users",
		} {
			if _, err := executor.Exec(ctx, stmt); err != nil {
				w.Debug("ClearAll statement failed (continuing)",
					wool.Field("stmt", stmt), wool.ErrField(err))
			}
		}
		return nil
	})
}

// Helper functions for status conversion
func parseUserStatus(status string) gen.UserStatus {
	switch status {
	case "active":
		return gen.UserStatus_USER_STATUS_ACTIVE
	case "inactive":
		return gen.UserStatus_USER_STATUS_INACTIVE
	case "suspended":
		return gen.UserStatus_USER_STATUS_SUSPENDED
	case "deleted":
		return gen.UserStatus_USER_STATUS_DELETED
	default:
		return gen.UserStatus_USER_STATUS_UNSPECIFIED
	}
}

func userStatusToString(status gen.UserStatus) string {
	switch status {
	case gen.UserStatus_USER_STATUS_ACTIVE:
		return "active"
	case gen.UserStatus_USER_STATUS_INACTIVE:
		return "inactive"
	case gen.UserStatus_USER_STATUS_SUSPENDED:
		return "suspended"
	case gen.UserStatus_USER_STATUS_DELETED:
		return "deleted"
	default:
		return "active" // Default to active for new users
	}
}
