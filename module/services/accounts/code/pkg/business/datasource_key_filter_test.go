package business

import (
	"strings"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestUploadKeyScope holds an object-storage source's key filter to its
// contract: patterns match the key with the prefix removed, unanchored and
// case-sensitive; an empty include admits everything; exclude wins; and file
// types intersect, case-insensitively, as on GitHub.
func TestUploadKeyScope(t *testing.T) {
	cases := []struct {
		name    string
		cfg     UploadDatasourceConfig
		admit   []string
		exclude []string
	}{
		{
			name:  "no filter admits every key under the prefix",
			cfg:   UploadDatasourceConfig{Prefix: "kb/"},
			admit: []string{"kb/a.md", "kb/deep/b.pdf"},
		},
		{
			name:    "patterns see the key relative to the prefix",
			cfg:     UploadDatasourceConfig{Prefix: "kb/", IncludePatterns: []string{`^guides/`}},
			admit:   []string{"kb/guides/a.md"},
			exclude: []string{"kb/notes/guides/a.md", "kb/a.md"},
		},
		{
			name:    "exclude wins over include",
			cfg:     UploadDatasourceConfig{Prefix: "kb/", IncludePatterns: []string{`\.md$`}, ExcludePatterns: []string{`^drafts/`}},
			admit:   []string{"kb/guides/a.md"},
			exclude: []string{"kb/drafts/a.md", "kb/guides/a.pdf"},
		},
		{
			name:    "patterns are case-sensitive unless they fold case",
			cfg:     UploadDatasourceConfig{Prefix: "kb/", IncludePatterns: []string{`^Policies/`, `(?i)^handbook/`}},
			admit:   []string{"kb/Policies/a.md", "kb/HANDBOOK/a.md"},
			exclude: []string{"kb/policies/a.md"},
		},
		{
			name:    "file types intersect with the patterns, case-insensitively",
			cfg:     UploadDatasourceConfig{Prefix: "kb/", IncludePatterns: []string{`^guides/`}, FileExtensions: []string{".md"}},
			admit:   []string{"kb/guides/A.MD"},
			exclude: []string{"kb/guides/a.pdf", "kb/other/a.md"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inScope := uploadKeyScope(&tc.cfg)
			for _, key := range tc.admit {
				if !inScope(key) {
					t.Errorf("%q is excluded, want admitted", key)
				}
			}
			for _, key := range tc.exclude {
				if inScope(key) {
					t.Errorf("%q is admitted, want excluded", key)
				}
			}
		})
	}
}

// TestNormalizeKeyPatternsRefusesByName: every refusal is INVALID_ARGUMENT and
// names the offending element, in the message a client reads and in the
// BadRequest violation a client can decode.
func TestNormalizeKeyPatternsRefusesByName(t *testing.T) {
	tooMany := make([]string, maxKeyPatterns+1)
	for i := range tooMany {
		tooMany[i] = "a"
	}
	cases := []struct {
		name     string
		patterns []string
		field    string
		mention  string
	}{
		{"an invalid RE2 expression", []string{`\.md$`, `(unclosed`}, "upload.include_patterns[1]", `"(unclosed"`},
		{"a backreference RE2 does not support", []string{`(a)\1`}, "upload.include_patterns[0]", `"(a)\\1"`},
		{"an empty pattern", []string{"  "}, "upload.include_patterns[0]", "empty"},
		{"a pattern over the length cap", []string{strings.Repeat("a", maxKeyPatternLength+1)}, "upload.include_patterns[0]", "256"},
		{"more patterns than the cap", tooMany, "upload.include_patterns", "16"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeKeyPatterns("upload.include_patterns", tc.patterns)
			st := status.Convert(err)
			if err == nil || st.Code() != codes.InvalidArgument {
				t.Fatalf("err = %v, want INVALID_ARGUMENT", err)
			}
			if !strings.HasPrefix(st.Message(), tc.field+": ") || !strings.Contains(st.Message(), tc.mention) {
				t.Fatalf("message %q does not name %s and %s", st.Message(), tc.field, tc.mention)
			}
			var field string
			for _, d := range st.Details() {
				if br, ok := d.(*errdetails.BadRequest); ok && len(br.GetFieldViolations()) == 1 {
					field = br.GetFieldViolations()[0].GetField()
				}
			}
			if field != tc.field {
				t.Fatalf("BadRequest field = %q, want %q", field, tc.field)
			}
		})
	}
	got, err := normalizeKeyPatterns("upload.exclude_patterns", []string{" ^drafts/ ", "^drafts/", `\.tmp$`})
	if err != nil || strings.Join(got, ",") != `^drafts/,\.tmp$` {
		t.Fatalf("normalize = %v, %v; want trimmed and de-duplicated", got, err)
	}
}
