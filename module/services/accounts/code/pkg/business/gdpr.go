package business

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/codefly-dev/core/wool"
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

var ErrPrivacyWorkflowUnavailable = errors.New("privacy workflow is not configured")

type PrivacyExportArtifact struct {
	DownloadURL string
	ExpiresAt   time.Time
}

type PrivacyWorkflow interface {
	Export(ctx context.Context, userID string) (PrivacyExportArtifact, error)
	Delete(ctx context.Context, userID string) error
}

// ── Business logic ─────────────────────────────────────────────

// RequestExport creates a pending data-export request and begins
// asynchronous collection of the user's data.
func (s *Service) RequestExport(ctx context.Context, userID string) (*GDPRRequest, error) {
	w := wool.Get(ctx).In("RequestExport")

	workflow := s.privacy
	if workflow == nil {
		return nil, ErrPrivacyWorkflowUnavailable
	}
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

	processingRequest := *req
	go s.processExport(context.Background(), gdprStore, &processingRequest, workflow)

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

	workflow := s.privacy
	if workflow == nil {
		return nil, ErrPrivacyWorkflowUnavailable
	}
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

	processingRequest := *req
	go s.processDeletion(context.Background(), gdprStore, &processingRequest, workflow)

	return req, nil
}

// processExport collects all user data and marks the request complete.
func (s *Service) processExport(
	ctx context.Context,
	gdprStore GDPRStore,
	req *GDPRRequest,
	workflow PrivacyWorkflow,
) {
	req.Status = GDPRProcessing
	_ = s.store.As(Identity{UserID: req.UserID}).Within(ctx, func(ctx context.Context) error {
		return gdprStore.UpdateGDPRRequest(ctx, req)
	})

	artifact, err := workflow.Export(ctx, req.UserID)
	if err != nil {
		s.failGDPRRequest(ctx, gdprStore, req, fmt.Sprintf("export user data: %v", err))
		return
	}

	now := time.Now()
	req.Status = GDPRCompleted
	req.DownloadURL = artifact.DownloadURL
	req.ExpiresAt = &artifact.ExpiresAt
	req.CompletedAt = &now

	if err := s.store.As(Identity{UserID: req.UserID}).Within(ctx, func(ctx context.Context) error {
		return gdprStore.UpdateGDPRRequest(ctx, req)
	}); err != nil {
		wool.Get(ctx).Warn(
			"failed to update GDPR request to completed",
			wool.ErrField(err),
		)
	}
}

func (s *Service) processDeletion(
	ctx context.Context,
	gdprStore GDPRStore,
	req *GDPRRequest,
	workflow PrivacyWorkflow,
) {
	req.Status = GDPRProcessing
	_ = s.store.As(Identity{UserID: req.UserID}).Within(ctx, func(ctx context.Context) error {
		return gdprStore.UpdateGDPRRequest(ctx, req)
	})

	if err := workflow.Delete(ctx, req.UserID); err != nil {
		s.failGDPRRequest(ctx, gdprStore, req, fmt.Sprintf("delete user data: %v", err))
		return
	}

	now := time.Now()
	req.Status = GDPRCompleted
	req.CompletedAt = &now
	if err := s.store.As(Identity{UserID: req.UserID}).Within(ctx, func(ctx context.Context) error {
		if err := s.suppressProductIdentity(
			ctx,
			userAnalyticsSuppression(req.UserID),
			Identity{UserID: req.UserID},
		); err != nil {
			return err
		}
		return gdprStore.UpdateGDPRRequest(ctx, req)
	}); err != nil {
		s.failGDPRRequest(ctx, gdprStore, req, fmt.Sprintf("complete deletion request: %v", err))
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
