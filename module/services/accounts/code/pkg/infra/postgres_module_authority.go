package infra

import (
	"context"
	"errors"
	"fmt"

	"accounts/pkg/business"

	"github.com/jackc/pgx/v5"
)

// The live authority read every module capability decision makes.
//
// THIS FILE HELD A REGRESSION THAT BROKE EVERY CAPABILITY PATH, and the shape of
// the mistake is worth more than the fix.
//
// The first version joined `installations.agent_principal_id` against the calling
// module principal. Those are two different identity spaces: an installation's
// agent principal is minted per installation with a fresh uuid
// (`getOrCreateAgentPrincipal`), while a module principal is a DETERMINISTIC uuid
// derived from its declared prefix. The join could never match, so every module
// capability answered PermissionDenied — and the read ran on whatever pool the
// caller's context carried, which for the adapters is the bare request pool where
// RLS hides `installations` outright.
//
// Every test of it faked the read as always-current, so not one could see it. A
// fake that stands in for the thing under test proves the caller, never the
// thing. That is why `TestLiveModuleAuthorityOverARealInstallation` below goes
// through a real `InstallSolution` against real PostgreSQL.
//
// WHAT A MODULE CAPABILITY CAN AND CANNOT BE CHECKED AGAINST:
//
//   - PRODUCER EPOCH is checkable. `principal_authorization_revisions` is keyed
//     on (org_id, principal_id) and a module principal id is a uuid, so the
//     epoch of the calling principal is a real, readable fact.
//   - INSTALLATION FRESHNESS is checkable only when the capability NAMES an
//     installation. Nothing relates a module principal to an installation —
//     an installation is an organisation's consent to a solution TARGET, and the
//     module principal is not a party to it. So the host cannot infer which
//     installation a module capability acts under; the credential has to say.
//
// Until the seal carries an installation id, that term is therefore SKIPPED
// rather than failed. Skipping an unanswerable question is not the same as
// answering it "yes": the epoch still binds, and `AuthorizeModuleCapability`
// still refuses a credential that names an installation whose live state has
// moved. Failing instead — which is what the regression did — denies every
// legitimate module caller, which is not a stricter check but a broken one.

// LiveModuleAuthority reads the authority facts for one module principal.
//
// It runs on the CONTROL-PLANE pool explicitly rather than on whatever the
// caller's context carries. `principal_authorization_revisions` and
// `installations` are both granted to `app_control_plane` only, and under the
// request role RLS narrows `installations` to the calling tenant's session — a
// module capability is not exercised inside a tenant session, so the request
// pool returns nothing and the host would read "revoked" from "invisible".
//
// installationID is the installation the CREDENTIAL named, or empty when it
// names none.
func (s *PostgresStore) LiveModuleAuthority(
	ctx context.Context, principalID, orgID, installationID string,
) (*business.LiveModuleAuthority, error) {
	if orgID == "" {
		// Refused before the query, because `$1::uuid` on an empty string is a
		// cast error that surfaces as Unavailable — a database fault for what is
		// actually a malformed caller.
		return nil, fmt.Errorf("%w: no bound organization on the calling capability", business.ErrModuleAuthorityUnreadable)
	}

	live := &business.LiveModuleAuthority{}
	err := s.WithControlPlane(ctx, func(ctx context.Context) error {
		var epoch int64
		// The principal's own epoch, bumped by any narrowing of its envelope.
		// COALESCE to 0 because a principal that has never been narrowed has no
		// row, and "never narrowed" is epoch 0 rather than an error.
		if err := s.getQueryExecutor(ctx).QueryRow(ctx, `
			SELECT COALESCE(
				(SELECT revision FROM public.principal_authorization_revisions
				  WHERE org_id = $1::uuid AND principal_id = $2::uuid), 0)`,
			orgID, principalID,
		).Scan(&epoch); err != nil {
			return err
		}
		live.ProducerEpoch = uint64(epoch)

		if installationID == "" {
			// The credential names no installation, so there is no installation
			// to re-read. See the file comment: this is an unanswerable term,
			// not a satisfied one.
			return nil
		}

		var revision int64
		err := s.getQueryExecutor(ctx).QueryRow(ctx, `
			SELECT i.id::text, COALESCE(o.revision, 0)
			  FROM public.installations i
			  LEFT JOIN public.organization_authorization_revisions o
			         ON o.org_id = i.org_id
			 WHERE i.id = $1::uuid
			   AND i.org_id = $2::uuid
			   AND i.status = 'active'`,
			installationID, orgID,
		).Scan(&live.InstallationID, &revision)
		if errors.Is(err, pgx.ErrNoRows) {
			// Named an installation that is revoked, absent, or belongs to
			// another organisation. A verdict, and the one case where the
			// installation term genuinely refuses.
			return business.ErrModuleInstallationInactive
		}
		if err != nil {
			return err
		}
		live.InstallationRevision = uint64(revision)
		return nil
	})
	if errors.Is(err, business.ErrModuleInstallationInactive) {
		return nil, business.ErrModuleInstallationInactive
	}
	if err != nil {
		return nil, err
	}
	return live, nil
}

var _ business.ModuleAuthorityStore = (*PostgresStore)(nil)
