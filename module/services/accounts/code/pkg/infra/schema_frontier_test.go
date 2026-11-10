package infra

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestSchemaRefusal(t *testing.T) {
	cases := []struct {
		name    string
		version int64
		dirty   bool
		err     error
		want    string // substring of the refusal; empty means served
	}{
		{"current", requiredSchemaVersion, false, nil, ""},
		{"store ahead of the binary (accounts rolled back)", requiredSchemaVersion + 3, false, nil, ""},
		{"behind names the missing range", requiredSchemaVersion - 2, false, nil, "migrations 71 through 72 are not applied"},
		{"mid-migration (dirty at the required version)", requiredSchemaVersion, true, nil, "marked dirty"},
		{"failed migration (dirty below)", requiredSchemaVersion - 1, true, nil, "marked dirty"},
		{"unmigrated", 0, false, pgx.ErrNoRows, "has not been migrated"},
		{"no read grant is a store behind migration 72", 0, false, &pgconn.PgError{Code: "42501"}, "has not applied migration 72"},
		{"other read failure is surfaced", 0, false, errors.New("connection reset"), "connection reset"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := schemaRefusal(c.version, c.dirty, c.err)
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("refused a store the binary can serve: %v", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("refusal = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

// The binary requires exactly the frontier the store ships beside it: adding a
// migration without moving requiredSchemaVersion would release an accounts
// that serves against a schema it does not know.
func TestRequiredSchemaVersionIsTheShippedFrontier(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "..", "store", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the store ledger: %v", err)
	}
	up := regexp.MustCompile(`^(\d+)_.+\.up\.sql$`)
	var frontier int
	for _, e := range entries {
		if m := up.FindStringSubmatch(e.Name()); m != nil {
			n, _ := strconv.Atoi(m[1])
			frontier = max(frontier, n)
		}
	}
	if frontier != requiredSchemaVersion {
		t.Fatalf("the store ledger's frontier is %d and accounts requires %d: move requiredSchemaVersion in schema_frontier.go with the migration", frontier, requiredSchemaVersion)
	}
}
