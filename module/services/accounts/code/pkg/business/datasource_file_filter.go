package business

import (
	"fmt"
	"regexp"
	"strings"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
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

// Bounds on an object-storage source's key patterns. Go's regexp is RE2, so a
// tenant's expression runs in time linear in the key whatever it says; the
// bounds keep the per-key cost and the stored config small.
const (
	maxKeyPatterns      = 16
	maxKeyPatternLength = 256
)

// keyFilterRefused is the refusal of one key-filter field: INVALID_ARGUMENT
// with a BadRequest violation naming the field, and a message that starts with
// the same path, which is what a client without the detail types reads.
func keyFilterRefused(field, reason string) error {
	base := status.New(codes.InvalidArgument, field+": "+reason)
	detailed, err := base.WithDetails(&errdetails.BadRequest{FieldViolations: []*errdetails.BadRequest_FieldViolation{{Field: field, Description: reason}}})
	if err != nil {
		return base.Err()
	}
	return detailed.Err()
}

// normalizeKeyPatterns trims, de-duplicates and compiles one pattern list,
// refusing an invalid expression with its field path and the pattern named.
func normalizeKeyPatterns(field string, values []string) ([]string, error) {
	if len(values) > maxKeyPatterns {
		return nil, keyFilterRefused(field, fmt.Sprintf("at most %d patterns", maxKeyPatterns))
	}
	var out []string
	seen := map[string]bool{}
	for i, raw := range values {
		value := strings.TrimSpace(raw)
		path := fmt.Sprintf("%s[%d]", field, i)
		switch {
		case value == "":
			return nil, keyFilterRefused(path, "a pattern cannot be empty")
		case len(value) > maxKeyPatternLength:
			return nil, keyFilterRefused(path, fmt.Sprintf("a pattern is at most %d characters", maxKeyPatternLength))
		}
		if _, err := regexp.Compile(value); err != nil {
			return nil, keyFilterRefused(path, fmt.Sprintf("%q is not a valid RE2 expression: %v", value, err))
		}
		if !seen[value] {
			out = append(out, value)
			seen[value] = true
		}
	}
	return out, nil
}

// uploadKeyScope is an object-storage source's key filter, the InScope of its
// connector Scope as datasourceFileScope is a GitHub source's: a key is in scope
// when, with the prefix removed, it matches an include pattern (or there are
// none), matches no exclude pattern, and carries an admitted file type. The
// patterns were compiled when the config was accepted; one that no longer
// compiles admits nothing rather than everything.
func uploadKeyScope(cfg *UploadDatasourceConfig) func(key string) bool {
	compile := func(patterns []string) ([]*regexp.Regexp, bool) {
		out := make([]*regexp.Regexp, 0, len(patterns))
		for _, p := range patterns {
			re, err := regexp.Compile(p)
			if err != nil {
				return nil, false
			}
			out = append(out, re)
		}
		return out, true
	}
	include, okInclude := compile(cfg.IncludePatterns)
	exclude, okExclude := compile(cfg.ExcludePatterns)
	if !okInclude || !okExclude {
		return func(string) bool { return false }
	}
	matchesAny := func(res []*regexp.Regexp, s string) bool {
		for _, re := range res {
			if re.MatchString(s) {
				return true
			}
		}
		return false
	}
	return func(key string) bool {
		relative := strings.TrimPrefix(key, cfg.Prefix)
		if len(include) > 0 && !matchesAny(include, relative) {
			return false
		}
		return !matchesAny(exclude, relative) && fileTypeAllowed(relative, cfg.FileExtensions)
	}
}
