// Package github is a minimal GitHub client for datasource ingestion. Content —
// branches, commits, trees, diffs and file bytes — travels over git's smart-HTTP
// transport through a per-source mirror (git.go), one request per change set
// rather than one per file, and nothing is read over the REST API, so a sync
// spends none of the budget GitHub meters REST reads against (60 an hour per
// source IP for a client holding no credential). It authenticates with a per-source token
// supplied by the caller (a PAT or a GitHub App installation token) — or with
// none at all, for a public repository — and holds no persistence or crypto
// concerns of its own.
package github

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultBaseURL is the API base the deployment is configured with; the git
// origin is derived from it (api.github.com serves github.com). Tests and
// GitHub Enterprise override it.
const DefaultBaseURL = "https://api.github.com"

// ErrNotFound is returned when GitHub answers 404 for a repo, ref, or path.
var ErrNotFound = errors.New("github: not found")
var ErrUnauthorized = errors.New("github: unauthorized")
var ErrForbidden = errors.New("github: forbidden")
var ErrRateLimited = errors.New("github: rate limited")

// RateLimitError is a rate-limited request, carrying when GitHub said the limit
// resets (zero when it did not say) and whether the request held no credential.
// It matches ErrRateLimited, and ErrUnauthenticatedRateLimited when
// Unauthenticated, under errors.Is.
type RateLimitError struct {
	ResetAt         time.Time
	Unauthenticated bool
}

func (e *RateLimitError) Error() string {
	msg := ErrRateLimited.Error()
	if e.Unauthenticated {
		msg = ErrUnauthenticatedRateLimited.Error()
	}
	if !e.ResetAt.IsZero() {
		msg += " until " + e.ResetAt.UTC().Format(time.RFC3339)
	}
	return msg
}

// Is reports the sentinels a rate limit matches.
func (e *RateLimitError) Is(target error) bool {
	return target == ErrRateLimited || (e.Unauthenticated && target == ErrUnauthenticatedRateLimited)
}

// ErrUnauthenticatedRateLimited is ErrRateLimited for a client holding no token.
// GitHub meters unauthenticated requests per source IP address — 60 an hour on
// github.com — rather than per credential, so the remedy differs: waiting for
// the window to reset, or connecting the repository with a credential. It wraps
// ErrRateLimited, so a caller that only asks "was this rate limited?" still
// matches it.
var ErrUnauthenticatedRateLimited = fmt.Errorf("%w: unauthenticated request limit exhausted", ErrRateLimited)

// ErrFileTooLarge is returned when a file exceeds the size a read allows.
var ErrFileTooLarge = errors.New("github: file too large")

// File is one repository blob discovered under a source's path prefixes. Size is
// the blob's byte length, or -1 before the blob has been fetched.
type File struct {
	Path string
	SHA  string
	Size int64
}

// Compare statuses GitHub reports for a base...head comparison.
const (
	CompareStatusAhead     = "ahead"
	CompareStatusBehind    = "behind"
	CompareStatusIdentical = "identical"
	CompareStatusDiverged  = "diverged"
)

// ChangedFile is one file in a base...head comparison. Status is GitHub's
// per-file status (added, modified, removed, renamed, copied, changed); SHA is
// the blob sha at head; PreviousFilename is set only for a rename or copy.
type ChangedFile struct {
	Filename         string
	PreviousFilename string
	Status           string
	SHA              string
}

// Comparison is the result of comparing two commits. Status is the overall
// relationship (ahead, behind, identical, diverged); Truncated reports that the
// file list passed the comparison's cap and is therefore incomplete.
type Comparison struct {
	Status    string
	Files     []ChangedFile
	Truncated bool
}

// Client talks to a single GitHub deployment with a single token. An empty
// token sends no credential at all, which GitHub serves only for public
// repositories.
type Client struct {
	token      string
	gitBaseURL string
	gitScheme  string
	cacheRoot  string
	gitCalls   atomic.Int64
}

// Option configures a Client.
type Option func(*Client)

// WithCacheRoot places the per-source mirrors under dir instead of the user
// cache directory.
func WithCacheRoot(dir string) Option {
	return func(c *Client) { c.cacheRoot = dir }
}

// New returns a Client for the given token. baseURL empty uses DefaultBaseURL;
// the git transport origin is derived from it.
func New(token, baseURL string, opts ...Option) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	c := &Client{token: token, cacheRoot: DefaultCacheRoot()}
	c.gitBaseURL, c.gitScheme = gitBaseURLFor(strings.TrimRight(baseURL, "/"))
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// DefaultCacheRoot is where mirrors live when no root is configured: under the
// temporary directory, the one path a service whose root filesystem is read-only
// is given to write to. It is scratch space by design — a mirror lost with it
// costs one refetch.
func DefaultCacheRoot() string {
	return filepath.Join(os.TempDir(), "codefly-datasource-github")
}

// Requests reports the git network requests this client made.
func (c *Client) Requests() int64 { return c.gitCalls.Load() }

func (c *Client) recordFetch() { c.gitCalls.Add(1) }

// DefaultBranch returns the branch the repository's HEAD names, in one git
// request.
func (c *Client) DefaultBranch(ctx context.Context, repo string) (string, error) {
	out, err := c.lsRemote(ctx, repo, []string{"--symref"}, "HEAD")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if target, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
			branch, _, _ := strings.Cut(target, "\t")
			if branch != "" {
				return branch, nil
			}
		}
	}
	return "", fmt.Errorf("github: repo %q has no default branch", repo)
}

// ResolveCommit returns the commit the branch ref currently points at, in one git
// request. It is monotonic across pushes even when file content reverts to an
// earlier state, so callers key delivery idempotency on it to avoid silently
// dropping an A→B→A revert that a content hash alone would dedupe away.
func (c *Client) ResolveCommit(ctx context.Context, repo, ref string) (string, error) {
	ref = strings.TrimPrefix(ref, "refs/heads/")
	if ref == "" || strings.ContainsAny(ref, " \t\n*?[\\") || strings.HasPrefix(ref, "-") {
		return "", fmt.Errorf("github: invalid branch %q", ref)
	}
	out, err := c.lsRemote(ctx, repo, []string{"--heads"}, "refs/heads/"+ref)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		sha, name, ok := strings.Cut(line, "\t")
		if ok && name == "refs/heads/"+ref && ValidObjectID(sha) {
			return sha, nil
		}
	}
	return "", ErrNotFound
}

// RepositoryIsPublic reports whether repo can be read with no credential at all,
// in one git request. It must be asked of a client holding no token, and then
// the answer is a proof: GitHub serves an unauthenticated read only of a public
// repository, and answers one of a private, internal or missing repository the
// same way — which surfaces as ErrNotFound, never as "public". A client holding
// a token proves nothing by reading, so it never answers public.
func (c *Client) RepositoryIsPublic(ctx context.Context, repo string) (bool, error) {
	if c.token != "" {
		return false, errors.New("github: only a client holding no credential can prove a repository public")
	}
	if _, err := c.lsRemote(ctx, repo, nil, "HEAD"); err != nil {
		return false, err
	}
	return true, nil
}
