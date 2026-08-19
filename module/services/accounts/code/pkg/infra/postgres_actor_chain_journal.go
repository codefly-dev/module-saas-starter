package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"accounts/pkg/business"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// =====================================================================
// actor_chain_journal — Postgres adapter (RFC-0003)
// =====================================================================
//
// Implements business.ActorChainJournal. Every on-behalf-of hop minted by the
// Work Context issuer is appended here, content-addressed and hash-chained to its
// parent hop, so the acting relationship survives the token's expiry. Revocation
// is a separate append-only list keyed by each hop's revocation id; the ancestry
// walk in AnyActorChainHopRevoked is what makes revoking one hop kill everything
// downstream of it.

func (s *PostgresStore) AppendActorChainHop(
	ctx context.Context,
	hop business.ActorChainHopInput,
) (*business.ActorChainHop, error) {
	if hop.OrgID == "" {
		return nil, errors.New("actor chain hop requires org id")
	}
	if hop.ID == "" {
		return nil, errors.New("actor chain hop requires id")
	}
	var stored *business.ActorChainHop
	err := s.WithOrgTx(ctx, hop.OrgID, func(ctx context.Context) error {
		q := s.getQueryExecutor(ctx)

		prevHash := ""
		if hop.ParentDelegationID != "" {
			err := q.QueryRow(ctx,
				`SELECT hop_hash FROM actor_chain_journal WHERE id = $1 AND org_id = $2`,
				hop.ParentDelegationID, hop.OrgID,
			).Scan(&prevHash)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("resolve parent actor chain hop: %w", err)
			}
		}
		hopHash := business.HopContentHash(hop, prevHash)

		scopes, err := json.Marshal(actorChainScopesForStorage(hop.GrantedScopes))
		if err != nil {
			return fmt.Errorf("encode actor chain scopes: %w", err)
		}

		// Idempotent on the hop id: re-issuing the same hop must not fork a
		// duplicate row or mint a second revocation handle. The generated
		// revocation id is discarded on conflict.
		if _, err := q.Exec(ctx, `
			INSERT INTO actor_chain_journal (
				id, org_id, task_id, session_id, owner_principal_id,
				actor_principal_id, actor_kind, parent_delegation_id,
				delegation_grant_id, granted_scopes, authorization_revision,
				revocation_id, hop_index, prev_hash, hop_hash
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
			ON CONFLICT (id) DO NOTHING`,
			hop.ID, hop.OrgID, nilIfEmpty(hop.TaskID), nilIfEmpty(hop.SessionID),
			nilIfNotUUID(hop.OwnerPrincipalID), nilIfNotUUID(hop.ActorPrincipalID),
			hop.ActorKind, nilIfNotUUID(hop.ParentDelegationID),
			nilIfNotUUID(hop.DelegationGrantID), scopes, int64(hop.AuthorizationRevision),
			uuid.NewString(), hop.HopIndex, nilIfEmpty(prevHash), hopHash,
		); err != nil {
			return fmt.Errorf("append actor chain hop: %w", err)
		}

		row, err := scanActorChainHop(q.QueryRow(ctx, `
			SELECT id, org_id, task_id, session_id, owner_principal_id,
			       actor_principal_id, actor_kind, parent_delegation_id,
			       delegation_grant_id, granted_scopes, authorization_revision,
			       revocation_id, hop_index, prev_hash, hop_hash
			FROM actor_chain_journal
			WHERE id = $1 AND org_id = $2`,
			hop.ID, hop.OrgID,
		))
		if err != nil {
			return fmt.Errorf("read appended actor chain hop: %w", err)
		}
		stored = row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return stored, nil
}

func (s *PostgresStore) AnyActorChainHopRevoked(
	ctx context.Context,
	orgID string,
	hopIDs []string,
) (bool, error) {
	if orgID == "" || len(hopIDs) == 0 {
		return false, nil
	}
	var revoked bool
	err := s.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		// Walk each hop and its ancestors via parent_delegation_id in one pass;
		// any hop is dead if some hop on its path carries a revoked revocation id.
		return s.getQueryExecutor(ctx).QueryRow(ctx, `
			WITH RECURSIVE ancestry AS (
				SELECT id, org_id, parent_delegation_id, revocation_id
				FROM actor_chain_journal
				WHERE id = ANY($1) AND org_id = $2
				UNION
				SELECT parent.id, parent.org_id, parent.parent_delegation_id, parent.revocation_id
				FROM actor_chain_journal AS parent
				JOIN ancestry ON ancestry.parent_delegation_id = parent.id
				             AND parent.org_id = ancestry.org_id
			)
			SELECT EXISTS (
				SELECT 1
				FROM ancestry
				JOIN actor_chain_revocations AS revocation
				  ON revocation.revocation_id = ancestry.revocation_id
				 AND revocation.org_id = ancestry.org_id
			)`,
			hopIDs, orgID,
		).Scan(&revoked)
	})
	if err != nil {
		return false, err
	}
	return revoked, nil
}

func (s *PostgresStore) RevokeActorChainHop(
	ctx context.Context,
	orgID string,
	hopID string,
	revokedByPrincipalID string,
	reason string,
) error {
	if orgID == "" || hopID == "" {
		return errors.New("actor chain revocation requires org id and hop id")
	}
	return s.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		q := s.getQueryExecutor(ctx)
		var revocationID string
		err := q.QueryRow(ctx,
			`SELECT revocation_id FROM actor_chain_journal WHERE id = $1 AND org_id = $2`,
			hopID, orgID,
		).Scan(&revocationID)
		if errors.Is(err, pgx.ErrNoRows) {
			return business.NewStoreError(
				fmt.Errorf("actor chain hop %s not found", hopID),
				business.ErrTypeNotFound,
			)
		}
		if err != nil {
			return fmt.Errorf("resolve actor chain hop for revocation: %w", err)
		}
		if _, err := q.Exec(ctx, `
			INSERT INTO actor_chain_revocations (
				revocation_id, org_id, revoked_by_principal_id, reason
			) VALUES ($1, $2, $3, $4)
			ON CONFLICT (revocation_id) DO NOTHING`,
			revocationID, orgID, nilIfNotUUID(revokedByPrincipalID), nilIfEmpty(reason),
		); err != nil {
			return fmt.Errorf("record actor chain revocation: %w", err)
		}
		return nil
	})
}

func actorChainScopesForStorage(scopes []business.ActorChainScope) []business.ActorChainScope {
	if scopes == nil {
		return []business.ActorChainScope{}
	}
	return scopes
}

func scanActorChainHop(row pgx.Row) (*business.ActorChainHop, error) {
	var hop business.ActorChainHop
	var taskID, sessionID, parentDelegationID, delegationGrantID, prevHash *string
	var revision int64
	var scopes []byte
	if err := row.Scan(
		&hop.ID,
		&hop.OrgID,
		&taskID,
		&sessionID,
		&hop.OwnerPrincipalID,
		&hop.ActorPrincipalID,
		&hop.ActorKind,
		&parentDelegationID,
		&delegationGrantID,
		&scopes,
		&revision,
		&hop.RevocationID,
		&hop.HopIndex,
		&prevHash,
		&hop.HopHash,
	); err != nil {
		return nil, err
	}
	if taskID != nil {
		hop.TaskID = *taskID
	}
	if sessionID != nil {
		hop.SessionID = *sessionID
	}
	if parentDelegationID != nil {
		hop.ParentDelegationID = *parentDelegationID
	}
	if delegationGrantID != nil {
		hop.DelegationGrantID = *delegationGrantID
	}
	if prevHash != nil {
		hop.PrevHash = *prevHash
	}
	hop.AuthorizationRevision = uint64(revision)
	if len(scopes) > 0 {
		if err := json.Unmarshal(scopes, &hop.GrantedScopes); err != nil {
			return nil, fmt.Errorf("decode actor chain scopes: %w", err)
		}
	}
	return &hop, nil
}

var _ business.ActorChainJournal = (*PostgresStore)(nil)
