package infra

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

// orgIdentityProviderColumns is the shared projection; COALESCE keeps the
// optional text columns non-null so they scan into plain strings.
const orgIdentityProviderColumns = `
	id::text, org_id::text, kind, display_name,
	COALESCE(issuer, ''), COALESCE(client_id, ''), COALESCE(client_secret_ref, ''),
	COALESCE(jwks_url, ''), COALESCE(token_url, ''), COALESCE(audience, ''),
	claim_map, allowed_email_domains, allowed_groups,
	COALESCE(vanity_host, ''), status, created_at, updated_at`

func scanOrgIdentityProvider(row pgx.Row) (*business.OrgIdentityProvider, error) {
	var p business.OrgIdentityProvider
	var claimMap []byte
	if err := row.Scan(
		&p.ID, &p.OrgID, &p.Kind, &p.DisplayName,
		&p.Issuer, &p.ClientID, &p.ClientSecretRef,
		&p.JWKSURL, &p.TokenURL, &p.Audience,
		&claimMap, &p.AllowedEmailDomains, &p.AllowedGroups,
		&p.VanityHost, &p.Status, &p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if len(claimMap) > 0 {
		_ = json.Unmarshal(claimMap, &p.ClaimMap)
	}
	return &p, nil
}

// UpsertOrgIdentityProvider writes an org's provider configuration. One row per
// org (UNIQUE(org_id)); a re-configuration keeps the original id. Runs under the
// caller's WithOrgTx.
func (s *PostgresStore) UpsertOrgIdentityProvider(ctx context.Context, provider *business.OrgIdentityProvider) error {
	claimMap, err := json.Marshal(provider.ClaimMap)
	if err != nil {
		return err
	}
	if provider.ClaimMap == nil {
		claimMap = []byte("{}")
	}
	// A nil slice encodes as SQL NULL, which the NOT NULL array columns reject;
	// send an empty array instead so the column keeps its documented default.
	emailDomains := provider.AllowedEmailDomains
	if emailDomains == nil {
		emailDomains = []string{}
	}
	allowedGroups := provider.AllowedGroups
	if allowedGroups == nil {
		allowedGroups = []string{}
	}
	_, err = s.getQueryExecutor(ctx).Exec(ctx, `
		INSERT INTO org_identity_providers (
			id, org_id, kind, display_name, issuer, client_id, client_secret_ref,
			jwks_url, token_url, audience, claim_map, allowed_email_domains,
			allowed_groups, vanity_host, status)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), NULLIF($7, ''),
			NULLIF($8, ''), NULLIF($9, ''), NULLIF($10, ''), $11, $12,
			$13, NULLIF($14, ''), $15)
		ON CONFLICT (org_id) DO UPDATE SET
			kind                  = EXCLUDED.kind,
			display_name          = EXCLUDED.display_name,
			issuer                = EXCLUDED.issuer,
			client_id             = EXCLUDED.client_id,
			client_secret_ref     = EXCLUDED.client_secret_ref,
			jwks_url              = EXCLUDED.jwks_url,
			token_url             = EXCLUDED.token_url,
			audience              = EXCLUDED.audience,
			claim_map             = EXCLUDED.claim_map,
			allowed_email_domains = EXCLUDED.allowed_email_domains,
			allowed_groups        = EXCLUDED.allowed_groups,
			vanity_host           = EXCLUDED.vanity_host,
			status                = EXCLUDED.status,
			updated_at            = NOW()`,
		provider.ID, provider.OrgID, provider.Kind, provider.DisplayName,
		provider.Issuer, provider.ClientID, provider.ClientSecretRef,
		provider.JWKSURL, provider.TokenURL, provider.Audience,
		claimMap, emailDomains, allowedGroups,
		provider.VanityHost, provider.Status,
	)
	return err
}

// GetOrgIdentityProvider returns the org's configured provider, or (nil, nil)
// when none exists. Runs under the caller's WithOrgTx.
func (s *PostgresStore) GetOrgIdentityProvider(ctx context.Context, orgID string) (*business.OrgIdentityProvider, error) {
	row := s.getQueryExecutor(ctx).QueryRow(ctx,
		`SELECT `+orgIdentityProviderColumns+` FROM org_identity_providers WHERE org_id = $1`, orgID)
	provider, err := scanOrgIdentityProvider(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return provider, nil
}

// SetOrgIdentityProviderStatus flips the lifecycle status. Runs under the
// caller's WithOrgTx.
func (s *PostgresStore) SetOrgIdentityProviderStatus(ctx context.Context, orgID, status string) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		UPDATE org_identity_providers
		   SET status = $2, updated_at = NOW()
		 WHERE org_id = $1`, orgID, status)
	return err
}

// ResolveOrgProviderByEmailDomain returns the single active provider whose
// allowlist contains the domain. This is a pre-auth, cross-org lookup — we
// don't yet know the tenant — so it runs through the audited control-plane
// role with an explicit, parameterized filter. An ambiguous match (the same
// domain allowlisted by more than one active provider) resolves to nil rather
// than guessing a tenant.
func (s *PostgresStore) ResolveOrgProviderByEmailDomain(ctx context.Context, domain string) (*business.OrgIdentityProvider, error) {
	return s.resolvePreAuthProvider(ctx, `
		SELECT `+orgIdentityProviderColumns+`
		  FROM org_identity_providers
		 WHERE status = 'active'
		   AND allowed_email_domains @> ARRAY[$1::text]
		 LIMIT 2`, domain)
}

// ResolveOrgProviderByHost returns the active provider bound to a vanity host.
// The host is unique deployment-wide, so at most one row matches; same
// control-plane pre-auth path as the email-domain lookup.
func (s *PostgresStore) ResolveOrgProviderByHost(ctx context.Context, host string) (*business.OrgIdentityProvider, error) {
	return s.resolvePreAuthProvider(ctx, `
		SELECT `+orgIdentityProviderColumns+`
		  FROM org_identity_providers
		 WHERE status = 'active'
		   AND vanity_host = $1
		 LIMIT 2`, host)
}

func (s *PostgresStore) resolvePreAuthProvider(ctx context.Context, query, arg string) (*business.OrgIdentityProvider, error) {
	var matched *business.OrgIdentityProvider
	err := s.WithControlPlane(ctx, func(ctx context.Context) error {
		rows, err := s.getQueryExecutor(ctx).Query(ctx, query, arg)
		if err != nil {
			return err
		}
		defer rows.Close()
		var providers []*business.OrgIdentityProvider
		for rows.Next() {
			provider, err := scanOrgIdentityProvider(rows)
			if err != nil {
				return err
			}
			providers = append(providers, provider)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		// Exactly one match is required; ambiguity fails closed to nil so the
		// pre-auth response cannot misroute or leak which orgs share a domain.
		if len(providers) == 1 {
			matched = providers[0]
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return matched, nil
}
