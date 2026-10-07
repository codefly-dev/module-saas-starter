package infra

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

// solution_registrations is a control-plane-owned platform relation (no tenant
// column, no RLS): only app_control_plane may touch it. All four methods
// therefore assume the caller opened a WithControlPlane transaction, which
// getQueryExecutor picks up from ctx.

const solutionRegistrationColumns = `solution_id, publisher, revision, tombstoned_at,
	frontend_revision, frontend_manifest, frontend_contract_version,
	backend_revision, backend_upstream, backend_service_alias, backend_contract_version,
	declared_binding_id, declared_generation, declared_release, declared_target_id::text,
	declared_kind,
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
		declaredKind    *string
	)
	if err := row.Scan(
		&record.SolutionID, &record.Publisher, &record.Revision, &tombstonedAt,
		&frontRevision, &frontManifest, &frontContract,
		&backendRevision, &backendUpstream, &backendAlias, &backendContract,
		&declaredBinding, &declaredGen, &declaredRelease, &declaredTarget,
		&declaredKind,
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
	// The cold-cutover schema requires the declared binding, generation, release,
	// target and kind as one complete declaration.
	if declaredBinding != nil {
		record.Declared = &business.SolutionDeclaredBinding{
			BindingID:  *declaredBinding,
			TargetID:   derefString(declaredTarget),
			Generation: uint64(*declaredGen),
			Release:    derefString(declaredRelease),
			Kind:       business.SolutionDeclaredKind(derefString(declaredKind)),
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
// NOTHING ABOVE HERE MAY CHOOSE A RUNTIME BOUNDARY, and migration 28 is what
// makes that structural rather than a property of this statement's column list.
// The boundary used to be derived from a stored per-registration seed this
// statement deliberately omitted from both the INSERT and the ON CONFLICT DO
// UPDATE; it is now derived from `declared_binding_id`, which this statement DOES
// write — but only from the delivered declaration, never from a registrant's
// request. A boundary a caller could name would let one solution mint for
// another's and read, answer and recover its runs.
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
		declaredKind    *string
	)
	if declared := record.Declared; declared != nil {
		generation := int64(declared.Generation)
		declaredBinding, declaredGen, declaredRelease = &declared.BindingID, &generation, &declared.Release
		declaredTarget = declared.TargetID
		kind := string(declared.Kind)
		declaredKind = &kind
	}
	if half := record.Frontend; half != nil {
		frontRevision, frontManifest = &half.Revision, &half.Manifest
		frontContract = &half.ContractVersion
	}
	if half := record.Backend; half != nil {
		backendRevision, backendUpstream = &half.Revision, &half.Upstream
		backendAlias, backendContract = &half.ServiceAlias, &half.ContractVersion
	}
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		INSERT INTO public.solution_registrations (
			solution_id, publisher, revision, tombstoned_at,
			frontend_revision, frontend_manifest, frontend_contract_version,
			backend_revision, backend_upstream, backend_service_alias, backend_contract_version,
			declared_binding_id, declared_generation, declared_release, declared_target_id,
			declared_kind,
			updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, NULLIF($15, '')::uuid, $16, $17)
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
			declared_kind = EXCLUDED.declared_kind,
			updated_at = EXCLUDED.updated_at`,
		record.SolutionID, record.Publisher, record.Revision, record.TombstonedAt,
		frontRevision, frontManifest, frontContract,
		backendRevision, backendUpstream, backendAlias, backendContract,
		declaredBinding, declaredGen, declaredRelease, declaredTarget,
		declaredKind,
		record.UpdatedAt)
	return err
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

// The boundary SEED READS, restored from main's #1017. They were lost when this
// branch's side of the file won the merge, and the loss was SILENT: the mint
// obtains this interface with a discarded-error type assertion
// (`config.Authority.(business.SolutionRuntimeBoundarySeedStore)` in
// work_context_rpcs.go), so a store that does not implement it leaves the field
// nil and every boundary lookup dies at runtime with the build green. The
// compile-time assertion below is what makes that impossible to repeat.
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
// missingBackendHalf names which of the backend half's two columns is absent,
// for a refusal that says what it read rather than only "not serving". The
// schema's whole-or-absent CHECK means they move together, so one message
// covers both; naming them is what makes an operator's next step obvious.
func missingBackendHalf(revision *int64, upstream *string) string {
	switch {
	case revision == nil && upstream == nil:
		return "backend_revision and backend_upstream"
	case revision == nil:
		return "backend_revision"
	case upstream == nil || strings.TrimSpace(*upstream) == "":
		return "backend_upstream"
	}
	return ""
}

func (s *PostgresStore) SolutionRuntimeBoundarySeed(
	ctx context.Context, solutionID string,
) (business.SolutionBoundarySeed, error) {
	var (
		bindingID       *string
		publisher       string
		tombstonedAt    *time.Time
		backendRevision *int64
		backendUpstream *string
	)
	// Read the DECLARED row, on every mint. This selected
	// `runtime_boundary, …, backend_lease_expires_at` — and migration 26 DROPS
	// the lease column, so the statement did not return a stale value, it failed
	// outright: every solution-scoped mint errored on `column
	// backend_lease_expires_at does not exist`. Nothing caught it because the
	// suites that reach this path are DSN-gated and skip. `runtime_boundary` is
	// dropped in its turn by migration 28; the binding id read below is the seed.
	//
	// Serving is now delivered presence: a non-tombstoned declared row whose
	// applied generation carries a backend half. There is no lease to renew —
	// the renewal path is deleted — so revocation reaches a capability through
	// THIS read, which the mint and the gateway perform per request under the
	// existing 120s cache bound, never from an unbounded snapshot.
	err := s.WithControlPlane(ctx, func(ctx context.Context) error {
		return s.getQueryExecutor(ctx).QueryRow(ctx,
			`SELECT declared_binding_id, publisher, tombstoned_at, backend_revision, backend_upstream
			 FROM public.solution_registrations WHERE solution_id = $1`, solutionID).
			Scan(&bindingID, &publisher, &tombstonedAt, &backendRevision, &backendUpstream)
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
	// A row with no declared binding is not a declared presence at all: the
	// cutover's own constraint makes an undeclared row a withdrawn one.
	if bindingID == nil || strings.TrimSpace(*bindingID) == "" {
		return business.SolutionBoundarySeed{}, business.ErrSolutionRegistrationTombstoned
	}
	missing := missingBackendHalf(backendRevision, backendUpstream)
	return business.SolutionBoundarySeed{
		BindingID:          *bindingID,
		Publisher:          publisher,
		BackendServing:     missing == "",
		MissingBackendHalf: missing,
	}, nil
}

// SolutionRuntimeBoundarySeeds returns every stored seed, tombstones included.
// It is the input to the mint's collision check, which refuses a caller-named
// task_id that is any solution's boundary; a removed solution's runs may still
// be executing, so its seed still has to be protected.
func (s *PostgresStore) SolutionRuntimeBoundarySeeds(ctx context.Context) ([]string, error) {
	var seeds []string
	err := s.WithControlPlane(ctx, func(ctx context.Context) error {
		// Binding ids, not the `runtime_boundary` column migration 28 drops: the collision
		// check refuses a caller-named task_id that is any solution's boundary,
		// and a boundary is now derived from the binding. Tombstones included —
		// a withdrawn solution's runs may still be executing.
		rows, err := s.getQueryExecutor(ctx).Query(ctx,
			`SELECT declared_binding_id FROM public.solution_registrations
			 WHERE declared_binding_id IS NOT NULL`)
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

// SaveSolutionRegistration writes the whole record. Every half column is
// written on every save, so tombstoning — which passes a record with no halves
// — clears the endpoints in the same statement that records the deletion.
//
// NOTHING ABOVE HERE MAY CHOOSE A RUNTIME BOUNDARY, and migration 28 is what
// makes that structural rather than a property of this statement's column list.
// The boundary used to be derived from a stored per-registration seed this
// statement deliberately omitted from both the INSERT and the ON CONFLICT DO
// UPDATE; it is now derived from `declared_binding_id`, which this statement DOES
// write — but only from the delivered declaration, never from a registrant's
// request. A boundary a caller could name would let one solution mint for
// another's and read, answer and recover its runs.
// Asserted at compile time, because the only other consumer is a type assertion
// that discards its error. Without this line, deleting any method above is a
// silent downgrade rather than a build failure.
var _ business.SolutionRuntimeBoundarySeedStore = (*PostgresStore)(nil)
