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

// File is one repository blob discovered under a source's path prefixes.
type File struct {
	Path string
	SHA  string
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
		files = append(files, File{Path: entry.Path, SHA: entry.SHA})
	}
	return files, nil
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
