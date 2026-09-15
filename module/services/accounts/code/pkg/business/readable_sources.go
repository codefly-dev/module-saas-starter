package business

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type sourceReadCursor struct {
	Scope     string
	After     string
	ExpiresAt time.Time
}

// SourceSyncRequest is one source's most recent "a sync was requested" record,
// read from the audit trail. It is deliberately not a CollectionSyncProvenance:
// that message also carries the ingest facts, which this record knows nothing
// about, and a carrier able to hold them invites a caller to assign the whole
// message and silently drop them.
type SourceSyncRequest struct {
	RequestedAt time.Time
	RequestedBy string
}

// CollectionMetadataDisclosure names the collection details the viewer's
// capability seals authority over. A detail it does not name is reported as
// withheld rather than as absent, so a consumer can tell "you may not see who
// can read this" from "nobody can".
type CollectionMetadataDisclosure struct {
	Grants        bool
	SyncRequester bool
}

// ReadableSourceCollections checks authority and reads one joined page in a
// single repeatable-read snapshot. Cursors bind identity and durable revisions;
// expiration of a standing grant also forces enumeration to restart. resources
// names the permission resource types the calling module's content is governed
// by, which is what read grants are intersected against.
func (s *Service) ReadableSourceCollections(ctx context.Context, org string, subjects []string, resources []string, disclosure CollectionMetadataDisclosure, req *gen.ListReadableSourceCollectionsRequest, authorize func(context.Context) error) (*gen.ListReadableSourceCollectionsResponse, error) {
	if org == "" || len(subjects) == 0 || authorize == nil {
		return nil, status.Error(codes.PermissionDenied, "viewer identity required")
	}
	for _, subject := range subjects {
		if subject == "" {
			return nil, status.Error(codes.PermissionDenied, "viewer identity required")
		}
	}
	// A module that declared no content has no basis to ask. The store refuses an
	// empty set too, but the guarantee belongs here rather than resting on the one
	// caller that happens to check first.
	if len(resources) == 0 {
		return nil, status.Error(codes.PermissionDenied, "module content resources required")
	}
	// The digest below binds the resource set into the cursor, so reordering a
	// declaration — which changes no authority — must not invalidate a live page.
	resources = slices.Sorted(slices.Values(resources))
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
			Org       string
			Subjects  []string
			Resources []string
			Revision  string
		}{org, subjects, resources, revision})
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
		collections, err := s.store.ListReadableSourcesPage(ctx, org, subjects, resources, cur.After, size+1)
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
		return s.discloseCollectionMetadata(ctx, org, resources, disclosure, out.Collections)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// discloseCollectionMetadata attaches the details this viewer may inspect to a
// page already narrowed to collections they can read, and marks every row with
// what was disclosed. Both extra reads stay inside the caller's snapshot, so the
// grants and the sync record describe the same instant as the page itself.
func (s *Service) discloseCollectionMetadata(ctx context.Context, org string, resources []string, disclosure CollectionMetadataDisclosure, collections []*gen.ReadableSourceCollection) error {
	if len(collections) == 0 {
		return nil
	}
	grantDisclosure := gen.MetadataDisclosure_METADATA_DISCLOSURE_WITHHELD
	if disclosure.Grants {
		grantDisclosure = gen.MetadataDisclosure_METADATA_DISCLOSURE_DISCLOSED
	}
	requesterDisclosure := gen.MetadataDisclosure_METADATA_DISCLOSURE_WITHHELD
	if disclosure.SyncRequester {
		requesterDisclosure = gen.MetadataDisclosure_METADATA_DISCLOSURE_DISCLOSED
	}
	var boundaries, sources []string
	for _, collection := range collections {
		collection.GrantDisclosure = grantDisclosure
		if collection.Sync == nil {
			collection.Sync = &gen.CollectionSyncProvenance{}
		}
		collection.Sync.RequesterDisclosure = requesterDisclosure
		if !slices.Contains(boundaries, collection.BoundaryId) {
			boundaries = append(boundaries, collection.BoundaryId)
		}
		sources = append(sources, collection.SourceId)
	}
	if disclosure.Grants {
		grants, err := s.store.ReadableCollectionGrants(ctx, org, boundaries, resources)
		if err != nil {
			return err
		}
		for _, collection := range collections {
			collection.ReadGrants = grants[collection.BoundaryId]
		}
	}
	if disclosure.SyncRequester {
		requests, err := s.store.LatestSourceSyncRequests(ctx, org, sources)
		if err != nil {
			return err
		}
		for _, collection := range collections {
			request, ok := requests[collection.SourceId]
			if !ok {
				continue
			}
			collection.Sync.RequestedAt = timestamppb.New(request.RequestedAt)
			collection.Sync.RequestedByLabel = request.RequestedBy
		}
	}
	return nil
}
