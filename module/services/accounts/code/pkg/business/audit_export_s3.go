package business

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AuditExportConfig carries everything the S3 exporter needs to hand
// audit events off to a customer's bucket. SecretAccessKey is
// intentionally cleartext in DB — the protection layer is bucket-
// level encryption (Postgres TDE / disk encryption); the API never
// echoes it back on List/Get (returns "" instead) so the FE can't
// leak it via accidental render.
type AuditExportConfig struct {
	ID              string
	OrgID           string
	Bucket          string
	Region          string
	Endpoint        string // empty → real AWS S3
	Prefix          string
	AccessKeyID     string
	SecretAccessKey string
	CadenceMinutes  int
	Enabled         bool
	LastExportedAt  *time.Time
	LastError       string
	LastErrorAt     *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// SaveAuditExportConfig upserts a per-org export config. When
// secretAccessKey is empty the existing secret is preserved — the
// FE submits "" rather than echoing the masked secret back.
//
// Pre-flight: before persisting, the resolved (creds + endpoint)
// MUST authenticate against the configured S3. A typo in the access
// key id or a wrong region would otherwise sit silently in the row
// and only surface in last_error a full cadence later (default 60
// min). Failing fast at Save time gives the operator a tight
// feedback loop in the admin form's toast.
//
// Authz expectation: caller is org-admin, enforced at the adapter.
// RLS: the read + upsert run inside WithOrgTx so the policy on
// audit_export_configs filters to this org's row.
func (s *Service) SaveAuditExportConfig(ctx context.Context, orgID string, in *AuditExportConfig) error {
	if in.CadenceMinutes < 5 {
		in.CadenceMinutes = 60
	}
	in.OrgID = orgID
	if in.ID == "" {
		in.ID = NewIDString()
	}
	// Pre-flight against the resolved config (network call to
	// customer's S3) — DON'T wrap this in WithOrgTx, no DB involved.
	// We need the existing secret first if the FE submitted "".
	if in.SecretAccessKey == "" {
		err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
			existing, _ := s.store.GetAuditExportConfig(ctx, orgID)
			if existing != nil {
				in.SecretAccessKey = existing.SecretAccessKey
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if err := VerifyAuditExportConnection(ctx, in); err != nil {
		return status.Errorf(codes.InvalidArgument,
			"connection probe failed: %v", err)
	}
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		return s.store.UpsertAuditExportConfig(ctx, in)
	}); err != nil {
		return err
	}
	s.emit(ctx, orgID, "system", "audit_export.configured", "audit_export_config", in.ID, orgID)
	return nil
}

// GetAuditExportConfig returns the config for an org with the
// secret_access_key masked to "" — never echo a stored secret to a
// client. Returns (nil, nil) when the org has no config yet.
func (s *Service) GetAuditExportConfig(ctx context.Context, orgID string) (*AuditExportConfig, error) {
	var cfg *AuditExportConfig
	err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		c, err := s.store.GetAuditExportConfig(ctx, orgID)
		cfg = c
		return err
	})
	if err != nil || cfg == nil {
		return nil, err
	}
	cfg.SecretAccessKey = ""
	return cfg, nil
}

// DeleteAuditExportConfig removes the config; exports stop on the
// next exporter scheduler tick.
func (s *Service) DeleteAuditExportConfig(ctx context.Context, orgID string) error {
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		return s.store.DeleteAuditExportConfig(ctx, orgID)
	}); err != nil {
		return err
	}
	s.emit(ctx, orgID, "system", "audit_export.deleted", "audit_export_config", orgID, orgID)
	return nil
}
