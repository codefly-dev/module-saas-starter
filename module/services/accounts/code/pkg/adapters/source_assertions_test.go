package adapters

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Shared machinery for the tests in this package that assert on SOURCE rather than on
// behaviour — the factor gates, the reserved-audience guards, the Connect cookie lift.
//
// It lives in its own file because three test files use it. It was defined in the one
// named for a single finding, which is how a helper becomes invisible to the next
// person who needs it (R1019-N12/f7).

// executableSource is the file with every comment removed, so prose can neither
// satisfy nor break a source assertion. A comment is not code; a check that cannot
// tell them apart is satisfied by writing the answer in a comment.
func executableSource(t *testing.T, name string) string {
	t.Helper()
	source, err := os.ReadFile(name)
	require.NoError(t, err)
	fileSet := token.NewFileSet()
	// ParseComments is required: without it File.Comments is EMPTY and this function
	// silently strips nothing, which is a check that looks like it works. It was that
	// way until a mutation test proved a commented-out guard still satisfied a caller.
	parsed, err := parser.ParseFile(fileSet, name, source,
		parser.ParseComments|parser.SkipObjectResolution)
	require.NoError(t, err)

	stripped := append([]byte(nil), source...)
	for _, group := range parsed.Comments {
		// Position().Offset is the canonical byte offset. Deriving one from Pos() and
		// the file's Base() by hand is where this silently became a no-op.
		start := fileSet.Position(group.Pos()).Offset
		end := fileSet.Position(group.End()).Offset
		for i := start; i < end && i < len(stripped); i++ {
			if stripped[i] != '\n' {
				stripped[i] = ' '
			}
		}
	}
	return string(stripped)
}

// The guard against that failure mode: this function must actually remove a comment,
// and must leave code alone. Asserted against a file written for the purpose — asserting
// against this file would compare its own assertion literals, which are code, and a
// needle that appears in both can never be shown absent.
func TestExecutableSourceRemovesComments(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "sample.go")
	require.NoError(t, os.WriteFile(name, []byte(`package sample

// a line comment naming guardedCall()
func f() {
	/* a block comment naming guardedCall() */
	guardedCall()
}
`), 0o600))

	text := executableSource(t, name)
	require.Contains(t, text, "\tguardedCall()", "the executable call must survive")
	require.NotContains(t, text, "a line comment", "a line comment must not survive")
	require.NotContains(t, text, "a block comment", "a block comment must not survive")
	require.Equal(t, 1, strings.Count(text, "guardedCall()"),
		"only the executable occurrence may remain; the two in comments must be gone")
}
