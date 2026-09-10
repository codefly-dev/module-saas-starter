package infra

import (
	"context"
	"encoding/json"
	"time"

	"github.com/codefly-dev/core/wool"
)

// MembershipIntegrityFinding is one organization-level administrative-continuity
// defect recorded by migration 132's scan. The two findings are mutually
// exclusive, so a finding is also an organization: a backlog of N rows is N
// organizations an operator has to decide about.
type MembershipIntegrityFinding struct {
	OrgID   string
	Finding string
	Detail  json.RawMessage
	FoundAt time.Time
}

// RecordMembershipIntegrityFindings re-runs the inventory and returns the number
// of organizations outstanding afterwards.
//
// Control plane rather than WithOrgTx because an inventory is the one thing that
// cannot be tenant-scoped: it has to see organizations nobody is currently
// acting as. organizations and organization_members force RLS — which binds the
// table owner too — so a caller that does not span tenants reads zero rows; the
// function refuses to run for such a caller rather than reporting an empty
// platform, and this is the boundary that satisfies it. No user-supplied input
// reaches the query.
func (s *PostgresStore) RecordMembershipIntegrityFindings(ctx context.Context) (int, error) {
	w := wool.Get(ctx).In("RecordMembershipIntegrityFindings")
	var outstanding int
	if err := s.WithControlPlane(ctx, func(ctx context.Context) error {
		return s.getQueryExecutor(ctx).QueryRow(ctx,
			`SELECT public.record_membership_integrity_findings()`).Scan(&outstanding)
	}); err != nil {
		return 0, w.Wrapf(err, "failed to record membership integrity findings")
	}
	return outstanding, nil
}

// ListMembershipIntegrityFindings returns the current backlog, most recently
// found first.
//
// Control plane for the same reason as the scan, and additionally because the
// findings table forces RLS with a tenant policy: read it as any role that does
// not span tenants — the store owner-connection included — and it is silently
// empty rather than denied. This is the only read path that reports the truth.
func (s *PostgresStore) ListMembershipIntegrityFindings(ctx context.Context) ([]MembershipIntegrityFinding, error) {
	w := wool.Get(ctx).In("ListMembershipIntegrityFindings")
	var findings []MembershipIntegrityFinding
	if err := s.WithControlPlane(ctx, func(ctx context.Context) error {
		rows, err := s.getQueryExecutor(ctx).Query(ctx, `
			SELECT org_id, finding, detail, found_at
			FROM membership_integrity_findings
			ORDER BY found_at DESC, org_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var finding MembershipIntegrityFinding
			if err := rows.Scan(&finding.OrgID, &finding.Finding, &finding.Detail, &finding.FoundAt); err != nil {
				return err
			}
			findings = append(findings, finding)
		}
		return rows.Err()
	}); err != nil {
		return nil, w.Wrapf(err, "failed to list membership integrity findings")
	}
	return findings, nil
}
