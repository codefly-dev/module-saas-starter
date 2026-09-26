// Package github is a minimal GitHub client for datasource ingestion. Content —
// branches, commits, trees, diffs and file bytes — travels over git's smart-HTTP
// transport through a per-source mirror (git.go), one request per change set
// rather than one per file. The REST API is used only for what git cannot say:
// whether a repository is public. It authenticates with a per-source token
// supplied by the caller (a PAT or a GitHub App installation token) — or with
// none at all, for a public repository — and holds no persistence or crypto
// concerns of its own.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultBaseURL is api.github.com; tests and GitHub Enterprise override it.
const DefaultBaseURL = "https://api.github.com"

// maxResponseBytes bounds a REST response body.
const maxResponseBytes = 1 << 20

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
	baseURL    string
	token      string
	http       *http.Client
	gitBaseURL string
	gitScheme  string
	cacheRoot  string
	restCalls  atomic.Int64
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
	c := &Client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		token:     token,
		http:      &http.Client{Timeout: 30 * time.Second},
		cacheRoot: DefaultCacheRoot(),
	}
	c.gitBaseURL, c.gitScheme = gitBaseURLFor(c.baseURL)
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

// Usage reports the REST calls and the git network requests this client made.
func (c *Client) Usage() (rest, git int64) { return c.restCalls.Load(), c.gitCalls.Load() }

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

// RepositoryIsPublic reports whether GitHub describes repo as public. It is
// meant to be asked by a client holding no token, where the answer is also a
// proof: GitHub answers an unauthenticated request for a private or missing
// repository with the same 404, which surfaces as ErrNotFound and never as
// "public".
//
// Public is only ever an affirmative answer. A response that omits the `private`
// flag, or names any visibility other than public (an Enterprise "internal"
// repository), is reported as not public rather than guessed at.
func (c *Client) RepositoryIsPublic(ctx context.Context, repo string) (bool, error) {
	var out struct {
		Private    *bool  `json:"private"`
		Visibility string `json:"visibility"`
	}
	if err := c.getJSON(ctx, "/repos/"+repo, &out); err != nil {
		return false, err
	}
	if out.Private == nil || *out.Private {
		return false, nil
	}
	return out.Visibility == "" || out.Visibility == "public", nil
}

func (c *Client) getJSON(ctx context.Context, path string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	c.restCalls.Add(1)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return err
	}
	if err := c.classify(resp, body); err != nil {
		if !errors.Is(err, errUnexpectedStatus) {
			return err
		}
		return fmt.Errorf("github: %s %s: unexpected status %d", req.Method, path, resp.StatusCode)
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("github: decode %s response: %w", path, err)
	}
	return nil
}

// errUnexpectedStatus marks a non-2xx status classify has no sentinel for; each
// caller reports it with its own request context.
var errUnexpectedStatus = errors.New("github: unexpected status")

// classify maps a response status onto the package's sentinels, or nil for a
// success. Every request goes through it, so a rate limit reads the same whether
// it hit a JSON read or a blob fetch.
func (c *Client) classify(resp *http.Response, body []byte) error {
	// Secondary limits can omit Retry-After; GitHub identifies those in its
	// structured message. Inspect it for classification only, never expose it.
	var failure struct {
		Message string `json:"message"`
	}
	if resp.StatusCode == http.StatusForbidden {
		_ = json.Unmarshal(body, &failure)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return ErrUnauthorized
	case resp.StatusCode == http.StatusTooManyRequests ||
		(resp.StatusCode == http.StatusForbidden &&
			(resp.Header.Get("Retry-After") != "" || resp.Header.Get("X-RateLimit-Remaining") == "0" ||
				strings.Contains(strings.ToLower(failure.Message), "rate limit"))):
		return &RateLimitError{ResetAt: rateLimitReset(resp.Header, time.Now()), Unauthenticated: c.token == ""}
	case resp.StatusCode == http.StatusForbidden:
		return ErrForbidden
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return errUnexpectedStatus
	}
	return nil
}

// rateLimitReset reads when a rate limit lifts: Retry-After (seconds) for a
// secondary limit, else X-RateLimit-Reset (epoch seconds) for the primary one.
// Zero means GitHub did not say.
func rateLimitReset(h http.Header, now time.Time) time.Time {
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if secs, err := strconv.ParseInt(v, 10, 64); err == nil && secs >= 0 && secs < 24*3600 {
			return now.Add(time.Duration(secs) * time.Second).UTC()
		}
	}
	if v := strings.TrimSpace(h.Get("X-RateLimit-Reset")); v != "" {
		if epoch, err := strconv.ParseInt(v, 10, 64); err == nil && epoch > 0 {
			return time.Unix(epoch, 0).UTC()
		}
	}
	return time.Time{}
}
