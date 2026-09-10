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

// TestDatasourceCatalog_DeclaresAPICredentialKinds proves the API connector
// declares the credential kinds it accepts (issue #472: the catalog metadata is
// how a connector advertises the credential inputs a client must collect), and
// that the bespoke GitHub connector declares none.
func TestDatasourceCatalog_DeclaresAPICredentialKinds(t *testing.T) {
	catalog := datasourceCatalog()
	byProvider := map[gen.DatasourceProvider]*gen.DatasourceProviderDescriptor{}
	for _, p := range catalog.GetProviders() {
		byProvider[p.GetProvider()] = p
	}

	api := byProvider[gen.DatasourceProvider_DATASOURCE_PROVIDER_API]
	if api == nil {
		t.Fatal("API provider missing from catalog")
	}
	want := map[gen.ApiCredentialKind]bool{
		gen.ApiCredentialKind_API_CREDENTIAL_KIND_BEARER: true,
		gen.ApiCredentialKind_API_CREDENTIAL_KIND_BASIC:  true,
		gen.ApiCredentialKind_API_CREDENTIAL_KIND_HEADER: true,
		gen.ApiCredentialKind_API_CREDENTIAL_KIND_QUERY:  true,
		gen.ApiCredentialKind_API_CREDENTIAL_KIND_OAUTH2: true,
	}
	got := map[gen.ApiCredentialKind]bool{}
	for _, k := range api.GetSupportedCredentialKinds() {
		got[k] = true
	}
	for k := range want {
		if !got[k] {
			t.Errorf("API connector does not declare credential kind %v", k)
		}
	}

	if github := byProvider[gen.DatasourceProvider_DATASOURCE_PROVIDER_GITHUB]; github == nil {
		t.Fatal("GitHub provider missing from catalog")
	} else if len(github.GetSupportedCredentialKinds()) != 0 {
		t.Errorf("GitHub is bespoke; it must declare no api credential kinds, got %v", github.GetSupportedCredentialKinds())
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
	})

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
	})

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
	})

	if out.GetLastIngestedAt() != nil {
		t.Errorf("last_ingested_at = %v, want unset", out.GetLastIngestedAt())
	}
	if out.GetLastIngestedCommit() != "" {
		t.Errorf("last_ingested_commit = %q, want empty", out.GetLastIngestedCommit())
	}
}
