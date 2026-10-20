package business

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"accounts/pkg/jobs"

	"github.com/codefly-dev/core/wool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ErrDatasourceSourceScopeChanged reports that a source's fetch scope moved
// after the caller read it, so the whole-field replacement they asked for was
// refused instead of applied over the newer scope.
var ErrDatasourceSourceScopeChanged = errors.New("This source's branch or filters changed after this form was opened, so saving would have overwritten that change. Close it and edit the source again to work from its current scope.")

// DatasourceFetchScope is what a source fetches: the three values an edit
// replaces, together, so one can be stated as a whole.
type DatasourceFetchScope struct {
	Branch         string
	Paths          []string
	FileExtensions []string
}

// UpdateSourceInput is what a connected source may be re-aimed at: its branch
// and its path and file-type filters. Every field is the whole value the source
// is left with, never a patch — see UpdateSource.
type UpdateSourceInput struct {
	OrgID          string
	ID             string
	Branch         string
	Paths          []string
	FileExtensions []string
	// Expected is the scope the caller was shown, which the edit is accepted
	// against. Required, and nil is refused rather than read as "apply it
	// anyway": a whole-field replacement applied unconditionally is precisely
	// how one editor's save erases another's.
	Expected *DatasourceFetchScope
}

// sameFetchScope reports whether a scope is the one a source already holds.
//
// Both sides are normalized the same way before comparing, which is what makes
// re-submitting the values a form was filled with a no-op rather than a change:
// the stored row may hold a spelling an earlier normalizer let through (an
// untrimmed prefix, an uppercase suffix), and comparing raw would report that
// as a move and re-send the whole repository to the consuming module. The
// comparison is deliberately more lenient than normalizeFileExtensions, which
// refuses what it cannot store — a value already in the row has to stay
// comparable even if it would not be accepted today, or the source it belongs
// to could never be edited at all.
func sameFetchScope(scope DatasourceFetchScope, source *DatasourceSource) bool {
	return strings.TrimSpace(scope.Branch) == strings.TrimSpace(source.Branch) &&
		slices.Equal(normalizePaths(scope.Paths), normalizePaths(source.Paths)) &&
		slices.Equal(comparableFileExtensions(scope.FileExtensions), comparableFileExtensions(source.FileExtensions))
}

// comparableFileExtensions lowercases, trims and de-duplicates a file-type
// filter for comparison only, in the order normalizeFileExtensions would have
// produced, and refuses nothing.
func comparableFileExtensions(values []string) []string {
	var out []string
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.ToLower(strings.TrimSpace(raw))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// UpdateSource changes what an already-connected source fetches, keeping its
// identity: the same row, the same credential envelope, the same sync history,
// and the same collection its Entries land in. That is the whole point of it
// existing — the only way to move a source's branch or paths used to be delete
// and re-add, which drops the sync cursor and hands the consuming module a
// collection that starts again from nothing.
//
// What it will not do is as deliberate as what it does. The repository, the
// provider, the boundary node and the credential are each the identity of
// something a consumer already holds, so none of them is mutable here: a source
// pointed at another repository is another source, and re-aiming one in place
// would silently replace a collection's contents with a different repository's.
// The credential has its own path (a reconnect through SyncSource), because
// replacing it also moves whose authority the source's syncs run under.
//
// The fields are applied as whole values, exactly as on connect: empty paths
// ingest the whole repository and empty file extensions admit every file type.
// A repeated proto3 field cannot distinguish "not sent" from "sent empty", so
// treating absence as "leave alone" would make clearing a filter impossible and
// make an accidental omission silently preserve one; the contract says so, and
// the caller repeats what it means to keep.
//
// That is also why the request carries the scope it expects to be replacing.
// Stating a whole scope means every save asserts the complete state, so an
// unconditional one applied late is indistinguishable from a deliberate revert:
// a form opened while the source read one way and saved after someone else
// narrowed it would restore the wider scope and re-ingest everything the other
// person had just excluded, with nothing in the trail to show it was an
// accident. Input.Expected is checked against the locked row, and a mismatch is
// refused as ErrDatasourceSourceScopeChanged.
//
// Validation and normalization are the connect path's own — validRepo is not
// re-run because the repository is not touched, but the branch is proven
// readable with the source's existing credential before anything is written,
// and the paths and extensions go through normalizePaths and
// normalizeFileExtensions, so a source can never hold a scope that connecting
// would have refused.
//
// A change that moves what is fetched enqueues a forced reconcile in
// reconcileModeReconfigured and returns its job id: the next snapshot covers a
// different set of files, which for a consumer is a new snapshot of the
// collection, and the sync's progress reports DatasourceSyncTriggerReconfigured
// so a reader can tell that from an ordinary re-sync. A request that changes
// nothing writes nothing, records nothing and returns an empty job id.
//
// Every decision that governs the write is taken against the row under lock,
// inside the writing transaction: whether it is still there, whether it still
// holds the scope the caller was shown, and whether the request moves it. Only
// the GitHub branch check happens outside, because no transaction should be
// held open across a provider call — and it is sound there because the
// repository and the credential it validates against are not editable, and the
// expectation proved under lock is what decides whether the branch moved at all.
func (s *Service) UpdateSource(ctx context.Context, actorID string, input UpdateSourceInput) (*DatasourceSource, string, error) {
	w := wool.Get(ctx).In("UpdateSource")
	orgID := strings.TrimSpace(input.OrgID)
	id := strings.TrimSpace(input.ID)
	if orgID == "" || id == "" {
		return nil, "", w.NewError("org id and source id are required")
	}
	if input.Expected == nil {
		return nil, "", status.Error(codes.InvalidArgument, "the scope this edit expects to replace is required")
	}
	source, err := s.GetDatasourceSource(ctx, orgID, id)
	if err != nil {
		return nil, "", err
	}
	// Branch and path filters are the GitHub connector's config. The other
	// providers are configured by their own fields (a base URL, a sitemap, a
	// bucket prefix), and silently accepting a branch for one of them would
	// report a change that changed nothing.
	if source.Provider != DatasourceProviderGitHub {
		return nil, "", status.Error(codes.FailedPrecondition, "only a github source has a branch and path filters")
	}

	branch := strings.TrimSpace(input.Branch)
	paths := normalizePaths(input.Paths)
	extensions, err := normalizeFileExtensions(input.FileExtensions)
	if err != nil {
		return nil, "", w.Wrap(err)
	}

	// The branch is proven readable before the transaction opens, and only when
	// the request moves it away from the scope the caller was shown. That
	// expectation is verified under lock below, so a branch that is about to be
	// written has always been validated first.
	expected := *input.Expected
	if branch != strings.TrimSpace(expected.Branch) {
		if err := s.validateGitHubBranch(ctx, source, branch); err != nil {
			return nil, "", err
		}
	}

	var (
		updated DatasourceSource
		changed bool
	)
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		current, err := s.store.LockDatasourceSourceFetchScope(ctx, orgID, id)
		if err != nil {
			return err
		}
		// Deleted since the read above. Refusing here is what keeps an audit
		// record and a queued sync from describing a source that is gone.
		if current == nil {
			return ErrDatasourceSourceNotFound
		}
		// The scope moved under the caller. Their request states a whole scope,
		// so applying it now would overwrite whatever replaced what they saw —
		// most often restoring a filter someone else had just narrowed, and
		// re-ingesting everything it excluded.
		if !sameFetchScope(expected, current) {
			return ErrDatasourceSourceScopeChanged
		}
		// The boundary label is not in the locked projection (it comes from a
		// join with no row to lock), so it is carried over from the read above;
		// an edit cannot move the boundary, so it cannot have gone stale.
		updated = *current
		updated.BoundaryLabel = source.BoundaryLabel
		// Decided against the locked row, so "nothing changed" is a fact about
		// the stored scope and not about a copy of it read earlier.
		if sameFetchScope(DatasourceFetchScope{Branch: branch, Paths: paths, FileExtensions: extensions}, current) {
			return nil
		}
		previousBranch := current.Branch
		updated.Branch = branch
		updated.Paths = paths
		updated.FileExtensions = extensions
		if err := s.store.UpdateDatasourceSourceFetchScope(ctx, &updated); err != nil {
			return err
		}
		changed = true
		payload := map[string]any{
			"repo":            updated.Repo,
			"branch":          branch,
			"paths":           paths,
			"file_extensions": extensions,
			// The collection whose contents the re-snapshot replaces, so the
			// record reaches whoever holds a grant on it.
			"boundary": updated.BoundaryNodeID,
		}
		// Empty unless the branch moved, which is how the trail tells a re-aimed
		// source from one whose filters were narrowed where it stands.
		if branch != previousBranch {
			payload["previous_branch"] = previousBranch
		}
		return s.emitTx(ctx, actorID, "user", EventDatasourceSourceUpdated, "datasource", updated.ID, orgID, payload)
	}); err != nil {
		// Both refusals are the caller's answer, not a persistence fault, so
		// they reach the handler as themselves rather than wrapped in one.
		if errors.Is(err, ErrDatasourceSourceNotFound) || errors.Is(err, ErrDatasourceSourceScopeChanged) {
			return nil, "", err
		}
		return nil, "", w.Wrapf(err, "persist datasource source update")
	}
	if !changed {
		return &updated, "", nil
	}

	jobID, err := s.enqueueReconfiguredSync(ctx, &updated)
	if err != nil {
		// The source is already committed at its new scope. The periodic
		// reconcile and "Sync now" both still re-snapshot it, so the update
		// stands and the caller is told there is no sync to watch rather than
		// being told the edit failed and retrying it.
		w.Warn("enqueue reconfigured sync failed; the periodic reconcile will re-snapshot the source",
			wool.Field("source", updated.ID), wool.ErrField(err))
		return &updated, "", nil
	}
	return &updated, jobID, nil
}

// enqueueReconfiguredSync queues the re-snapshot an update owes the consuming
// module: the same forced reconcile "Sync now" and the first sync at connect
// enqueue, in the mode that names why it ran.
func (s *Service) enqueueReconfiguredSync(ctx context.Context, source *DatasourceSource) (string, error) {
	if s.datasourceJobs == nil {
		return "", errors.New("datasource connector is not configured")
	}
	return s.enqueueReconcile(ctx, source, reconcileModeReconfigured, NewIDString())
}

// validateGitHubBranch proves the source's own credential can read a branch
// before that branch is persisted, through the same validateGitHubAccess the
// connect path and the sync preflight use.
//
// It refuses where checkGitHubSyncPreflight defers. The preflight may hand a
// transient GitHub failure to the durable worker because the work it guards
// retries; there is nothing to retry here, and a branch that was never proven
// readable would be written to the row and then fail every sync from now on,
// with the source still reading active.
func (s *Service) validateGitHubBranch(ctx context.Context, source *DatasourceSource, branch string) error {
	if s.datasourceCipher == nil {
		return status.Error(codes.FailedPrecondition, "datasource secret cipher is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client, err := s.githubClientForSource(ctx, source)
	if err != nil {
		var failure *jobs.ProcessingError
		if errors.As(err, &failure) {
			code := codes.FailedPrecondition
			if failure.Retryable {
				code = codes.Unavailable
			}
			return status.Error(code, failure.Failure.Message)
		}
		return err
	}
	return validateGitHubAccess(ctx, client, source.Repo, branch)
}
