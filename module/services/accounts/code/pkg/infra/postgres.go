package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	database, closeDatabase, err := scopedpostgres.Open(
		ctx,
		readOnlyConnection,
		readWriteConnection,
		postgresAuthenticator{},
		scopedpostgres.WithScopeSettings("app.current_org_id", "app.current_user_id"),
		scopedpostgres.WithOperationTimeout(5*time.Second),
	)
	if err != nil {
		return nil, w.Wrapf(err, "failed to open authenticated Postgres boundary")
	}
	store, err := NewPostgresStoreFromURL(ctx, readWriteConnection)
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

// NewPostgresStoreFromURL creates a legacy store from one explicit connection
// URL for local tools and integration tests. Production must use
// NewPostgresStore, which requires service-postgres's distinct reader/writer
// capabilities and verified identity boundary.
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

	poolConfig.BeforeAcquire = func(ctx context.Context, conn *pgx.Conn) bool {
		// SET ROLE app_tenant — persists for the life of the user's
		// hold on this connection. Tx-scoped role assumptions inside
		// WithControlPlane layer on top and revert on
		// commit/rollback automatically.
		if _, err := conn.Exec(ctx, "SET ROLE app_tenant"); err != nil {
			wool.Get(ctx).In("BeforeAcquire").Debug("SET ROLE app_tenant failed", wool.ErrField(err))
			return false // reject this connection
		}
		return true
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
	defer tx.Rollback(ctx)

	// Create a new context with the transaction
	txCtx := context.WithValue(ctx, "tx", tx)

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
		ctx = context.WithValue(ctx, "tx", tx)
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
		ctx = context.WithValue(ctx, "tx", tx)
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
