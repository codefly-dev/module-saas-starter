package business

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/codefly-dev/core/wool"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// ── GDPR domain types ──────────────────────────────────────────

type GDPRRequestType string

const (
	GDPRExport   GDPRRequestType = "export"
	GDPRDeletion GDPRRequestType = "deletion"
)

type GDPRRequestStatus string

const (
	GDPRPending    GDPRRequestStatus = "pending"
	GDPRProcessing GDPRRequestStatus = "processing"
	GDPRCompleted  GDPRRequestStatus = "completed"
	GDPRFailed     GDPRRequestStatus = "failed"
)

type GDPRRequest struct {
	ID          string
	UserID      string
	Type        GDPRRequestType
	Status      GDPRRequestStatus
	DownloadURL string
	ExpiresAt   *time.Time
	Error       string
	CreatedAt   time.Time
	CompletedAt *time.Time
}

// GDPRStore abstracts persistence for GDPR data requests.
type GDPRStore interface {
	CreateGDPRRequest(ctx context.Context, req *GDPRRequest) error
	GetGDPRRequest(ctx context.Context, id string) (*GDPRRequest, error)
	UpdateGDPRRequest(ctx context.Context, req *GDPRRequest) error
	GetUserGDPRRequests(ctx context.Context, userID string) ([]*GDPRRequest, error)
}

// ── Business logic ─────────────────────────────────────────────

// RequestExport creates a pending data-export request and begins
// asynchronous collection of the user's data.
func (s *Service) RequestExport(ctx context.Context, userID string) (*GDPRRequest, error) {
	w := wool.Get(ctx).In("RequestExport")

	gdprStore, ok := s.store.(GDPRStore)
	if !ok {
		return nil, w.NewError("store does not implement GDPRStore")
	}

	req := &GDPRRequest{
		ID:     NewIDString(),
		UserID: userID,
		Type:   GDPRExport,
		Status: GDPRPending,
	}

	if err := s.store.As(Identity{UserID: req.UserID}).Within(ctx, func(ctx context.Context) error {
		return gdprStore.CreateGDPRRequest(ctx, req)
	}); err != nil {
		return nil, w.Wrapf(err, "cannot create GDPR export request")
	}

	s.emit(ctx, userID, "user", "gdpr.export_requested", "gdpr_request", req.ID, "")

	go s.processExport(context.Background(), gdprStore, req)

	return req, nil
}

// GetExportStatus returns a caller-owned export request.
func (s *Service) GetExportStatus(ctx context.Context, userID, requestID string) (*GDPRRequest, error) {
	return s.getGDPRStatus(ctx, userID, requestID, GDPRExport)
}

// GetDeletionStatus returns a caller-owned deletion request.
func (s *Service) GetDeletionStatus(ctx context.Context, userID, requestID string) (*GDPRRequest, error) {
	return s.getGDPRStatus(ctx, userID, requestID, GDPRDeletion)
}

func (s *Service) getGDPRStatus(ctx context.Context, userID, requestID string, expectedType GDPRRequestType) (*GDPRRequest, error) {
	w := wool.Get(ctx).In("GetGDPRStatus")

	gdprStore, ok := s.store.(GDPRStore)
	if !ok {
		return nil, w.NewError("store does not implement GDPRStore")
	}

	if userID == "" {
		return nil, w.NewError("authenticated user required")
	}

	// The user-scoped RLS policy makes another subject's request invisible even
	// when its UUID is supplied directly.
	var req *GDPRRequest
	if err := s.store.As(Identity{UserID: userID}).Within(ctx, func(ctx context.Context) error {
		var e error
		req, e = gdprStore.GetGDPRRequest(ctx, requestID)
		return e
	}); err != nil {
		return nil, w.Wrapf(err, "cannot get GDPR request")
	}
	if req == nil || req.UserID != userID || req.Type != expectedType {
		return nil, w.NewError("GDPR request not found")
	}
	return req, nil
}

// RequestDeletion creates a pending data-deletion request and begins
// asynchronous anonymization of the user's data.
func (s *Service) RequestDeletion(ctx context.Context, userID string) (*GDPRRequest, error) {
	w := wool.Get(ctx).In("RequestDeletion")

	gdprStore, ok := s.store.(GDPRStore)
	if !ok {
		return nil, w.NewError("store does not implement GDPRStore")
	}

	req := &GDPRRequest{
		ID:     NewIDString(),
		UserID: userID,
		Type:   GDPRDeletion,
		Status: GDPRPending,
	}

	if err := s.store.As(Identity{UserID: req.UserID}).Within(ctx, func(ctx context.Context) error {
		return gdprStore.CreateGDPRRequest(ctx, req)
	}); err != nil {
		return nil, w.Wrapf(err, "cannot create GDPR deletion request")
	}

	s.emit(ctx, userID, "user", "gdpr.deletion_requested", "gdpr_request", req.ID, "")

	go s.processDeletion(context.Background(), gdprStore, req)

	return req, nil
}

// processExport collects all user data and marks the request complete.
func (s *Service) processExport(ctx context.Context, gdprStore GDPRStore, req *GDPRRequest) {
	w := wool.Get(ctx).In("processExport")

	req.Status = GDPRProcessing
	_ = s.store.As(Identity{UserID: req.UserID}).Within(ctx, func(ctx context.Context) error {
		return gdprStore.UpdateGDPRRequest(ctx, req)
	})

	// Collect user data into an export bundle.
	export := map[string]any{}

	var user *gen.User
	var identities []*gen.UserIdentity
	var sessions []*Session
	var identitiesErr, sessionsErr error
	if err := s.store.As(Identity{UserID: req.UserID}).Within(ctx, func(ctx context.Context) error {
		var err error
		user, err = s.store.GetUser(ctx, req.UserID)
		if err != nil {
			return err
		}
		identities, identitiesErr = s.store.ListUserIdentities(ctx, req.UserID)
		sessions, sessionsErr = s.store.ListActiveSessions(ctx, req.UserID, 1000)
		return nil
	}); err != nil {
		s.failGDPRRequest(ctx, gdprStore, req, fmt.Sprintf("get user: %v", err))
		return
	}
	export["user"] = user

	if identitiesErr != nil {
		w.Warn("failed to list identities for export", wool.ErrField(identitiesErr))
	} else {
		export["identities"] = identities
	}

	// organizations is RLS-protected (Phase 2F); GDPR export spans
	// every org the user is a member of, so WithControlPlane.
	var orgs []*gen.Organization
	if berr := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		os, err := s.store.ListOrganizationsForUser(ctx, req.UserID)
		orgs = os
		return err
	}); berr != nil {
		w.Warn("failed to list orgs for export", wool.ErrField(berr))
	} else {
		export["organizations"] = orgs
	}

	if sessionsErr != nil {
		w.Warn("failed to list sessions for export", wool.ErrField(sessionsErr))
	} else {
		export["sessions"] = sessions
	}

	// Collect API keys. GDPR export spans every org the user is a
	// member of; under RLS the cross-org scan needs WithControlPlane —
	// the platform-level GDPR flow is privileged-by-policy.
	var apiKeys []*gen.APIKey
	if berr := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		ks, _, err := s.store.ListAPIKeys(ctx, "" /* all orgs */, 1000, "")
		apiKeys = ks
		return err
	}); berr != nil {
		w.Warn("failed to list api keys for export", wool.ErrField(berr))
	} else {
		export["api_keys"] = apiKeys
	}

	// Collect audit events for this user. Cross-tenant by nature:
	// the GDPR export gathers every event the user ever generated
	// regardless of org. audit_events is RLS-protected (Phase 2D)
	// so the scan needs WithControlPlane — this is a privileged-by-policy
	// platform flow.
	var auditEvents []AuditEntry
	if berr := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		ev, _, _, err := s.store.QueryAuditLog(ctx, "", req.UserID, "", "", "",
			nil, nil, 1000, "")
		auditEvents = ev
		return err
	}); berr != nil {
		w.Warn("failed to query audit log for export", wool.ErrField(berr))
	} else {
		export["audit_events"] = auditEvents
	}

	// Serialize the export.
	data, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		s.failGDPRRequest(ctx, gdprStore, req, fmt.Sprintf("marshal export: %v", err))
		return
	}

	// In a real implementation this would upload to object storage.
	// For now we mark complete with a placeholder URL containing the data size.
	now := time.Now()
	expires := now.Add(7 * 24 * time.Hour)
	req.Status = GDPRCompleted
	req.DownloadURL = fmt.Sprintf("data:application/json;size=%d", len(data))
	req.ExpiresAt = &expires
	req.CompletedAt = &now

	if err := s.store.As(Identity{UserID: req.UserID}).Within(ctx, func(ctx context.Context) error {
		return gdprStore.UpdateGDPRRequest(ctx, req)
	}); err != nil {
		w.Warn("failed to update GDPR request to completed", wool.ErrField(err))
	}
}

// processDeletion anonymizes user data and marks the request complete.
func (s *Service) processDeletion(ctx context.Context, gdprStore GDPRStore, req *GDPRRequest) {
	req.Status = GDPRProcessing
	_ = s.store.As(Identity{UserID: req.UserID}).Within(ctx, func(ctx context.Context) error {
		return gdprStore.UpdateGDPRRequest(ctx, req)
	})

	// Anonymize user: hash-based pseudonym.
	hash := sha256.Sum256([]byte(req.UserID))
	hashStr := fmt.Sprintf("%x", hash[:8])
	anonymousName := fmt.Sprintf("Deleted User #%s", hashStr)
	anonymousEmail := fmt.Sprintf("%s@deleted.local", hashStr)

	now := time.Now()
	req.Status = GDPRCompleted
	req.CompletedAt = &now

	// Anonymization, identity removal, session revocation, soft deletion, and
	// request completion are one user-scoped transaction. A partial GDPR
	// deletion is worse than a retryable failure, so every step is required.
	if err := s.store.As(Identity{UserID: req.UserID}).Within(ctx, func(ctx context.Context) error {
		if _, err := s.store.UpdateUser(ctx, req.UserID, map[string]any{
			"primary_email": anonymousEmail,
			"profile": map[string]string{
				"display_name": anonymousName,
			},
		}); err != nil {
			return err
		}
		if err := s.store.DeleteUserIdentities(ctx, req.UserID); err != nil {
			return err
		}
		if err := s.store.RevokeAllUserSessions(ctx, req.UserID, "gdpr_deletion"); err != nil {
			return err
		}
		if err := s.store.DeleteUser(ctx, req.UserID); err != nil {
			return err
		}
		if err := s.suppressProductIdentity(
			ctx,
			userAnalyticsSuppression(req.UserID),
			Identity{UserID: req.UserID},
		); err != nil {
			return err
		}
		return gdprStore.UpdateGDPRRequest(ctx, req)
	}); err != nil {
		s.failGDPRRequest(ctx, gdprStore, req, fmt.Sprintf("delete user data: %v", err))
		return
	}

	s.emit(ctx, req.UserID, "system", "gdpr.deletion_completed", "gdpr_request", req.ID, "")
}

func (s *Service) failGDPRRequest(ctx context.Context, gdprStore GDPRStore, req *GDPRRequest, errMsg string) {
	w := wool.Get(ctx).In("failGDPRRequest")
	req.Status = GDPRFailed
	req.Error = errMsg
	if err := s.store.As(Identity{UserID: req.UserID}).Within(ctx, func(ctx context.Context) error {
		return gdprStore.UpdateGDPRRequest(ctx, req)
	}); err != nil {
		w.Warn("failed to mark GDPR request as failed", wool.ErrField(err))
	}
}
