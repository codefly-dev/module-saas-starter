//go:build !race_only

package business

// The conformance record is prose, and prose drifts from code silently. This
// holds it to the registry the way release-gates.test.mjs holds RELEASE_GATES.md
// to the enforced set: every registered provider has a section, every section
// names a registered provider, and every non-conformant provider's tenant-facing
// gap sentence appears in its own section verbatim.
//
// The last of those is the one that matters most. The gap sentence is carried on
// the wire for every source of its provider and shown to a tenant to explain why
// the provider takes no new sources, so it is read far more often than this
// document — and it is the thing most likely to be edited without the reasoning
// behind it being revisited. Object storage's said "a removed object is never
// removed" while runUploadSync had been applying deletions through its closing
// listing for some time, which told every tenant of such a source that their
// deletions were being ignored.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const conformanceRecordPath = "../datasource/connector/CONFORMANCE.md"

func readConformanceRecord(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Clean(conformanceRecordPath))
	if err != nil {
		t.Fatalf("read the conformance record: %v", err)
	}
	return string(body)
}

// sections splits the record into its `## <key> …` sections, keyed by the first
// word of each heading — which is the connector's registry key.
func conformanceSections(record string) map[string]string {
	out := map[string]string{}
	key := ""
	var body strings.Builder
	flush := func() {
		if key != "" {
			out[key] = body.String()
		}
		body.Reset()
	}
	for _, line := range strings.Split(record, "\n") {
		if heading, found := strings.CutPrefix(line, "## "); found {
			flush()
			key = strings.Fields(heading)[0]
			continue
		}
		body.WriteString(line)
		body.WriteString("\n")
	}
	flush()
	return out
}

// registryKeys is every provider the host registers, conformant or not. It is
// read from the constants the registry is built from rather than by standing up
// a Service, so this test needs no database and no connector configuration.
var conformanceRecordProviders = map[string]string{
	DatasourceProviderGitHub:  "",
	DatasourceProviderAPI:     gapAPIDatasource,
	DatasourceProviderCrawler: gapCrawlerDatasource,
	DatasourceProviderUpload:  gapUploadDatasource,
}

func TestConformanceRecordCoversEveryRegisteredProvider(t *testing.T) {
	sections := conformanceSections(readConformanceRecord(t))
	// The non-connector sections of the document, which name no provider.
	for _, prose := range []string{"Reading", "Summary"} {
		delete(sections, prose)
	}
	for provider := range conformanceRecordProviders {
		if _, ok := sections[provider]; !ok {
			t.Errorf("provider %q is registered but has no section in %s", provider, conformanceRecordPath)
		}
	}
	for key := range sections {
		if _, ok := conformanceRecordProviders[key]; !ok {
			t.Errorf("%s documents %q, which no provider is registered under", conformanceRecordPath, key)
		}
	}
}

// A non-conformant provider's gap is the sentence a tenant is shown. It must
// appear in that provider's own section, so the reasoning behind it is one
// heading away from the words themselves — and so rewording one without the
// other fails here rather than in front of a tenant.
func TestConformanceRecordQuotesEveryRegisteredGap(t *testing.T) {
	sections := conformanceSections(readConformanceRecord(t))
	for provider, gap := range conformanceRecordProviders {
		if gap == "" {
			continue // conformant: there is no gap to quote
		}
		section, ok := sections[provider]
		if !ok {
			continue // reported by the test above
		}
		// The document wraps its prose, so compare on collapsed whitespace.
		if !strings.Contains(collapseSpace(section), collapseSpace(gap)) {
			t.Errorf("the registered gap for %q does not appear in its section of %s:\n  registered: %s",
				provider, conformanceRecordPath, gap)
		}
	}
}

// A conformant provider names no gap, in the registry or in the record.
func TestConformanceRecordNamesNoGapForAConformantProvider(t *testing.T) {
	sections := conformanceSections(readConformanceRecord(t))
	section, ok := sections[DatasourceProviderGitHub]
	if !ok {
		t.Fatalf("github has no section in %s", conformanceRecordPath)
	}
	if strings.Contains(section, "Registered gap:") {
		t.Errorf("%s records a gap for github, which is registered conformant", conformanceRecordPath)
	}
}

func collapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }
