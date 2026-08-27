package githubconnector

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ErrFileTooLarge is returned by GetRepoContents for a file GitHub will not
// inline through the contents API (it caps files at ~1MB and responds with
// encoding "none" and an empty body). A caller walking a tree can errors.Is on
// this to skip the file — or re-fetch it via the Git blob API by its SHA —
// instead of failing the entire pull.
var ErrFileTooLarge = errors.New("github file too large for the contents API")

// Content kinds returned by the GitHub contents API.
const (
	ContentTypeFile      = "file"
	ContentTypeDir       = "dir"
	ContentTypeSymlink   = "symlink"
	ContentTypeSubmodule = "submodule"
)

// RepoContent is one entry from the repo-contents API: a file (with decoded
// bytes), or a directory (with its immediate children in Entries). Directory
// children carry metadata only — GitHub does not inline their content — so
// pulling a subtree walks Entries and re-fetches each file path.
type RepoContent struct {
	Type    string
	Name    string
	Path    string
	SHA     string
	Size    int
	Content []byte
	Entries []RepoContent
}

// contentPayload is the wire shape of a single contents entry. content/encoding
// are populated only for files fetched individually.
type contentPayload struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	SHA      string `json:"sha"`
	Size     int    `json:"size"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
}

// GetRepoContents fetches the file or directory at path in owner/repo. ref
// selects a branch, tag, or commit SHA; empty means the repository's default
// branch. A file result has decoded Content; a directory result has Entries.
func (c *Connector) GetRepoContents(ctx context.Context, token, owner, repo, path, ref string) (*RepoContent, error) {
	if strings.TrimSpace(owner) == "" || strings.TrimSpace(repo) == "" {
		return nil, fmt.Errorf("repo contents require an owner and repo")
	}

	endpoint := fmt.Sprintf("%s/repos/%s/%s/contents",
		c.baseURL, url.PathEscape(owner), url.PathEscape(repo))
	// The repo root is the bare /contents endpoint; a trailing slash (empty path)
	// is not the canonical form and GitHub can 404 it. Only append a path segment
	// when there is one.
	if escaped := escapePath(path); escaped != "" {
		endpoint += "/" + escaped
	}
	if ref = strings.TrimSpace(ref); ref != "" {
		endpoint += "?" + url.Values{"ref": {ref}}.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	setGitHubHeaders(req)

	var raw json.RawMessage
	if err := c.do(req, &raw); err != nil {
		return nil, fmt.Errorf("get repo contents %s/%s/%s: %w", owner, repo, path, err)
	}
	return parseContents(raw)
}

// parseContents distinguishes a directory (a JSON array) from a single file or
// other entry (a JSON object) by the first non-space byte.
func parseContents(raw json.RawMessage) (*RepoContent, error) {
	if isJSONArray(raw) {
		var payloads []contentPayload
		if err := json.Unmarshal(raw, &payloads); err != nil {
			return nil, fmt.Errorf("decode directory listing: %w", err)
		}
		dir := &RepoContent{Type: ContentTypeDir, Entries: make([]RepoContent, 0, len(payloads))}
		for _, p := range payloads {
			dir.Entries = append(dir.Entries, RepoContent{
				Type: p.Type, Name: p.Name, Path: p.Path, SHA: p.SHA, Size: p.Size,
			})
		}
		return dir, nil
	}

	var p contentPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("decode file contents: %w", err)
	}
	content := &RepoContent{Type: p.Type, Name: p.Name, Path: p.Path, SHA: p.SHA, Size: p.Size}
	if p.Type == ContentTypeFile {
		decoded, err := decodeContent(p.Encoding, p.Content)
		if err != nil {
			return nil, err
		}
		content.Content = decoded
	}
	return content, nil
}

// decodeContent decodes the base64 payload GitHub returns for a file. A file
// over the contents-API size cap comes back with encoding "none" and an empty
// body; that is reported as ErrFileTooLarge (a distinguishable sentinel) so a
// tree walk can skip it rather than aborting.
func decodeContent(encoding, content string) ([]byte, error) {
	switch encoding {
	case "base64":
		// GitHub wraps the base64 payload at column 60 with newlines.
		cleaned := strings.NewReplacer("\n", "", "\r", "").Replace(content)
		decoded, err := base64.StdEncoding.DecodeString(cleaned)
		if err != nil {
			return nil, fmt.Errorf("decode file content: %w", err)
		}
		return decoded, nil
	case "none":
		return nil, ErrFileTooLarge
	default:
		return nil, fmt.Errorf("unsupported github content encoding %q", encoding)
	}
}

func escapePath(path string) string {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

func isJSONArray(raw json.RawMessage) bool {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	return len(trimmed) > 0 && trimmed[0] == '['
}
