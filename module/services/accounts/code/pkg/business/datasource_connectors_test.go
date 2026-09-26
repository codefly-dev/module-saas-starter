package business_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"accounts/pkg/business"
	"accounts/pkg/datasource/connector"
)

// suiteCall is how a connector's own tests run the conformance suite.
var suiteCall = regexp.MustCompile(`connectortest\.Run[A-Z][A-Za-z]*\(t,`)

// TestEveryConformantConnectorRunsTheSuite is the conformance gate, the same
// shape as the boundary gate: a connector the host registers as conformant must
// have a conformance test beside it, in pkg/datasource/<key>/, that runs the
// shared suite. Registering one as conformant without that test is a red build.
func TestEveryConformantConnectorRunsTheSuite(t *testing.T) {
	svc, _ := business.NewService(nil)
	svc.SetDatasourceConnector(nil, nil, "")
	conformant := svc.DatasourceConnectors().Conformant()
	if len(conformant) == 0 {
		t.Fatal("the host registers no conformant connector")
	}
	for _, c := range conformant {
		key := c.Descriptor().Key
		dir := filepath.Join("..", "datasource", key)
		matches, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, file := range matches {
			body, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if suiteCall.Match(body) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("connector %q is registered conformant but no test in %s runs the conformance suite", key, dir)
		}
	}
}

// TestLegacyProvidersAreRegisteredWithTheirGap holds the registry to the owner's
// settled rule for providers still off the envelope: registered, flagged
// non-conformant with a named gap, not offered, not admitted.
func TestLegacyProvidersAreRegisteredWithTheirGap(t *testing.T) {
	svc, _ := business.NewService(nil)
	svc.SetDatasourceConnector(nil, nil, "")
	reg := svc.DatasourceConnectors()
	for _, key := range []string{business.DatasourceProviderAPI, business.DatasourceProviderCrawler, business.DatasourceProviderUpload} {
		d, ok := reg.Descriptor(key)
		if !ok || d.Conformant || d.Gap == "" {
			t.Fatalf("%s: descriptor %+v, want registered non-conformant with a gap", key, d)
		}
		if err := reg.AdmitNewSource(key); err != connector.ErrNonConformant {
			t.Fatalf("%s: admission = %v, want ErrNonConformant", key, err)
		}
	}
	for _, e := range svc.DatasourceCatalog() {
		if e.AcceptsNewSources != (e.Descriptor.Conformant && e.Descriptor.Readers != connector.ReadersTranslated) {
			t.Fatalf("the catalog says %s accepts new sources = %v", e.Descriptor.Key, e.AcceptsNewSources)
		}
	}
	if d, ok := reg.Descriptor(business.DatasourceProviderGitHub); !ok || !d.Conformant || d.Readers != connector.ReadersSourceScoped {
		t.Fatalf("github: %+v, want conformant and source-scoped", d)
	}
}
