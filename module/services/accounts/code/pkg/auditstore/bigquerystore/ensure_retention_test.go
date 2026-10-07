package bigquerystore

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/stretchr/testify/require"
	bq "google.golang.org/api/bigquery/v2"
	"google.golang.org/api/option"
)

func TestEnsureRejectsUnsafeRetentionMetadata(t *testing.T) {
	store := &Store{retention: 30 * 24 * time.Hour}
	for _, spec := range store.tableSpecs() {
		t.Run(spec.name, func(t *testing.T) {
			require.NoError(t, conforms(spec, metadataFor(spec)), "daily partitions without table expiry retain the configured window")
			for _, granularity := range []bigquery.TimePartitioningType{
				bigquery.HourPartitioningType, bigquery.MonthPartitioningType, bigquery.YearPartitioningType,
			} {
				t.Run(string(granularity), func(t *testing.T) {
					meta := metadataFor(spec)
					meta.TimePartitioning.Type = granularity
					require.ErrorContains(t, conforms(spec, meta), "DAY")
				})
			}
			t.Run("whole table expiry", func(t *testing.T) {
				meta := metadataFor(spec)
				meta.ExpirationTime = occurredAt.Add(24 * time.Hour)
				require.ErrorContains(t, conforms(spec, meta), "table expires")
			})
		})
	}
}

type retentionMetadataTransport func(*http.Request) (*http.Response, error)

func (f retentionMetadataTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// A dataset default may add whole-table expiry to a newly created table even
// though the creation request only specifies partition expiry. Ensure must
// validate the returned table metadata, including on this first startup.
func TestEnsureChecksNewTablesForInheritedExpiration(t *testing.T) {
	for _, inherited := range []bool{false, true} {
		name := "no dataset expiry"
		if inherited {
			name = "dataset expiry inherited"
		}
		t.Run(name, func(t *testing.T) {
			tables := map[string]*bq.Table{}
			var created []string
			transport := retentionMetadataTransport(func(req *http.Request) (*http.Response, error) {
				code := http.StatusOK
				var body any
				switch {
				case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/datasets/audit"):
					body = &bq.Dataset{DatasetReference: &bq.DatasetReference{ProjectId: "test-project", DatasetId: "audit"}}
				case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/tables"):
					var table bq.Table
					require.NoError(t, json.NewDecoder(req.Body).Decode(&table))
					if inherited {
						table.ExpirationTime = occurredAt.Add(24 * time.Hour).UnixMilli()
					}
					created = append(created, table.TableReference.TableId)
					tables[table.TableReference.TableId] = &table
					body = &table
				case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/tables/"):
					id := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
					if table, ok := tables[id]; ok {
						body = table
					} else {
						code = http.StatusNotFound
						body = map[string]any{"error": map[string]any{"code": code, "message": "table not found"}}
					}
				default:
					t.Fatalf("unexpected BigQuery request: %s %s", req.Method, req.URL.Path)
				}
				encoded, err := json.Marshal(body)
				require.NoError(t, err)
				return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(encoded))), Request: req}, nil
			})
			client, err := bigquery.NewClient(context.Background(), "test-project", option.WithHTTPClient(&http.Client{Transport: transport}), option.WithEndpoint("https://audit.example.test"))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, client.Close()) })
			store, err := New(client, Config{Dataset: "audit", ContentDetailRetention: 30 * 24 * time.Hour})
			require.NoError(t, err)
			err = store.Ensure(context.Background())
			if inherited {
				require.ErrorContains(t, err, "table expires")
				require.Equal(t, []string{EventsTable}, created, "startup stops at the first unsafe table")
			} else {
				require.NoError(t, err)
				require.Equal(t, []string{EventsTable, DetailsTable}, created)
				require.NoError(t, store.Ensure(context.Background()), "an existing valid daily table remains accepted")
			}
		})
	}
}
