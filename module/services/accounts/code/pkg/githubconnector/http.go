package githubconnector

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const (
	acceptHeader     = "application/vnd.github+json"
	apiVersion       = "2022-11-28"
	userAgent        = "saas-starter-github-connector"
	maxResponseBytes = 25 << 20 // GitHub caps file responses at 1MB and dir listings at 1000 entries; this bounds a hostile body.
)

// APIError is a non-2xx response from the GitHub REST API. It carries the
// status so callers can branch (a 404 on repo contents means the path is gone,
// not a hard failure).
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("github api: unexpected status %d", e.StatusCode)
	}
	return fmt.Sprintf("github api: status %d: %s", e.StatusCode, e.Message)
}

// IsNotFound reports whether err is a GitHub 404.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

func setGitHubHeaders(req *http.Request) {
	req.Header.Set("Accept", acceptHeader)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", userAgent)
}

// do executes req, enforces a 2xx status, and decodes the JSON body into out
// (skipped when out is nil). A non-2xx status becomes an *APIError.
func (c *Connector) do(req *http.Request, out any) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("read github response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{StatusCode: resp.StatusCode, Message: githubErrorMessage(body)}
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decode github response: %w", err)
		}
	}
	return nil
}

// githubErrorMessage pulls the human-readable message out of GitHub's error
// envelope ({"message": "..."}), falling back to the raw body.
func githubErrorMessage(body []byte) string {
	var envelope struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Message != "" {
		return envelope.Message
	}
	return string(body)
}
