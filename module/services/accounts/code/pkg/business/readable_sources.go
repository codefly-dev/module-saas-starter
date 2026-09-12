package business

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"time"

	gen "accounts/pkg/gen/saas/accounts/v1"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type sourceReadCursor struct {
	Scope     string
	After     string
	ExpiresAt time.Time
}

// ReadableSourceCollections checks authority and reads one joined page in a
// single repeatable-read snapshot. Cursors bind identity and durable revisions;
// expiration of a standing grant also forces enumeration to restart.
func (s *Service) ReadableSourceCollections(ctx context.Context, org string, subjects []string, req *gen.ListReadableSourceCollectionsRequest, authorize func(context.Context) error) (*gen.ListReadableSourceCollectionsResponse, error) {
	if org == "" || len(subjects) == 0 || authorize == nil {
		return nil, status.Error(codes.PermissionDenied, "viewer identity required")
	}
	for _, subject := range subjects {
		if subject == "" {
			return nil, status.Error(codes.PermissionDenied, "viewer identity required")
		}
	}
	var cur sourceReadCursor
	if req.GetPageToken() != "" {
		raw, err := base64.RawURLEncoding.DecodeString(req.GetPageToken())
		if err != nil || json.Unmarshal(raw, &cur) != nil || cur.Scope == "" || cur.After == "" {
			return nil, status.Error(codes.InvalidArgument, "invalid source cursor")
		}
		if _, err := uuid.Parse(cur.After); err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid source cursor")
		}
	}
	size := int(req.GetPageSize())
	if size <= 0 {
		size = 100
	}
	size = min(size, 1000)
	out := &gen.ListReadableSourceCollectionsResponse{}
	err := s.store.WithSourceReadSnapshot(ctx, org, func(ctx context.Context) error {
		if err := authorize(ctx); err != nil {
			return err
		}
		revision, expires, err := s.store.SourceReadRevision(ctx, org, subjects)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(struct {
			Org      string
			Subjects []string
			Revision string
		}{org, subjects, revision})
		if err != nil {
			return err
		}
		digest := sha256.Sum256(raw)
		scope := hex.EncodeToString(digest[:])
		// The database computes the next expiry using its transaction clock.
		// Comparing deadlines also detects expiry without trusting the app clock.
		if req.GetPageToken() != "" && (cur.Scope != scope || !cur.ExpiresAt.Equal(expires)) {
			return status.Error(codes.InvalidArgument, "source scope changed; restart pagination")
		}
		collections, err := s.store.ListReadableSourcesPage(ctx, org, subjects, cur.After, size+1)
		if err != nil {
			return err
		}
		out.Collections = collections
		if len(collections) > size {
			out.Collections = collections[:size]
			raw, err := json.Marshal(sourceReadCursor{Scope: scope, After: collections[size-1].SourceId, ExpiresAt: expires})
			if err != nil {
				return err
			}
			out.NextPageToken = base64.RawURLEncoding.EncodeToString(raw)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
