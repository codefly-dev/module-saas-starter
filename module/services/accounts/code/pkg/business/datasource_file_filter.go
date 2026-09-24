package business

import (
	"regexp"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GitHubSourceConfig is a GitHub source's non-secret config envelope. Empty
// (including legacy NULL config) preserves the all-file-types behavior and an
// envelope-backed credential.
type GitHubSourceConfig struct {
	FileExtensions []string `json:"file_extensions,omitempty"`
	// CredentialKind is set only for a source that holds no credential envelope
	// at all, and then names why: githubCredentialKindPublic. A source with an
	// envelope leaves it empty — the envelope names its own kind — and the store
	// clears it whenever an envelope is written, so the two never disagree.
	CredentialKind string `json:"credential_kind,omitempty"`
}

var fileExtensionPattern = regexp.MustCompile(`^\.[a-z0-9][a-z0-9._-]{0,31}$`)

func normalizeFileExtensions(values []string) ([]string, error) {
	if len(values) > 32 {
		return nil, status.Error(codes.InvalidArgument, "too many file extensions (maximum 32)")
	}
	var out []string
	seen := map[string]bool{}
	for _, raw := range values {
		value := strings.ToLower(strings.TrimSpace(raw))
		if !fileExtensionPattern.MatchString(value) {
			return nil, status.Error(codes.InvalidArgument, "file extensions must be suffixes such as .md, not paths or globs")
		}
		if !seen[value] {
			out = append(out, value)
			seen[value] = true
		}
	}
	return out, nil
}

func fileTypeAllowed(path string, extensions []string) bool {
	if len(extensions) == 0 {
		return true
	}
	name := strings.ToLower(path)
	for _, extension := range extensions {
		if strings.HasSuffix(name, extension) {
			return true
		}
	}
	return false
}
