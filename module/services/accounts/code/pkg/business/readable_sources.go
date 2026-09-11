package business

import (
	gen "accounts/pkg/gen/saas/accounts/v1"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"sort"
	"strings"
)

// ReadableSourceCollections intersects current owner/actor grants in the same
// tenant transaction as source discovery. Failed enumeration returns no data.
func (s *Service) ReadableSourceCollections(ctx context.Context, org string, subjects []string, req *gen.ListReadableSourceCollectionsRequest) (*gen.ListReadableSourceCollectionsResponse, error) {
	if org == "" || len(subjects) == 0 {
		return nil, status.Error(codes.PermissionDenied, "viewer identity required")
	}
	var collections []*gen.ReadableSourceCollection
	err := s.store.WithOrgTx(ctx, org, func(ctx context.Context) error {
		var allowed map[string]bool
		for _, subject := range subjects {
			if subject == "" {
				return status.Error(codes.PermissionDenied, "viewer identity required")
			}
			current := map[string]bool{}
			token := ""
			seen := map[string]bool{}
			for pages := 0; ; pages++ {
				if pages >= 1000 {
					return status.Error(codes.ResourceExhausted, "scope enumeration exceeds limit")
				}
				scopes, err := s.store.ListAccessibleScopes(ctx, org, subject, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "documents", "read", token, 1000)
				if err != nil {
					return err
				}
				for _, scope := range scopes {
					current[scope.GetNodeId()] = true
				}
				if len(scopes) < 1000 {
					break
				}
				next := scopes[len(scopes)-1].GetScopePath()
				if next == "" || next == token || seen[next] {
					return status.Error(codes.FailedPrecondition, "scope pagination did not advance")
				}
				seen[next] = true
				token = next
			}
			if allowed == nil {
				allowed = current
			} else {
				for id := range allowed {
					if !current[id] {
						delete(allowed, id)
					}
				}
			}
		}
		if len(allowed) == 0 {
			return nil
		}
		sources, err := s.store.ListDatasourceSources(ctx, org)
		if err != nil {
			return err
		}
		if len(sources) > 10000 {
			return status.Error(codes.ResourceExhausted, "source enumeration exceeds limit")
		}
		for _, source := range sources {
			if source.OrgID != org {
				return status.Error(codes.Internal, "source tenant mismatch")
			}
			if !allowed[source.BoundaryNodeID] {
				continue
			}
			if source.Provider != DatasourceProviderGitHub {
				return status.Error(codes.Unimplemented, "source read projection is not supported for this provider")
			}
			if source.ID == "" || source.Repo == "" {
				return status.Error(codes.FailedPrecondition, "source attribution is incomplete")
			}
			ref := source.Branch
			if ref != "" && !strings.HasPrefix(ref, "refs/") {
				ref = "refs/heads/" + ref
			}
			collections = append(collections, &gen.ReadableSourceCollection{SourceId: source.ID, BoundaryId: source.BoundaryNodeID, Origin: source.Provider, Container: source.Repo, Ref: ref, Paths: source.Paths})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(collections, func(i, j int) bool { return collections[i].SourceId < collections[j].SourceId })
	raw, _ := json.Marshal(struct {
		Org         string
		Subjects    []string
		Collections []*gen.ReadableSourceCollection
	}{org, subjects, collections})
	digest := sha256.Sum256(raw)
	scope := hex.EncodeToString(digest[:])
	cur := struct{ Scope, After string }{Scope: scope}
	if req.GetPageToken() != "" {
		raw, err := base64.RawURLEncoding.DecodeString(req.GetPageToken())
		if err != nil || json.Unmarshal(raw, &cur) != nil || cur.Scope != scope {
			return nil, status.Error(codes.InvalidArgument, "source scope changed; restart pagination")
		}
	}
	start := sort.Search(len(collections), func(i int) bool { return collections[i].SourceId > cur.After })
	size := int(req.GetPageSize())
	if size <= 0 {
		size = 100
	}
	size = min(size, 1000)
	end := min(start+size, len(collections))
	out := &gen.ListReadableSourceCollectionsResponse{Collections: collections[start:end]}
	if end < len(collections) {
		cur.After = collections[end-1].SourceId
		raw, _ := json.Marshal(cur)
		out.NextPageToken = base64.RawURLEncoding.EncodeToString(raw)
	}
	return out, nil
}
