package business

import (
	"regexp"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GitHubFileFilter occupies the source's existing non-secret config envelope.
// Empty (including legacy NULL config) preserves the all-file-types behavior.
type GitHubFileFilter struct {
	FileExtensions []string `json:"file_extensions,omitempty"`
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
