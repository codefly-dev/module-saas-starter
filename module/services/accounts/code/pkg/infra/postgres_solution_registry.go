package infra

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

// solution_registrations is a control-plane-owned platform relation (no tenant
// column, no RLS): only app_control_plane may touch it. All four methods
// therefore assume the caller opened a WithControlPlane transaction, which
// getQueryExecutor picks up from ctx.

const solutionRegistrationColumns = `solution_id, publisher, revision, runtime_boundary, tombstoned_at,
	frontend_revision, frontend_manifest, frontend_contract_version, frontend_lease_expires_at,
	backend_revision, backend_upstream, backend_service_alias, backend_contract_version, backend_lease_expires_at,
	updated_at`

func scanSolutionRegistration(row pgx.Row) (*business.SolutionRegistration, error) {
	var (
		record          business.SolutionRegistration
		tombstonedAt    *time.Time
		frontRevision   *int64
		frontManifest   *string
		frontContract   *string
		frontLease      *time.Time
		backendRevision *int64
		backendUpstream *string
		backendAlias    *string
		backendContract *string
		backendLease    *time.Time
	)
	if err := row.Scan(
		&record.SolutionID, &record.Publisher, &record.Revision, &record.RuntimeBoundary, &tombstonedAt,
		&frontRevision, &frontManifest, &frontContract, &frontLease,
		&backendRevision, &backendUpstream, &backendAlias, &backendContract, &backendLease,
		&record.UpdatedAt,
	); err != nil {
		return nil, err
	}
	record.TombstonedAt = tombstonedAt
	// The half-whole CHECK constraints guarantee a half's columns are all set
	// or all null, so the revision column alone decides whether it is present.
	if frontRevision != nil {
		record.Frontend = &business.SolutionFrontendHalf{
			Revision:        *frontRevision,
			Manifest:        *frontManifest,
			ContractVersion: derefString(frontContract),
			LeaseExpiresAt:  *frontLease,
		}
	}
	if backendRevision != nil {
		record.Backend = &business.SolutionBackendHalf{
			Revision:        *backendRevision,
			Upstream:        *backendUpstream,
			ServiceAlias:    *backendAlias,
			ContractVersion: derefString(backendContract),
			LeaseExpiresAt:  *backendLease,
		}
	}
	return &record, nil
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// GetSolutionRegistrationForUpdate reads and row-locks one record, returning
// nil when the solution has never registered. The lock holds for the caller's
// transaction, which is what makes read-decide-write safe against a concurrent
// registration of the other half.
func (s *PostgresStore) GetSolutionRegistrationForUpdate(
	ctx context.Context, solutionID string,
) (*business.SolutionRegistration, error) {
	q := s.getQueryExecutor(ctx)
	record, err := scanSolutionRegistration(q.QueryRow(ctx,
		`SELECT `+solutionRegistrationColumns+`
		 FROM public.solution_registrations WHERE solution_id = $1 FOR UPDATE`, solutionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return record, nil
}

// NextSolutionRegistryRevision draws the next value of the registry-wide
// sequence. Sequence advances are not transactional, so a rolled-back write
// leaves a gap; revisions are compared, never counted.
func (s *PostgresStore) NextSolutionRegistryRevision(ctx context.Context) (int64, error) {
	var revision int64
	if err := s.getQueryExecutor(ctx).QueryRow(ctx,
		`SELECT nextval('public.solution_registry_revision_sequence')`).Scan(&revision); err != nil {
		return 0, err
	}
	return revision, nil
}

// SaveSolutionRegistration writes the whole record. Every half column is
// written on every save, so tombstoning — which passes a record with no halves
// — clears the endpoints in the same statement that records the deletion.
//
// runtime_boundary is the one column this statement will not write (issue
// #1015). It is absent from the INSERT, so a new row takes the column's
// gen_random_uuid() default, and absent from the ON CONFLICT ... DO UPDATE, so
// an existing row keeps what it was given — through a replacement half, a
// tombstone and a reactivation alike. RETURNING reports whichever of the two
// happened, and the caller's record is corrected from it: the database is the
// only thing that can answer what a solution's boundary is, and a value
// invented anywhere above here is discarded rather than stored.
func (s *PostgresStore) SaveSolutionRegistration(ctx context.Context, record *business.SolutionRegistration) error {
	var (
		frontRevision   *int64
		frontManifest   *string
		frontContract   *string
		frontLease      *time.Time
		backendRevision *int64
		backendUpstream *string
		backendAlias    *string
		backendContract *string
		backendLease    *time.Time
	)
	if half := record.Frontend; half != nil {
		frontRevision, frontManifest = &half.Revision, &half.Manifest
		frontContract, frontLease = &half.ContractVersion, &half.LeaseExpiresAt
	}
	if half := record.Backend; half != nil {
		backendRevision, backendUpstream = &half.Revision, &half.Upstream
		backendAlias, backendContract, backendLease = &half.ServiceAlias, &half.ContractVersion, &half.LeaseExpiresAt
	}
	var boundary string
	err := s.getQueryExecutor(ctx).QueryRow(ctx, `
		INSERT INTO public.solution_registrations (
			solution_id, publisher, revision, tombstoned_at,
			frontend_revision, frontend_manifest, frontend_contract_version, frontend_lease_expires_at,
			backend_revision, backend_upstream, backend_service_alias, backend_contract_version, backend_lease_expires_at,
			updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (solution_id) DO UPDATE SET
			publisher = EXCLUDED.publisher,
			revision = EXCLUDED.revision,
			tombstoned_at = EXCLUDED.tombstoned_at,
			frontend_revision = EXCLUDED.frontend_revision,
			frontend_manifest = EXCLUDED.frontend_manifest,
			frontend_contract_version = EXCLUDED.frontend_contract_version,
			frontend_lease_expires_at = EXCLUDED.frontend_lease_expires_at,
			backend_revision = EXCLUDED.backend_revision,
			backend_upstream = EXCLUDED.backend_upstream,
			backend_service_alias = EXCLUDED.backend_service_alias,
			backend_contract_version = EXCLUDED.backend_contract_version,
			backend_lease_expires_at = EXCLUDED.backend_lease_expires_at,
			updated_at = EXCLUDED.updated_at
		RETURNING runtime_boundary`,
		record.SolutionID, record.Publisher, record.Revision, record.TombstonedAt,
		frontRevision, frontManifest, frontContract, frontLease,
		backendRevision, backendUpstream, backendAlias, backendContract, backendLease,
		record.UpdatedAt).Scan(&boundary)
	if err != nil {
		return err
	}
	record.RuntimeBoundary = boundary
	return nil
}

// SolutionRuntimeBoundarySeed returns one registered solution's boundary seed,
// the publisher of record, and whether its backend half is currently serving.
//
// It opens its own control-plane transaction rather than assuming one: the Work
// Context issuer calls it on the mint path, which holds no registry transaction
// of its own. A tombstoned record answers ErrSolutionRegistrationTombstoned and
// a missing one ErrSolutionRegistrationNotFound — a deregistered solution must
// not keep minting under the boundary its runs are filed against, and the two
// cases send an operator to different places.
//
// "Serving" is the backend half alone, not the record's derived status: the
// backend is the half that mints, and a solution whose page has not registered
// yet is still entitled to a boundary for the calls its own backend makes.
func (s *PostgresStore) SolutionRuntimeBoundarySeed(
	ctx context.Context, solutionID string,
) (business.SolutionBoundarySeed, error) {
	var (
		seed         string
		publisher    string
		tombstonedAt *time.Time
		backendLease *time.Time
	)
	err := s.WithControlPlane(ctx, func(ctx context.Context) error {
		return s.getQueryExecutor(ctx).QueryRow(ctx,
			`SELECT runtime_boundary, publisher, tombstoned_at, backend_lease_expires_at
			 FROM public.solution_registrations WHERE solution_id = $1`, solutionID).
			Scan(&seed, &publisher, &tombstonedAt, &backendLease)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return business.SolutionBoundarySeed{}, business.ErrSolutionRegistrationNotFound
	}
	if err != nil {
		return business.SolutionBoundarySeed{}, err
	}
	if tombstonedAt != nil {
		return business.SolutionBoundarySeed{}, business.ErrSolutionRegistrationTombstoned
	}
	return business.SolutionBoundarySeed{
		Seed:           seed,
		Publisher:      publisher,
		BackendServing: backendLease != nil && backendLease.After(time.Now().UTC()),
	}, nil
}

// SolutionRuntimeBoundarySeeds returns every stored seed, tombstones included.
// It is the input to the mint's collision check, which refuses a caller-named
// task_id that is any solution's boundary; a removed solution's runs may still
// be executing, so its seed still has to be protected.
func (s *PostgresStore) SolutionRuntimeBoundarySeeds(ctx context.Context) ([]string, error) {
	var seeds []string
	err := s.WithControlPlane(ctx, func(ctx context.Context) error {
		rows, err := s.getQueryExecutor(ctx).Query(ctx,
			`SELECT runtime_boundary FROM public.solution_registrations`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var seed string
			if err := rows.Scan(&seed); err != nil {
				return err
			}
			seeds = append(seeds, seed)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return seeds, nil
}

// ListSolutionRegistrations returns the registry snapshot ordered by solution
// id, plus the highest revision in the whole registry. The revision is computed
// over every row including tombstones, so a deregistration still advances what
// consumers converge on.
func (s *PostgresStore) ListSolutionRegistrations(
	ctx context.Context, includeTombstoned bool,
) ([]*business.SolutionRegistration, int64, error) {
	q := s.getQueryExecutor(ctx)
	rows, err := q.Query(ctx,
		`SELECT `+solutionRegistrationColumns+`
		 FROM public.solution_registrations
		 WHERE $1 OR tombstoned_at IS NULL
		 ORDER BY solution_id`, includeTombstoned)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	records := []*business.SolutionRegistration{}
	for rows.Next() {
		record, err := scanSolutionRegistration(rows)
		if err != nil {
			return nil, 0, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var registryRevision int64
	if err := q.QueryRow(ctx,
		`SELECT COALESCE(MAX(revision), 0) FROM public.solution_registrations`).Scan(&registryRevision); err != nil {
		return nil, 0, err
	}
	return records, registryRevision, nil
}

// ForceSolutionRuntimeBoundarySeedForTest writes a seed directly, bypassing
// every rule above it. It exists so the UNIQUE constraint on the column can be
// proved: nothing in the service writes that column, so there is no legitimate
// path that could ever collide, and a constraint no test can reach is a
// constraint nobody knows still exists. It is never called outside a test —
// `_ForTest` is what says so, and the control-plane call-site golden records
// the authority it reaches.
func (s *PostgresStore) ForceSolutionRuntimeBoundarySeedForTest(
	ctx context.Context, solutionID, seed string,
) error {
	return s.WithControlPlane(ctx, func(ctx context.Context) error {
		_, err := s.getQueryExecutor(ctx).Exec(ctx,
			`UPDATE public.solution_registrations SET runtime_boundary = $2 WHERE solution_id = $1`,
			solutionID, seed)
		return err
	})
}
