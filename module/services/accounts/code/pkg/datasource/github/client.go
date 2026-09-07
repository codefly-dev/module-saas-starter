// Package github is a minimal api.github.com client for datasource ingestion:
// resolve a repository's default branch, enumerate the files under a set of
// path prefixes at a ref, and fetch one file's bytes. It authenticates with a
// per-source token supplied by the caller (a PAT or a GitHub App installation
// token) and holds no persistence or crypto concerns of its own.
package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is api.github.com; tests and GitHub Enterprise override it.
const DefaultBaseURL = "https://api.github.com"

// maxFileBytes bounds a single fetched file so a pathological blob cannot
// exhaust memory or overflow the downstream job payload limit.
const maxFileBytes = 5 * 1024 * 1024

// ErrNotFound is returned when GitHub answers 404 for a repo, ref, or path.
var ErrNotFound = errors.New("github: not found")

// ErrFileTooLarge is returned when a file exceeds what the contents API can
// return inline (GitHub caps it at 1 MiB; files above that come back with
// encoding "none" and an empty body). Callers skip such files rather than
// treating them as a hard error.
var ErrFileTooLarge = errors.New("github: file too large for the contents API")

// File is one repository blob discovered under a source's path prefixes. Size is
// the blob's byte length as reported by the tree, so a snapshot manifest can
// carry it without fetching the blob.
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

// compareFileCap is GitHub's hard limit on the files a compare returns (300). A
// diff that reaches it is truncated and must be reconciled with a snapshot
// rather than trusted as a complete op list.
const compareFileCap = 300

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
// file list hit GitHub's 300-file cap and is therefore incomplete.
type Comparison struct {
	Status    string
	Files     []ChangedFile
	Truncated bool
}

// Client talks to a single GitHub deployment with a single token.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New returns a Client for the given token. baseURL empty uses DefaultBaseURL.
func New(token, baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// DefaultBranch returns the repository's default branch (e.g. "main").
func (c *Client) DefaultBranch(ctx context.Context, repo string) (string, error) {
	var out struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := c.getJSON(ctx, "/repos/"+repo, &out); err != nil {
		return "", err
	}
	if out.DefaultBranch == "" {
		return "", fmt.Errorf("github: repo %q has no default branch", repo)
	}
	return out.DefaultBranch, nil
}

// ResolveCommit returns the commit SHA that ref currently points at. It is
// monotonic across pushes even when file content reverts to an earlier state,
// so callers key delivery idempotency on it to avoid silently dropping an
// A→B→A revert that a content hash alone would dedupe away.
func (c *Client) ResolveCommit(ctx context.Context, repo, ref string) (string, error) {
	var out struct {
		SHA string `json:"sha"`
	}
	if err := c.getJSON(ctx, "/repos/"+repo+"/commits/"+url.PathEscape(ref), &out); err != nil {
		return "", err
	}
	if out.SHA == "" {
		return "", fmt.Errorf("github: ref %q in %q resolved to no commit", ref, repo)
	}
	return out.SHA, nil
}

// ListFiles returns every blob at ref whose path is under one of prefixes. An
// empty prefixes list matches the whole tree. Paths are matched at a path-
// segment boundary so "docs" selects "docs/a.md" but not "documents/a.md".
func (c *Client) ListFiles(ctx context.Context, repo, ref string, prefixes []string) ([]File, error) {
	var out struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
			Size int64  `json:"size"`
		} `json:"tree"`
		Truncated bool `json:"truncated"`
	}
	if err := c.getJSON(ctx, "/repos/"+repo+"/git/trees/"+url.PathEscape(ref)+"?recursive=1", &out); err != nil {
		return nil, err
	}
	if out.Truncated {
		return nil, errors.New("github: repository tree is too large to enumerate in one request")
	}
	files := make([]File, 0, len(out.Tree))
	for _, entry := range out.Tree {
		if entry.Type != "blob" {
			continue
		}
		if !pathMatches(entry.Path, prefixes) {
			continue
		}
		files = append(files, File{Path: entry.Path, SHA: entry.SHA, Size: entry.Size})
	}
	return files, nil
}

// Compare returns the changed files between base and head. GitHub caps the file
// list at 300; when that cap is reached the result is marked Truncated so the
// caller reconciles with a full snapshot instead of trusting a partial op list.
// A 404 (base commit no longer reachable, e.g. after a force push) surfaces as
// ErrNotFound, which the caller also handles by snapshotting.
func (c *Client) Compare(ctx context.Context, repo, base, head string) (*Comparison, error) {
	result := &Comparison{}
	seen := map[string]bool{}
	for page := 1; ; page++ {
		var out struct {
			Status string `json:"status"`
			Files  []struct {
				Filename         string `json:"filename"`
				PreviousFilename string `json:"previous_filename"`
				Status           string `json:"status"`
				SHA              string `json:"sha"`
			} `json:"files"`
		}
		target := "/repos/" + repo + "/compare/" +
			url.PathEscape(base) + "..." + url.PathEscape(head) +
			"?per_page=100&page=" + strconv.Itoa(page)
		if err := c.getJSON(ctx, target, &out); err != nil {
			return nil, err
		}
		if page == 1 {
			result.Status = out.Status
		}
		// Dedup by filename: a diff never lists a path twice, so a repeat means an
		// endpoint that ignored ?page and re-served an earlier page. Counting only
		// newly-seen files both drops those duplicates and lets the progress check
		// below terminate instead of looping to a false 300-file truncation.
		added := 0
		for _, f := range out.Files {
			if seen[f.Filename] {
				continue
			}
			seen[f.Filename] = true
			added++
			result.Files = append(result.Files, ChangedFile{
				Filename:         f.Filename,
				PreviousFilename: f.PreviousFilename,
				Status:           f.Status,
				SHA:              f.SHA,
			})
		}
		if len(result.Files) >= compareFileCap {
			result.Truncated = true
			result.Files = result.Files[:compareFileCap]
			return result, nil
		}
		// Fewer than a full page of new files means no more progress is possible:
		// either a genuine last page (< per_page returned) or a non-paginating
		// endpoint that only repeated files already seen.
		if added < 100 {
			return result, nil
		}
	}
}

// GetBlob returns the decoded bytes of a git blob by its sha, up to max bytes. It
// is the content-ticket resolution path: the module holds no token, so accounts
// re-fetches the blob with the source's token and streams it back. A blob larger
// than max is rejected with ErrFileTooLarge.
func (c *Client) GetBlob(ctx context.Context, repo, blobSHA string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/repos/"+repo+"/git/blobs/"+url.PathEscape(blobSHA), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(base64.StdEncoding.EncodedLen(int(max)))+4096))
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Errorf("github: GET blob %s: unexpected status %d", blobSHA, resp.StatusCode)
	}
	var out struct {
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
		Size     int64  `json:"size"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("github: decode blob %s: %w", blobSHA, err)
	}
	if out.Size > max {
		return nil, ErrFileTooLarge
	}
	if out.Encoding != "base64" {
		return nil, fmt.Errorf("github: unexpected blob encoding %q for %q", out.Encoding, blobSHA)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("github: decode blob %q: %w", blobSHA, err)
	}
	if int64(len(decoded)) > max {
		return nil, ErrFileTooLarge
	}
	return decoded, nil
}

// GetFileContent returns the decoded bytes of one file at ref.
func (c *Client) GetFileContent(ctx context.Context, repo, ref, path string) ([]byte, error) {
	var out struct {
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
		Type     string `json:"type"`
		Size     int64  `json:"size"`
	}
	target := "/repos/" + repo + "/contents/" + escapePath(path) + "?ref=" + url.QueryEscape(ref)
	if err := c.getJSON(ctx, target, &out); err != nil {
		return nil, err
	}
	if out.Type != "file" {
		return nil, fmt.Errorf("github: %q is not a file", path)
	}
	// A file too large for the contents API is reported as ErrFileTooLarge, not a
	// generic error, so a sync skips it instead of aborting the whole walk. GitHub
	// signals this two ways: the documented 1 MiB cap (size), and a 200 whose body
	// carries encoding "none" with empty content for files above it.
	if out.Size > maxFileBytes || out.Encoding == "none" {
		return nil, ErrFileTooLarge
	}
	if out.Encoding != "base64" {
		return nil, fmt.Errorf("github: unexpected content encoding %q for %q", out.Encoding, path)
	}
	// GitHub wraps the base64 payload at 60 columns.
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("github: decode %q: %w", path, err)
	}
	return decoded, nil
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
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFileBytes+1024))
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return fmt.Errorf("github: %s %s: unexpected status %d", req.Method, path, resp.StatusCode)
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("github: decode %s response: %w", path, err)
	}
	return nil
}

// pathMatches reports whether p is covered by any prefix at a segment boundary.
// No prefixes means the whole tree is in scope.
func pathMatches(p string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, prefix := range prefixes {
		prefix = strings.Trim(prefix, "/")
		if prefix == "" || p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

// escapePath percent-encodes each path segment while preserving the slashes
// GitHub's contents API expects between them.
func escapePath(p string) string {
	segments := strings.Split(p, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return strings.Join(segments, "/")
}
