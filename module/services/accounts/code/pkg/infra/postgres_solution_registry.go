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
	frontend_revision, frontend_manifest, frontend_contract_version,
	backend_revision, backend_upstream, backend_service_alias, backend_contract_version,
	declared_binding_id, declared_generation, declared_release, declared_target_id::text,
	updated_at`

func scanSolutionRegistration(row pgx.Row) (*business.SolutionRegistration, error) {
	var (
		record          business.SolutionRegistration
		tombstonedAt    *time.Time
		frontRevision   *int64
		frontManifest   *string
		frontContract   *string
		backendRevision *int64
		backendUpstream *string
		backendAlias    *string
		backendContract *string
		declaredBinding *string
		declaredTarget  *string
		declaredGen     *int64
		declaredRelease *string
	)
	if err := row.Scan(
		&record.SolutionID, &record.Publisher, &record.Revision, &record.RuntimeBoundary, &tombstonedAt,
		&frontRevision, &frontManifest, &frontContract,
		&backendRevision, &backendUpstream, &backendAlias, &backendContract,
		&declaredBinding, &declaredGen, &declaredRelease, &declaredTarget,
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
		}
	}
	if backendRevision != nil {
		record.Backend = &business.SolutionBackendHalf{
			Revision:        *backendRevision,
			Upstream:        *backendUpstream,
			ServiceAlias:    *backendAlias,
			ContractVersion: derefString(backendContract),
		}
	}
	// The cold-cutover schema requires the declared binding, generation, release
	// and target as one complete declaration.
	if declaredBinding != nil {
		record.Declared = &business.SolutionDeclaredBinding{
			BindingID:  *declaredBinding,
			TargetID:   derefString(declaredTarget),
			Generation: uint64(*declaredGen),
			Release:    derefString(declaredRelease),
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
// runtime_boundary is the one column this statement will not write, and that
// property moved here from the runtime self-registration writer this branch
// deletes (main's #1015/#1017). It is absent from the INSERT, so a new row takes
// the column's gen_random_uuid() default; absent from the ON CONFLICT DO UPDATE,
// so an existing row keeps what it was given — through a replaced half, a
// tombstone and a reactivation alike; and RETURNING reports whichever happened,
// so the caller's record is corrected from the database rather than the reverse.
//
// THE POINT IS THAT NOTHING ABOVE HERE MAY CHOOSE ONE. A boundary a caller
// could name would let one solution mint for another's and read, answer and
// recover its runs. A value invented above this statement is discarded rather
// than stored, and that is a property of the SQL rather than of any check — the
// column simply never appears on a write path.
//
// It is load-bearing that this survived the deletion. Resolving a
// modify-vs-delete by taking the delete leaves no compiler error and no failing
// test behind it, so the regression would have been silent;
// TestRuntimeBoundaryIsAssignedByTheDatabaseAndSurvivesEveryDeclaredWrite is
// what catches it.
func (s *PostgresStore) SaveSolutionRegistration(ctx context.Context, record *business.SolutionRegistration) error {
	var (
		frontRevision   *int64
		frontManifest   *string
		frontContract   *string
		backendRevision *int64
		backendUpstream *string
		backendAlias    *string
		backendContract *string
		declaredBinding *string
		declaredTarget  string
		declaredGen     *int64
		declaredRelease *string
	)
	if declared := record.Declared; declared != nil {
		generation := int64(declared.Generation)
		declaredBinding, declaredGen, declaredRelease = &declared.BindingID, &generation, &declared.Release
		declaredTarget = declared.TargetID
	}
	if half := record.Frontend; half != nil {
		frontRevision, frontManifest = &half.Revision, &half.Manifest
		frontContract = &half.ContractVersion
	}
	if half := record.Backend; half != nil {
		backendRevision, backendUpstream = &half.Revision, &half.Upstream
		backendAlias, backendContract = &half.ServiceAlias, &half.ContractVersion
	}
	var boundary string
	err := s.getQueryExecutor(ctx).QueryRow(ctx, `
		INSERT INTO public.solution_registrations (
			solution_id, publisher, revision, tombstoned_at,
			frontend_revision, frontend_manifest, frontend_contract_version,
			backend_revision, backend_upstream, backend_service_alias, backend_contract_version,
			declared_binding_id, declared_generation, declared_release, declared_target_id,
			updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, NULLIF($15, '')::uuid, $16)
		ON CONFLICT (solution_id) DO UPDATE SET
			publisher = EXCLUDED.publisher,
			revision = EXCLUDED.revision,
			tombstoned_at = EXCLUDED.tombstoned_at,
			frontend_revision = EXCLUDED.frontend_revision,
			frontend_manifest = EXCLUDED.frontend_manifest,
			frontend_contract_version = EXCLUDED.frontend_contract_version,
			backend_revision = EXCLUDED.backend_revision,
			backend_upstream = EXCLUDED.backend_upstream,
			backend_service_alias = EXCLUDED.backend_service_alias,
			backend_contract_version = EXCLUDED.backend_contract_version,
			declared_binding_id = EXCLUDED.declared_binding_id,
			declared_generation = EXCLUDED.declared_generation,
			declared_release = EXCLUDED.declared_release,
			declared_target_id = EXCLUDED.declared_target_id,
			updated_at = EXCLUDED.updated_at
		RETURNING runtime_boundary`,
		record.SolutionID, record.Publisher, record.Revision, record.TombstonedAt,
		frontRevision, frontManifest, frontContract,
		backendRevision, backendUpstream, backendAlias, backendContract,
		declaredBinding, declaredGen, declaredRelease, declaredTarget,
		record.UpdatedAt).Scan(&boundary)
	if err != nil {
		return err
	}
	record.RuntimeBoundary = boundary
	return nil
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
