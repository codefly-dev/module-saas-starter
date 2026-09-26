package github

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"accounts/pkg/datasource/connector"
	accountsv1 "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/wool"
)

// The GitHub files connector: a repository, read through the source's mirror,
// on the datasource envelope. An item's stable id is its path; its version is
// the git blob id. A repository has no per-file access list, so every item is
// source-scoped.

// ConnectorKey is the registry key of the GitHub connector.
const ConnectorKey = "github"

// Files-interface limits: a batch serves at most this many files and bytes.
const (
	MaxFilesPerBatch = 1000
	MaxBatchBytes    = 64 << 20
)

// estimatedRateLimitWindow is the reset a rate limit gets when the transport
// did not say when it resets (git's smart-HTTP transport never does).
const estimatedRateLimitWindow = time.Minute

// SourceConfig is a GitHub source's configuration as the connector reads it.
// InScope is the host's own path-and-suffix scope for the source, so the scope
// rule has one definition.
type SourceConfig struct {
	Repo    string
	Branch  string
	InScope func(path string) bool
}

// Mirror is the part of a Repository the connector reads through.
type Mirror interface {
	List(ctx context.Context, commit string, match func(path string) bool) ([]File, error)
	Compare(ctx context.Context, base, head string) (*Comparison, error)
	Fetch(ctx context.Context, ids []string) error
	Sizes(ctx context.Context, ids []string) (map[string]int64, error)
	Stream(ctx context.Context, ids []string, visit func(id string, size int64, content io.Reader) error) error
	// Fetches reports the network requests this handle made, so a read reports
	// what it cost.
	Fetches() int
	Close() error
}

// Remote is what the connector needs of a credentialed GitHub client.
type Remote interface {
	DefaultBranch(ctx context.Context, repo string) (string, error)
	ResolveCommit(ctx context.Context, repo, ref string) (string, error)
	OpenMirror(ctx context.Context, ws Workspace, repo string) (Mirror, error)
}

// OpenMirror adapts OpenRepository to Remote.
func (c *Client) OpenMirror(ctx context.Context, ws Workspace, repo string) (Mirror, error) {
	r, err := c.OpenRepository(ctx, ws, repo)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// RemoteFunc resolves the credentialed client for one source. The host owns the
// credential: it decrypts a stored token or mints an installation token here,
// per call, and the connector never sees anything but the client.
type RemoteFunc func(ctx context.Context, src connector.Source) (Remote, error)

// FilesConnector is the GitHub connector.
type FilesConnector struct {
	remote RemoteFunc
	budget connector.Budget
	now    func() time.Time
}

// ConnectorOption configures a FilesConnector.
type ConnectorOption func(*FilesConnector)

// WithMaxItemBytes lowers the per-file limit, for a test that must script an
// over-limit file cheaply. It never raises it past MaxFileBytes.
func WithMaxItemBytes(n int64) ConnectorOption {
	return func(c *FilesConnector) {
		if n > 0 && n < c.budget.MaxItemBytes {
			c.budget.MaxItemBytes = n
		}
	}
}

// NewFilesConnector returns the GitHub files connector over remote.
func NewFilesConnector(remote RemoteFunc, opts ...ConnectorOption) *FilesConnector {
	c := &FilesConnector{
		remote: remote,
		budget: connector.Budget{MaxItemsPerCall: MaxFilesPerBatch, MaxBytesPerCall: MaxBatchBytes, MaxItemBytes: MaxFileBytes},
		now:    time.Now,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Descriptor is the connector's catalog entry.
func (c *FilesConnector) Descriptor() connector.Descriptor {
	return connector.Descriptor{
		Key:         ConnectorKey,
		DisplayName: "GitHub",
		Description: "A GitHub repository, pulled on sync and kept fresh through push webhooks.",
		Interface:   connector.InterfaceFiles,
		ConfigFields: []connector.ConfigField{
			{Key: "repo", DisplayName: "Repository", Help: "owner/name, e.g. codefly-dev/module-saas-starter", Required: true},
			{Key: "paths", DisplayName: "Paths", Help: "Path prefixes to ingest; empty means the whole repository.", Required: false},
			{Key: "file_extensions", DisplayName: "File types", Help: "Case-insensitive suffix allowlist such as .md or .mdx, intersected with paths; empty means all types.", Required: false},
			{Key: "branch", DisplayName: "Branch", Help: "Git ref to pull; empty resolves to the default branch.", Required: false},
		},
		CredentialModes: []connector.CredentialMode{connector.CredentialOrgApp, connector.CredentialStaticSecret, connector.CredentialNone},
		SupportsWebhook: true,
		Readers:         connector.ReadersSourceScoped,
		Budget:          c.budget,
		Conformant:      true,
	}
}

func (c *FilesConnector) open(ctx context.Context, src connector.Source) (SourceConfig, Remote, Mirror, error) {
	cfg, ok := src.Config.(SourceConfig)
	if !ok || cfg.Repo == "" || cfg.InScope == nil {
		return SourceConfig{}, nil, nil, fmt.Errorf("github connector: source %s has no GitHub configuration", src.ID)
	}
	if err := ctx.Err(); err != nil {
		return SourceConfig{}, nil, nil, err
	}
	remote, err := c.remote(ctx, src)
	if err != nil {
		return SourceConfig{}, nil, nil, c.envelopeError(err)
	}
	mirror, err := remote.OpenMirror(ctx, Workspace{Org: src.OrgID, Source: src.ID}, cfg.Repo)
	if err != nil {
		return SourceConfig{}, nil, nil, c.envelopeError(err)
	}
	return cfg, remote, mirror, nil
}

// Changes diffs from version from to the branch's current head. An empty from
// is a complete snapshot; a from that is not an ancestor of head (history was
// rewritten), that the remote no longer serves, or whose diff passed the
// changed-file cap requires a resync.
func (c *FilesConnector) Changes(ctx context.Context, src connector.Source, from string) (connector.ChangeSet, error) {
	cfg, remote, mirror, err := c.open(ctx, src)
	if err != nil {
		return connector.ChangeSet{}, err
	}
	defer func() { _ = mirror.Close() }()
	branch := cfg.Branch
	if branch == "" {
		if branch, err = remote.DefaultBranch(ctx, cfg.Repo); err != nil {
			return connector.ChangeSet{}, c.envelopeError(err)
		}
	}
	head, err := remote.ResolveCommit(ctx, cfg.Repo, branch)
	if err != nil {
		return connector.ChangeSet{}, c.envelopeError(err)
	}
	set := connector.ChangeSet{SourceID: src.ID, From: from, To: head}
	readers := connector.ApplySourcePolicy(src, connector.SourceScoped())
	if from == "" {
		files, err := mirror.List(ctx, head, cfg.InScope)
		if err != nil {
			return connector.ChangeSet{}, c.envelopeError(err)
		}
		set.Complete = true
		for _, f := range files {
			set.Changes = append(set.Changes, connector.Change{
				Key: connector.KeyFor(src, f.Path), Kind: connector.ChangeAdded,
				ItemVersion: f.SHA, Locator: f.Path, Readers: readers,
			})
		}
		return set, nil
	}
	if from == head {
		return set, nil
	}
	if !ValidObjectID(from) {
		return connector.ChangeSet{}, connector.ErrResyncRequired
	}
	cmp, err := mirror.Compare(ctx, from, head)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return connector.ChangeSet{}, connector.ErrResyncRequired
		}
		return connector.ChangeSet{}, c.envelopeError(err)
	}
	if cmp.Status != CompareStatusAhead || cmp.Truncated {
		return connector.ChangeSet{}, connector.ErrResyncRequired
	}
	for _, f := range cmp.Files {
		set.Changes = append(set.Changes, changesFor(src, cfg.InScope, f, readers)...)
	}
	return set, nil
}

// changesFor maps one changed file onto the envelope under the source's scope.
// A move is MOVED only when both ends are in scope; a move into scope is an
// addition and a move out of it a deletion.
func changesFor(src connector.Source, inScope func(string) bool, f ChangedFile, readers *accountsv1.DatasourceItemReaders) []connector.Change {
	added := func(kind connector.ChangeKind) []connector.Change {
		return []connector.Change{{Key: connector.KeyFor(src, f.Filename), Kind: kind, ItemVersion: f.SHA, Locator: f.Filename, Readers: readers}}
	}
	deleted := func(path string) []connector.Change {
		return []connector.Change{{Key: connector.KeyFor(src, path), Kind: connector.ChangeDeleted, Locator: path}}
	}
	switch f.Status {
	case "added", "copied":
		if inScope(f.Filename) {
			return added(connector.ChangeAdded)
		}
	case "modified":
		if inScope(f.Filename) {
			return added(connector.ChangeModified)
		}
	case "removed":
		if inScope(f.Filename) {
			return deleted(f.Filename)
		}
	case "renamed":
		switch from, to := inScope(f.PreviousFilename), inScope(f.Filename); {
		case from && to:
			out := added(connector.ChangeMoved)
			out[0].PreviousItemID = f.PreviousFilename
			return out
		case to:
			return added(connector.ChangeAdded)
		case from:
			return deleted(f.PreviousFilename)
		}
	}
	return nil
}

// FetchFiles serves a batch of one version's files in request order, from one
// mirror fetch. Every ref must be in the source's scope at version (and at its
// item version, when named), and the batch must fit the budget; otherwise the
// whole batch is refused before visit is first called.
func (c *FilesConnector) FetchFiles(ctx context.Context, src connector.Source, version string, refs []connector.FileRef, visit func(connector.File, io.Reader) error) error {
	if len(refs) == 0 || len(refs) > c.budget.MaxItemsPerCall {
		return fmt.Errorf("%w: a batch names 1 to %d files", connector.ErrBatchTooLarge, c.budget.MaxItemsPerCall)
	}
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if ref.ItemID == "" || seen[ref.ItemID] {
			return fmt.Errorf("github connector: every file of a batch names a distinct item")
		}
		seen[ref.ItemID] = true
	}
	if !ValidObjectID(version) {
		return connector.ErrVersionNotFound
	}
	cfg, _, mirror, err := c.open(ctx, src)
	if err != nil {
		return err
	}
	defer func() { _ = mirror.Close() }()
	listed, err := mirror.List(ctx, version, cfg.InScope)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return connector.ErrVersionNotFound
		}
		return c.envelopeError(err)
	}
	inScope := make(map[string]string, len(listed))
	for _, f := range listed {
		inScope[f.Path] = f.SHA
	}
	ids := make([]string, len(refs))
	for i, ref := range refs {
		sha, ok := inScope[ref.ItemID]
		if !ok || (ref.ItemVersion != "" && ref.ItemVersion != sha) {
			return connector.ErrItemNotFound
		}
		ids[i] = sha
	}
	if err := mirror.Fetch(ctx, ids); err != nil {
		return c.envelopeError(err)
	}
	sizes, err := mirror.Sizes(ctx, ids)
	if err != nil {
		return c.envelopeError(err)
	}
	var total int64
	for _, id := range ids {
		if sizes[id] > c.budget.MaxItemBytes {
			return fmt.Errorf("%w: a file is over %d bytes", connector.ErrItemTooLarge, c.budget.MaxItemBytes)
		}
		total += sizes[id]
	}
	if total > c.budget.MaxBytesPerCall {
		return fmt.Errorf("%w: the batch is %d bytes, over %d; request fewer files", connector.ErrBatchTooLarge, total, c.budget.MaxBytesPerCall)
	}
	readers := connector.ApplySourcePolicy(src, connector.SourceScoped())
	i := 0
	err = mirror.Stream(ctx, ids, func(id string, size int64, content io.Reader) error {
		ref := refs[i]
		i++
		buffered := bufio.NewReaderSize(content, 512)
		head, _ := buffered.Peek(512)
		return visit(connector.File{
			Provenance: &accountsv1.DatasourceProvenance{
				SourceId: src.ID, OrgId: src.OrgID, BoundaryNodeId: src.BoundaryNodeID,
				Version: version, ItemId: ref.ItemID, ItemVersion: id,
			},
			Locator:     ref.ItemID,
			ContentType: http.DetectContentType(head),
			Size:        size,
			Readers:     readers,
		}, buffered)
	})
	if err != nil {
		return c.envelopeError(err)
	}
	wool.Get(ctx).In("github.FetchFiles").Info("served files", wool.Field("source", src.ID),
		wool.Field("files", len(refs)), wool.Field("bytes", total), wool.Field("git_fetches", mirror.Fetches()))
	return nil
}

// envelopeError maps a GitHub failure onto the envelope's typed refusals,
// keeping the original in the chain for the host's own diagnostics. A
// cancelled or expired context is reported as itself.
func (c *FilesConnector) envelopeError(err error) error {
	var limited *RateLimitError
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.As(err, &limited):
		out := &connector.RateLimitedError{ResetAt: limited.ResetAt, Scope: "credential"}
		if limited.Unauthenticated {
			out.Scope = "deployment"
		}
		if out.ResetAt.IsZero() {
			out.ResetAt, out.Estimated = c.now().Add(estimatedRateLimitWindow), true
		}
		return out
	case errors.Is(err, ErrUnauthorized), errors.Is(err, ErrForbidden):
		return fmt.Errorf("%w: %w", connector.ErrCredentialRefused, err)
	case errors.Is(err, ErrFileTooLarge):
		return fmt.Errorf("%w: %w", connector.ErrItemTooLarge, err)
	}
	return err
}
