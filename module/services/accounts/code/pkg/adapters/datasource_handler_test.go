package adapters

import (
	"testing"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// TestAPICredentialKindRoundTrip proves every credential kind maps between the
// proto enum and the business string in both directions, including the query
// and OAuth2 kinds added for the generalized credential model.
func TestAPICredentialKindRoundTrip(t *testing.T) {
	kinds := []gen.ApiCredentialKind{
		gen.ApiCredentialKind_API_CREDENTIAL_KIND_BEARER,
		gen.ApiCredentialKind_API_CREDENTIAL_KIND_BASIC,
		gen.ApiCredentialKind_API_CREDENTIAL_KIND_HEADER,
		gen.ApiCredentialKind_API_CREDENTIAL_KIND_QUERY,
		gen.ApiCredentialKind_API_CREDENTIAL_KIND_OAUTH2,
	}
	for _, kind := range kinds {
		if got := apiCredentialKindToProto(apiCredentialKindFromProto(kind)); got != kind {
			t.Errorf("round trip %v -> %q -> %v", kind, apiCredentialKindFromProto(kind), got)
		}
	}
	if business.APICredentialKindOAuth2 != "oauth2" || business.APICredentialKindQuery != "query" {
		t.Fatalf("business kind constants drifted: %q %q", business.APICredentialKindOAuth2, business.APICredentialKindQuery)
	}
}

// TestDatasourceCatalog_ProjectsTheRegistry proves the catalog is the host's
// connector registry: GitHub conformant and accepting new sources, with its
// interface, readers model, budget and config; the providers still off the
// envelope listed, flagged with their gap, and accepting none.
func TestDatasourceCatalog_ProjectsTheRegistry(t *testing.T) {
	svc, _ := business.NewService(nil)
	svc.SetDatasourceConnector(nil, nil, "")
	catalog := datasourceCatalog(svc.DatasourceCatalog())
	byKey := map[string]*gen.DatasourceProviderDescriptor{}
	for _, p := range catalog.GetProviders() {
		byKey[p.GetConnector()] = p
	}
	if len(byKey) != 4 {
		t.Fatalf("catalog = %v, want the four registered providers", catalog.GetProviders())
	}
	github := byKey[business.DatasourceProviderGitHub]
	if github.GetProvider() != gen.DatasourceProvider_DATASOURCE_PROVIDER_GITHUB || !github.GetConformant() || !github.GetAcceptsNewSources() ||
		github.GetConformanceGap() != "" ||
		github.GetInterface() != gen.DatasourceConnectorInterface_DATASOURCE_CONNECTOR_INTERFACE_FILES ||
		github.GetReadersModel() != gen.DatasourceReadersModel_DATASOURCE_READERS_MODEL_SOURCE_SCOPED ||
		github.GetBudget().GetMaxItemsPerCall() != 1000 || len(github.GetCredentialModes()) != 3 ||
		len(github.GetConfigFields()) != 4 || github.GetConfigFields()[0].GetKey() != "repo" || !github.GetSupportsWebhook() {
		t.Fatalf("GitHub entry = %v", github)
	}
	for _, key := range []string{business.DatasourceProviderAPI, business.DatasourceProviderCrawler, business.DatasourceProviderUpload} {
		p := byKey[key]
		if p == nil || p.GetConformant() || p.GetAcceptsNewSources() || p.GetConformanceGap() == "" || p.GetBudget() != nil ||
			p.GetProvider() == gen.DatasourceProvider_DATASOURCE_PROVIDER_UNSPECIFIED {
			t.Fatalf("%s entry = %v, want listed, non-conformant, with a gap, accepting no new source", key, p)
		}
	}
	if datasourceCatalog(nil).GetProviders() != nil {
		t.Fatal("an unconfigured connector offers nothing")
	}
}

// TestDatasourceSourceToProto_FlagsNonConformantProviders proves an existing
// source says whether its provider meets the connector envelope, so a client
// can flag one that does not (the owner's settled question 7).
func TestDatasourceSourceToProto_FlagsNonConformantProviders(t *testing.T) {
	svc, _ := business.NewService(nil)
	svc.SetDatasourceConnector(nil, nil, "")
	reg := svc.DatasourceConnectors()
	gh := datasourceSourceToProto(&business.DatasourceSource{Provider: business.DatasourceProviderGitHub}, reg)
	if !gh.GetConformant() || gh.GetConformanceGap() != "" {
		t.Fatalf("github source = conformant %v gap %q", gh.GetConformant(), gh.GetConformanceGap())
	}
	crawl := datasourceSourceToProto(&business.DatasourceSource{Provider: business.DatasourceProviderCrawler, Crawler: &business.CrawlerDatasourceConfig{SitemapURL: "https://docs.example.com/sitemap.xml"}}, reg)
	if crawl.GetConformant() || crawl.GetConformanceGap() == "" {
		t.Fatalf("crawler source = conformant %v gap %q, want flagged with its gap", crawl.GetConformant(), crawl.GetConformanceGap())
	}
	if unconfigured := datasourceSourceToProto(&business.DatasourceSource{Provider: business.DatasourceProviderGitHub}, nil); unconfigured.GetConformant() {
		t.Fatal("with no registry nothing is reported conformant")
	}
}

// TestDatasourceSourceToProto_ProjectsIngestProvenance proves the ingest cursor
// reaches the wire for a github source, whose deliveries the change-set compiler
// tracks. Its last_synced_at stays unset: no github path writes that column, so
// a client that reads only last_synced_at sees a repo source as never ingested.
func TestDatasourceSourceToProto_ProjectsIngestProvenance(t *testing.T) {
	ingested := time.Date(2026, 3, 2, 11, 30, 0, 0, time.UTC)
	out := datasourceSourceToProto(&business.DatasourceSource{
		ID:                 "11111111-1111-1111-1111-111111111111",
		OrgID:              "22222222-2222-2222-2222-222222222222",
		Provider:           business.DatasourceProviderGitHub,
		Status:             business.DatasourceStatusActive,
		LastIngestedAt:     &ingested,
		LastIngestedCommit: "0f1e2d3c4b5a69788796a5b4c3d2e1f009182736",
	}, nil)

	if got := out.GetLastIngestedAt().AsTime(); !got.Equal(ingested) {
		t.Errorf("last_ingested_at = %s, want %s", got, ingested)
	}
	if got := out.GetLastIngestedCommit(); got != "0f1e2d3c4b5a69788796a5b4c3d2e1f009182736" {
		t.Errorf("last_ingested_commit = %q", got)
	}
	if out.GetLastSyncedAt() != nil {
		t.Errorf("last_synced_at = %v, want unset for a github source", out.GetLastSyncedAt())
	}
}

// TestDatasourceSourceToProto_PullProviderKeepsSyncClock is the other half of the
// exclusivity the wire documents: a pulled provider advances last_synced_at and
// never enters the change-set compiler, so its ingest cursor must stay absent
// rather than project a zero instant a client would render as an ingest.
func TestDatasourceSourceToProto_PullProviderKeepsSyncClock(t *testing.T) {
	synced := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	out := datasourceSourceToProto(&business.DatasourceSource{
		ID:           "11111111-1111-1111-1111-111111111111",
		OrgID:        "22222222-2222-2222-2222-222222222222",
		Provider:     business.DatasourceProviderAPI,
		Status:       business.DatasourceStatusActive,
		LastSyncedAt: &synced,
	}, nil)

	if got := out.GetLastSyncedAt().AsTime(); !got.Equal(synced) {
		t.Errorf("last_synced_at = %s, want %s", got, synced)
	}
	if out.GetLastIngestedAt() != nil {
		t.Errorf("last_ingested_at = %v, want unset for a pulled provider", out.GetLastIngestedAt())
	}
	if out.GetLastIngestedCommit() != "" {
		t.Errorf("last_ingested_commit = %q, want empty for a pulled provider", out.GetLastIngestedCommit())
	}
}

// TestDatasourceSourceToProto_OmitsUnadvancedCursor proves a source no delivery
// has reached yet projects an absent timestamp rather than the zero instant, so a
// client can render "never ingested" instead of January 1st year one.
func TestDatasourceSourceToProto_OmitsUnadvancedCursor(t *testing.T) {
	out := datasourceSourceToProto(&business.DatasourceSource{
		ID:       "11111111-1111-1111-1111-111111111111",
		OrgID:    "22222222-2222-2222-2222-222222222222",
		Provider: business.DatasourceProviderGitHub,
		Status:   business.DatasourceStatusActive,
	}, nil)

	if out.GetLastIngestedAt() != nil {
		t.Errorf("last_ingested_at = %v, want unset", out.GetLastIngestedAt())
	}
	if out.GetLastIngestedCommit() != "" {
		t.Errorf("last_ingested_commit = %q, want empty", out.GetLastIngestedCommit())
	}
}

// TestDatasourceSourceToProto_ProjectsDegradedStatusAndReason covers the state a
// tenant most needs to see and could not: the compiler parks a source it cannot
// make progress on and records why, but the wire had no degraded value and no
// reason field, so the projection fell through to UNSPECIFIED and dropped the
// explanation. A source that had silently stopped ingesting was indistinguishable
// from one whose status was simply unknown.
func TestDatasourceSourceToProto_ProjectsDegradedStatusAndReason(t *testing.T) {
	const reason = "snapshot manifest is 1048576 bytes, over the 983040-byte ingest limit"
	out := datasourceSourceToProto(&business.DatasourceSource{
		ID:           "11111111-1111-1111-1111-111111111111",
		OrgID:        "22222222-2222-2222-2222-222222222222",
		Provider:     business.DatasourceProviderGitHub,
		Status:       business.DatasourceStatusDegraded,
		StatusReason: reason,
	}, nil)

	if got := out.GetStatus(); got != gen.DatasourceStatus_DATASOURCE_STATUS_DEGRADED {
		t.Errorf("status = %v, want DATASOURCE_STATUS_DEGRADED", got)
	}
	if got := out.GetStatusReason(); got != reason {
		t.Errorf("status_reason = %q, want %q", got, reason)
	}
}

func TestDatasourceSourceToProto_ProjectsBoundaryLabel(t *testing.T) {
	out := datasourceSourceToProto(&business.DatasourceSource{
		ID:             "11111111-1111-1111-1111-111111111111",
		OrgID:          "22222222-2222-2222-2222-222222222222",
		Provider:       business.DatasourceProviderGitHub,
		Status:         business.DatasourceStatusActive,
		BoundaryNodeID: "33333333-3333-3333-3333-333333333333",
		BoundaryLabel:  "guides",
	}, nil)
	if got := out.GetBoundaryNodeId(); got != "33333333-3333-3333-3333-333333333333" {
		t.Errorf("boundary_node_id = %q", got)
	}
	if got := out.GetBoundaryLabel(); got != "guides" {
		t.Errorf("boundary_label = %q, want %q", got, "guides")
	}
}

// TestDatasourceStatusToProto_MapsEveryStoredStatus pins the whole switch rather
// than the one arm this change added. A stored status that loses its case falls
// through to UNSPECIFIED, which on the wire is indistinguishable from "unknown"
// — exactly the defect that hid a degraded source. The unrecognised-value arm
// pins that fallback as deliberate, so a status this projection has never heard
// of stays UNSPECIFIED instead of being reported as a real one.
func TestDatasourceStatusToProto_MapsEveryStoredStatus(t *testing.T) {
	for _, testCase := range []struct {
		stored string
		want   gen.DatasourceStatus
	}{
		{business.DatasourceStatusActive, gen.DatasourceStatus_DATASOURCE_STATUS_ACTIVE},
		{business.DatasourceStatusPaused, gen.DatasourceStatus_DATASOURCE_STATUS_PAUSED},
		{business.DatasourceStatusDegraded, gen.DatasourceStatus_DATASOURCE_STATUS_DEGRADED},
		{"a-status-this-binary-predates", gen.DatasourceStatus_DATASOURCE_STATUS_UNSPECIFIED},
	} {
		if got := datasourceStatusToProto(testCase.stored); got != testCase.want {
			t.Errorf("datasourceStatusToProto(%q) = %v, want %v", testCase.stored, got, testCase.want)
		}
	}
}

// The projection is the only place an administrator sees which filter a
// connected source is running under, so the stored allowlist has to reach the
// GitHub config on the wire — and an empty one has to project as an empty
// repeated field, which every client reads as "all file types".
func TestDatasourceSourceToProto_ProjectsFileExtensions(t *testing.T) {
	filtered := datasourceSourceToProto(&business.DatasourceSource{
		ID:             "11111111-1111-1111-1111-111111111111",
		OrgID:          "22222222-2222-2222-2222-222222222222",
		Provider:       business.DatasourceProviderGitHub,
		Status:         business.DatasourceStatusActive,
		Repo:           "acme/docs",
		Paths:          []string{"docs"},
		FileExtensions: []string{".md", ".mdx"},
	}, nil)
	got := filtered.GetGithub().GetFileExtensions()
	if len(got) != 2 || got[0] != ".md" || got[1] != ".mdx" {
		t.Errorf("file_extensions = %v", got)
	}

	legacy := datasourceSourceToProto(&business.DatasourceSource{
		ID:       "11111111-1111-1111-1111-111111111111",
		OrgID:    "22222222-2222-2222-2222-222222222222",
		Provider: business.DatasourceProviderGitHub,
		Status:   business.DatasourceStatusActive,
		Repo:     "acme/docs",
	}, nil)
	// Assert the config block is present before reading through it. The generated
	// getters are nil-safe, so GetGithub().GetFileExtensions() reads as an empty
	// allowlist whether the projection carries an unfiltered source or drops its
	// GitHub config entirely — and dropping it would take repo, paths and branch
	// with it, which is what the source table renders.
	if legacy.GetGithub() == nil {
		t.Fatal("an unfiltered source must still project its github config")
	}
	if got := legacy.GetGithub().GetRepo(); got != "acme/docs" {
		t.Errorf("legacy repo = %q", got)
	}
	if got := legacy.GetGithub().GetFileExtensions(); len(got) != 0 {
		t.Errorf("legacy file_extensions = %v, want empty", got)
	}
}
