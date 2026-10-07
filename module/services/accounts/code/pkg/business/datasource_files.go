package business

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"accounts/pkg/datasource/connector"
	"accounts/pkg/datasource/github"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/jobs"

	"github.com/codefly-dev/core/wool"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/protoadapt"
	"google.golang.org/protobuf/types/known/durationpb"
)

// The files interface serves a files-shaped datasource's content to a module in
// batches: one call names a source, the version its files were listed at, and
// up to github.MaxFilesPerBatch of them. The host reads the whole batch from
// the source's repository mirror, fetching whatever the mirror lacks in one
// request, so a snapshot of any size costs its reader one call per batch and
// GitHub one request per batch at most.
const (
	// accountsErrorDomain scopes the ErrorInfo reasons this package attaches —
	// the ones below and the module surface's own. It is not the service's only
	// domain: the adapters layer answers under SolutionRegistryErrorDomain, so a
	// reason is read together with the domain it arrived with.
	accountsErrorDomain = "saas.accounts.v1"

	DatasourceReasonRateLimited        = "DATASOURCE_RATE_LIMITED"
	DatasourceReasonBatchTooLarge      = "DATASOURCE_BATCH_TOO_LARGE"
	DatasourceReasonFileTooLarge       = "DATASOURCE_FILE_TOO_LARGE"
	DatasourceReasonFileNotInVersion   = "DATASOURCE_FILE_NOT_IN_VERSION"
	DatasourceReasonVersionNotFound    = "DATASOURCE_VERSION_NOT_FOUND"
	DatasourceReasonRepositoryTooLarge = "DATASOURCE_REPOSITORY_TOO_LARGE"

	// defaultRateLimitRetry is the back-off a rate limit gets when GitHub did
	// not say when it resets.
	defaultRateLimitRetry = time.Minute
)

// DatasourceFileRequest names one file of a batch: its stable item id (for a
// GitHub source, its path) and the item version the source version lists it
// with (a git blob id).
type DatasourceFileRequest struct {
	ItemID      string
	ItemVersion string
}

// DatasourceProvenance is the envelope every served item carries: the source,
// its org and boundary, the source version read, and the item's stable id and
// content version.
type DatasourceProvenance struct {
	SourceID       string
	OrgID          string
	BoundaryNodeID string
	Version        string
	ItemID         string
	ItemVersion    string
}

// DatasourceFile opens one served file: its provenance, path, the host's
// advisory content type, its exact size, and who may read it.
type DatasourceFile struct {
	Provenance  DatasourceProvenance
	Path        string
	ContentType string
	Size        int64
	Readers     *gen.DatasourceItemReaders
}

// ModuleFetchDatasourceFiles serves a batch of one source's files at one pinned
// version, handing visit each file in request order with a reader of exactly
// its size. Authorization is FetchDatasourceBlob's: the caller's datasource-queue
// grant plus the source row's own org. Every named file must be in the source's
// scope at that version under that item id, and the batch must fit its limits;
// otherwise the whole call is refused before visit is first called.
func (s *Service) ModuleFetchDatasourceFiles(ctx context.Context, caller ModuleCaller, sourceID, version string, refs []DatasourceFileRequest, visit func(DatasourceFile, io.Reader) error) error {
	w := wool.Get(ctx).In("ModuleFetchDatasourceFiles")
	grant, err := s.moduleGrant(caller)
	if err != nil {
		return err
	}
	if !grant.allowsQueue(DatasourceIngestQueue) {
		return status.Errorf(codes.PermissionDenied, "principal %s may not fetch datasource files: the %q queue grant is required", caller.PrincipalID, DatasourceIngestQueue)
	}
	if s.datasourceCipher == nil || s.newGitHubClient == nil || s.datasourceConnectors == nil {
		return status.Error(codes.FailedPrecondition, "datasource connector is not configured")
	}
	if !github.ValidObjectID(version) {
		return status.Error(codes.InvalidArgument, "version must be the full commit id the files were listed at")
	}
	if len(refs) == 0 || len(refs) > github.MaxFilesPerBatch {
		return status.Errorf(codes.InvalidArgument, "a batch names 1 to %d files", github.MaxFilesPerBatch)
	}
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if ref.ItemID == "" || seen[ref.ItemID] || !github.ValidObjectID(ref.ItemVersion) {
			return status.Error(codes.InvalidArgument, "every file names a distinct item id and a full item version")
		}
		seen[ref.ItemID] = true
	}
	source, err := s.store.GetDatasourceSourceByID(ctx, sourceID)
	if err != nil {
		return status.Error(codes.Internal, w.Wrapf(err, "load source").Error())
	}
	if source == nil || source.Provider != DatasourceProviderGitHub {
		return status.Errorf(codes.NotFound, "datasource source %s not found", sourceID)
	}
	if err := authorizeTenant(caller, grant, source.OrgID); err != nil {
		return err
	}
	files, ok := s.datasourceConnectors.Files(source.Provider)
	if !ok {
		return status.Errorf(codes.NotFound, "datasource source %s not found", sourceID)
	}
	crefs := make([]connector.FileRef, len(refs))
	for i, ref := range refs {
		crefs[i] = connector.FileRef{ItemID: ref.ItemID, ItemVersion: ref.ItemVersion}
	}
	var total int64
	// A module reading content is waiting on the answer, and it is served from
	// the mirror the sync filled: it spends the credential as interactive work.
	ctx = connector.WithPriority(ctx, connector.PriorityInteractive)
	err = files.FetchFiles(ctx, connectorSource(source), version, crefs, func(f connector.File, content io.Reader) error {
		total += f.Size
		p := f.Provenance
		return visit(DatasourceFile{
			Provenance: DatasourceProvenance{
				SourceID: p.GetSourceId(), OrgID: p.GetOrgId(), BoundaryNodeID: p.GetBoundaryNodeId(),
				Version: p.GetVersion(), ItemID: p.GetItemId(), ItemVersion: p.GetItemVersion(),
			},
			Path:        f.Locator,
			ContentType: f.ContentType,
			Size:        f.Size,
			Readers:     f.Readers,
		}, content)
	})
	if err != nil {
		return datasourceConnectorStatus(w, err)
	}
	// Record the data access on the source's own tenant spine: one event for the
	// whole batch, carrying what it served. A transient audit-write failure must
	// not fail the read, so this is the fire-and-forget emit.
	s.emit(ctx, caller.PrincipalID, "agent", EventDatasourceFilesFetched, "datasource", source.ID, source.OrgID,
		map[string]any{"repo": source.Repo, "version": version, "files": len(refs), "bytes": total})
	return nil
}

// readDatasourceBlob reads one blob of the source's repository through its
// mirror, fetching it in one request when the mirror lacks it. It serves the
// single-blob paths — a content ticket and the deprecated FetchDatasourceBlob —
// which carry no version, so the blob is authorized at the repository grain.
func (s *Service) readDatasourceBlob(ctx context.Context, source *DatasourceSource, client GitHubContentClient, blobSHA string, max int64) ([]byte, error) {
	repo, err := client.OpenRepository(ctx, githubWorkspace(source), source.Repo)
	if err != nil {
		return nil, err
	}
	defer func() { _ = repo.Close() }()
	if err := repo.Fetch(ctx, []string{blobSHA}); err != nil {
		return nil, err
	}
	return repo.Read(ctx, blobSHA, max)
}

// datasourceConnectorStatus maps a connector's typed refusal onto the status a
// module caller acts on, whatever the provider. Anything untyped falls through
// to the GitHub mapping, which never lets provider text reach the caller.
func datasourceConnectorStatus(w *wool.Wool, err error) error {
	var failure *jobs.ProcessingError
	var limited *connector.RateLimitedError
	switch {
	case errors.As(err, &failure):
		return datasourceCredentialStatus(w, err)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return status.FromContextError(err).Err()
	case errors.As(err, &limited):
		message := "The provider rate limited the read. Retry after the reset."
		if limited.Scope == "deployment" {
			message = githubUnauthenticatedRateLimitMessage
		}
		retry := defaultRateLimitRetry
		if d := time.Until(limited.ResetAt); d > 0 {
			retry = d
		}
		return datasourceStatus(codes.ResourceExhausted, DatasourceReasonRateLimited, message,
			map[string]string{"reset_at": limited.ResetAt.UTC().Format(time.RFC3339)}, retry)
	case errors.Is(err, connector.ErrItemNotFound):
		return datasourceStatus(codes.NotFound, DatasourceReasonFileNotInVersion,
			"a named file is not in the source's scope at that version with that item version", nil, 0)
	case errors.Is(err, connector.ErrVersionNotFound):
		return datasourceStatus(codes.NotFound, DatasourceReasonVersionNotFound, "the source's repository does not hold that version", nil, 0)
	case errors.Is(err, connector.ErrItemTooLarge):
		return datasourceStatus(codes.FailedPrecondition, DatasourceReasonFileTooLarge,
			fmt.Sprintf("a named file exceeds the %d-byte file limit", github.MaxFileBytes),
			map[string]string{"limit": strconv.Itoa(github.MaxFileBytes)}, 0)
	case errors.Is(err, connector.ErrBatchTooLarge):
		return datasourceStatus(codes.FailedPrecondition, DatasourceReasonBatchTooLarge,
			fmt.Sprintf("the batch is over the %d-byte batch limit; request fewer files", github.MaxBatchBytes),
			map[string]string{"limit": strconv.Itoa(github.MaxBatchBytes)}, 0)
	case errors.Is(err, connector.ErrCredentialRefused):
		return status.Error(codes.FailedPrecondition, "the provider refused the source's credential; the source must be reconnected")
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	return datasourceGitHubStatus(w, err, "fetch files")
}

// datasourceStatus builds a gRPC status carrying an ErrorInfo with reason, and,
// for a retryable refusal, a RetryInfo.
func datasourceStatus(code codes.Code, reason, message string, metadata map[string]string, retry time.Duration) error {
	st := status.New(code, message)
	details := []protoadapt.MessageV1{&errdetails.ErrorInfo{Reason: reason, Domain: accountsErrorDomain, Metadata: metadata}}
	if retry > 0 {
		details = append(details, &errdetails.RetryInfo{RetryDelay: durationpb.New(retry)})
	}
	withDetails, err := st.WithDetails(details...)
	if err != nil {
		return st.Err()
	}
	return withDetails.Err()
}

// datasourceRateLimitStatus types a provider rate limit for a module: when it
// resets, and how long to wait.
func datasourceRateLimitStatus(limited *github.RateLimitError, now time.Time) error {
	message := "GitHub rate limited the read. Retry after the reset."
	if limited.Unauthenticated {
		message = githubUnauthenticatedRateLimitMessage
	}
	retry := defaultRateLimitRetry
	metadata := map[string]string{}
	if !limited.ResetAt.IsZero() {
		metadata["reset_at"] = limited.ResetAt.UTC().Format(time.RFC3339)
		if d := limited.ResetAt.Sub(now); d > 0 {
			retry = d
		}
	}
	return datasourceStatus(codes.ResourceExhausted, DatasourceReasonRateLimited, message, metadata, retry)
}

// datasourceGitHubStatus maps a mirror or GitHub failure onto the status a
// module caller acts on. Provider text never reaches the caller.
func datasourceGitHubStatus(w *wool.Wool, err error, what string) error {
	var limited *github.RateLimitError
	switch {
	case errors.As(err, &limited):
		return datasourceRateLimitStatus(limited, time.Now())
	case errors.Is(err, github.ErrRepositoryTooLarge):
		return datasourceStatus(codes.FailedPrecondition, DatasourceReasonRepositoryTooLarge, "the source's repository exceeds the host's mirror size limit", nil, 0)
	case errors.Is(err, github.ErrTooManyFiles):
		return status.Error(codes.FailedPrecondition, "the source's version lists more in-scope files than a source may hold")
	case errors.Is(err, github.ErrGitUnavailable):
		return status.Error(codes.FailedPrecondition, "this host cannot read repositories: git is not installed in its image")
	case errors.Is(err, github.ErrUnauthorized), errors.Is(err, github.ErrForbidden):
		return status.Error(codes.FailedPrecondition, "GitHub refused the source's credential; the source must be reconnected")
	case errors.Is(err, github.ErrNotFound):
		return status.Error(codes.NotFound, "GitHub no longer serves the named content for this source")
	}
	return status.Error(codes.Internal, w.Wrapf(err, what).Error())
}

// datasourceCredentialStatus maps a credential resolution failure: a revoked
// installation or an unreadable credential is a precondition the tenant must
// repair, not an internal fault.
func datasourceCredentialStatus(w *wool.Wool, err error) error {
	var failure *jobs.ProcessingError
	if errors.As(err, &failure) {
		if failure.Retryable {
			return status.Error(codes.Unavailable, failure.Failure.Message)
		}
		return status.Error(codes.FailedPrecondition, failure.Failure.Message)
	}
	return status.Error(codes.Internal, w.Wrapf(err, "authenticate to github").Error())
}
