package business

import (
	"context"
	"time"
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
// Authz expectation: caller is org-admin, enforced at the adapter.
func (s *Service) SaveAuditExportConfig(ctx context.Context, orgID string, in *AuditExportConfig) error {
	if in.CadenceMinutes < 5 {
		in.CadenceMinutes = 60
	}
	in.OrgID = orgID
	if in.ID == "" {
		in.ID = NewIDString()
	}
	if in.SecretAccessKey == "" {
		existing, _ := s.store.GetAuditExportConfig(ctx, orgID)
		if existing != nil {
			in.SecretAccessKey = existing.SecretAccessKey
		}
	}
	if err := s.store.UpsertAuditExportConfig(ctx, in); err != nil {
		return err
	}
	s.emit(ctx, orgID, "system", "audit_export.configured", "audit_export_config", in.ID, orgID)
	return nil
}

// GetAuditExportConfig returns the config for an org with the
// secret_access_key masked to "" — never echo a stored secret to a
// client. Returns (nil, nil) when the org has no config yet.
func (s *Service) GetAuditExportConfig(ctx context.Context, orgID string) (*AuditExportConfig, error) {
	cfg, err := s.store.GetAuditExportConfig(ctx, orgID)
	if err != nil || cfg == nil {
		return nil, err
	}
	cfg.SecretAccessKey = ""
	return cfg, nil
}

// DeleteAuditExportConfig removes the config; exports stop on the
// next exporter scheduler tick.
func (s *Service) DeleteAuditExportConfig(ctx context.Context, orgID string) error {
	if err := s.store.DeleteAuditExportConfig(ctx, orgID); err != nil {
		return err
	}
	s.emit(ctx, orgID, "system", "audit_export.deleted", "audit_export_config", orgID, orgID)
	return nil
}
