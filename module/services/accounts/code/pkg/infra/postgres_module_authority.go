package infra

import (
	"context"
	"errors"

	"accounts/pkg/business"

	"github.com/jackc/pgx/v5"
)

// The live authority read every capability decision makes.
//
// ONE statement, and that is the whole reason this is a single method rather
// than three. The installation, its revision and the principal's epoch are three
// authority facts, and reading them in separate round trips lets a revoke land
// between two of them — producing a decision that describes a state which never
// existed. Joined here, all three are read at one snapshot.
//
// Control-plane, because a module capability acts across the organisation's own
// boundary: the request role's RLS would narrow the installation read to the
// calling tenant's session, and a module principal's capability is not exercised
// inside a tenant session.

// LiveModuleAuthority reads installation freshness and the producer epoch for one
// module principal in one organisation.
//
// `installations` is joined on the AGENT PRINCIPAL rather than on the module
// principal id, because an installation's `agent_principal_id` is the identity
// the module acts as — which is what the capability's caller id resolves to.
//
// ErrModuleInstallationInactive when there is no ACTIVE installation, rather
// than zero values. Zero would read as "revision 0", which a credential minted
// before any revision existed would match — so the absence has to be an error
// and not a value.
func (s *PostgresStore) LiveModuleAuthority(
	ctx context.Context, principalID, orgID string,
) (*business.LiveModuleAuthority, error) {
	var (
		installationID string
		revision       int64
		epoch          int64
	)
	err := s.getQueryExecutor(ctx).QueryRow(ctx, `
		SELECT i.id::text,
		       -- The organisation's authorization revision is the installation's
		       -- freshness: it is bumped by every change to who may do what in
		       -- that organisation, which is exactly the set of events that must
		       -- invalidate a capability minted against it.
		       COALESCE(o.revision, 0),
		       -- The principal's own epoch, bumped by any narrowing of its
		       -- envelope. Without it a narrowing revokes only the credential it
		       -- was holding and the replacement is reminted with the old
		       -- authority — so the narrowing would undo itself after one
		       -- credential lifetime.
		       COALESCE(p.revision, 0)
		  FROM public.installations i
		  LEFT JOIN public.organization_authorization_revisions o
		         ON o.org_id = i.org_id
		  LEFT JOIN public.principal_authorization_revisions p
		         ON p.org_id = i.org_id AND p.principal_id = i.agent_principal_id
		 WHERE i.org_id = $1::uuid
		   AND i.agent_principal_id = $2::uuid
		   AND i.status = 'active'`,
		orgID, principalID,
	).Scan(&installationID, &revision, &epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, business.ErrModuleInstallationInactive
	}
	if err != nil {
		return nil, err
	}
	return &business.LiveModuleAuthority{
		InstallationID:       installationID,
		InstallationRevision: uint64(revision),
		ProducerEpoch:        uint64(epoch),
	}, nil
}

var _ business.ModuleAuthorityStore = (*PostgresStore)(nil)
