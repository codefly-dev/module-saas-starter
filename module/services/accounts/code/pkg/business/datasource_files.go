package business

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"accounts/pkg/datasource/github"
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
// up to maxDatasourceFilesPerBatch of them. The host reads the whole batch from
// the source's repository mirror, fetching whatever the mirror lacks in one
// request, so a snapshot of any size costs its reader one call per batch and
// GitHub one request per batch at most.
const (
	maxDatasourceFilesPerBatch   = 1000
	maxDatasourceFilesBatchBytes = 64 << 20

	// datasourceErrorDomain scopes the ErrorInfo reasons below.
	datasourceErrorDomain = "saas.accounts.v1"

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

// DatasourceFileRequest names one file of a batch: its path and the provider
// item id (a git blob id) the version lists it under.
type DatasourceFileRequest struct {
	Path   string
	ItemID string
}

// DatasourceProvenance is the envelope every served item carries.
type DatasourceProvenance struct {
	SourceID       string
	OrgID          string
	BoundaryNodeID string
	Version        string
	ItemID         string
}

// DatasourceFile opens one served file: its provenance, path, the host's
// advisory content type, and its exact size.
type DatasourceFile struct {
	Provenance  DatasourceProvenance
	Path        string
	ContentType string
	Size        int64
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
	if s.datasourceCipher == nil || s.newGitHubClient == nil {
		return status.Error(codes.FailedPrecondition, "datasource connector is not configured")
	}
	if !github.ValidObjectID(version) {
		return status.Error(codes.InvalidArgument, "version must be the full commit id the files were listed at")
	}
	if len(refs) == 0 || len(refs) > maxDatasourceFilesPerBatch {
		return status.Errorf(codes.InvalidArgument, "a batch names 1 to %d files", maxDatasourceFilesPerBatch)
	}
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if ref.Path == "" || seen[ref.Path] || !github.ValidObjectID(ref.ItemID) {
			return status.Error(codes.InvalidArgument, "every file names a distinct path and a full item id")
		}
		seen[ref.Path] = true
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
	client, err := s.githubClientForSource(ctx, source)
	if err != nil {
		return datasourceCredentialStatus(w, err)
	}
	repo, err := client.OpenRepository(ctx, githubWorkspace(source), source.Repo)
	if err != nil {
		return datasourceGitHubStatus(w, err, "open repository mirror")
	}
	defer func() { _ = repo.Close() }()

	listed, err := repo.List(ctx, version, datasourceFileScope(source))
	if err != nil {
		if errors.Is(err, github.ErrNotFound) {
			return datasourceStatus(codes.NotFound, DatasourceReasonVersionNotFound, "the source's repository does not hold that version", nil, 0)
		}
		return datasourceGitHubStatus(w, err, "list version")
	}
	inScope := make(map[string]string, len(listed))
	for _, f := range listed {
		inScope[f.Path] = f.SHA
	}
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		if inScope[ref.Path] != ref.ItemID {
			return datasourceStatus(codes.NotFound, DatasourceReasonFileNotInVersion,
				"a named file is not in the source's scope at that version under that item id", nil, 0)
		}
		ids = append(ids, ref.ItemID)
	}
	if err := repo.Fetch(ctx, ids); err != nil {
		return datasourceGitHubStatus(w, err, "fetch files")
	}
	sizes, err := repo.Sizes(ctx, ids)
	if err != nil {
		return status.Error(codes.Internal, w.Wrapf(err, "size files").Error())
	}
	var total int64
	for _, id := range ids {
		if sizes[id] > github.MaxFileBytes {
			return datasourceStatus(codes.FailedPrecondition, DatasourceReasonFileTooLarge,
				fmt.Sprintf("a named file exceeds the %d-byte file limit", github.MaxFileBytes),
				map[string]string{"limit": strconv.Itoa(github.MaxFileBytes)}, 0)
		}
		total += sizes[id]
	}
	if total > maxDatasourceFilesBatchBytes {
		return datasourceStatus(codes.FailedPrecondition, DatasourceReasonBatchTooLarge,
			fmt.Sprintf("the batch is %d bytes, over the %d-byte batch limit; request fewer files", total, maxDatasourceFilesBatchBytes),
			map[string]string{"limit": strconv.Itoa(maxDatasourceFilesBatchBytes)}, 0)
	}
	i := 0
	err = repo.Stream(ctx, ids, func(id string, size int64, content io.Reader) error {
		ref := refs[i]
		i++
		buffered := bufio.NewReaderSize(content, 512)
		head, _ := buffered.Peek(512)
		file := DatasourceFile{
			Provenance: DatasourceProvenance{
				SourceID: source.ID, OrgID: source.OrgID, BoundaryNodeID: source.BoundaryNodeID,
				Version: version, ItemID: id,
			},
			Path:        ref.Path,
			ContentType: http.DetectContentType(head),
			Size:        size,
		}
		if err := visit(file, buffered); err != nil {
			return err
		}
		// Record the data access on the source's own tenant spine, one event per
		// file served, as FetchDatasourceBlob does.
		s.emit(ctx, caller.PrincipalID, "agent", EventDatasourceBlobFetched, "datasource", source.ID, source.OrgID,
			map[string]any{"repo": source.Repo, "blob_sha": id, "bytes": size})
		return nil
	})
	if err != nil {
		if _, ok := status.FromError(err); ok {
			return err
		}
		if ctx.Err() != nil {
			return status.FromContextError(ctx.Err()).Err()
		}
		return status.Error(codes.Internal, w.Wrapf(err, "stream files").Error())
	}
	w.Info("served datasource files", wool.Field("source", source.ID), wool.Field("files", len(refs)), wool.Field("bytes", total), wool.Field("git_fetches", repo.Fetches()))
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

// datasourceStatus builds a gRPC status carrying an ErrorInfo with reason, and,
// for a retryable refusal, a RetryInfo.
func datasourceStatus(code codes.Code, reason, message string, metadata map[string]string, retry time.Duration) error {
	st := status.New(code, message)
	details := []protoadapt.MessageV1{&errdetails.ErrorInfo{Reason: reason, Domain: datasourceErrorDomain, Metadata: metadata}}
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
