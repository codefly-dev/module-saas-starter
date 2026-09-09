package composition

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/codefly-dev/agents/modules/saas-starter/module/tools/modulepackage"
)

func eventsProtoRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "documents/events/v1/entry.proto"), `syntax = "proto3";
package documents.events.v1;
message EntryIngested {
  string id = 1;
  string boundary_id = 2;
}
`)
	writeFile(t, filepath.Join(root, "documents/events/v2/entry.proto"), `syntax = "proto3";
package documents.events.v2;
message EntryIngested {
  string id = 1;
}
`)
	return root
}

func documentsContribution() EventsContribution {
	return EventsContribution{
		Schema:    eventsContributionSchema,
		Namespace: "documents",
		Queues:    []string{"documents.ingest"},
		Publishes: []PublishedEvent{{
			Type:       "documents.entry.ingested",
			Schema:     "documents/events/v1/entry.proto#EntryIngested",
			Visibility: "tenant",
			Partition:  "{tenant_id}/{boundary_id}",
			Retention:  "30d",
		}},
		Consumes: []ConsumedEvent{{
			Type:     "documents.entry.ingested",
			Queue:    "documents.ingest",
			Delivery: "ordered",
		}},
	}
}

func TestBuildEventCatalogMergesPublishesAndConsumes(t *testing.T) {
	catalog, err := buildEventCatalog([]EventsContribution{documentsContribution()}, modulepackage.Manifest{}, eventsProtoRoot(t), eventCatalog{})
	if err != nil {
		t.Fatalf("buildEventCatalog: %v", err)
	}
	if len(catalog.Publishes) != 1 || catalog.Publishes[0].Type != "documents.entry.ingested" {
		t.Fatalf("unexpected publishes: %+v", catalog.Publishes)
	}
	if catalog.Publishes[0].Major != 1 || len(catalog.Publishes[0].Fields) != 2 {
		t.Fatalf("schema fields not resolved: %+v", catalog.Publishes[0])
	}
	if len(catalog.Consumes) != 1 || catalog.Consumes[0].Subscriber != "documents" {
		t.Fatalf("unexpected consumes: %+v", catalog.Consumes)
	}
}

func TestBuildEventCatalogRejectsNamespaceOwnership(t *testing.T) {
	contribution := documentsContribution()
	contribution.Publishes[0].Type = "billing.invoice.paid"
	contribution.Consumes = nil
	_, err := buildEventCatalog([]EventsContribution{contribution}, modulepackage.Manifest{}, eventsProtoRoot(t), eventCatalog{})
	if err == nil || !strings.Contains(err.Error(), "outside namespace") {
		t.Fatalf("expected namespace-ownership error, got %v", err)
	}
}

func TestBuildEventCatalogRejectsUnresolvedSchema(t *testing.T) {
	contribution := documentsContribution()
	contribution.Publishes[0].Schema = "documents/events/v1/entry.proto#Missing"
	contribution.Consumes = nil
	_, err := buildEventCatalog([]EventsContribution{contribution}, modulepackage.Manifest{}, eventsProtoRoot(t), eventCatalog{})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected unresolved-schema error, got %v", err)
	}
}

func TestBuildEventCatalogRejectsDuplicateType(t *testing.T) {
	contribution := documentsContribution()
	contribution.Publishes = append(contribution.Publishes, PublishedEvent{
		Type:       "documents.entry.ingested",
		Schema:     "documents/events/v1/entry.proto#EntryIngested",
		Visibility: "tenant",
	})
	contribution.Consumes = nil
	_, err := buildEventCatalog([]EventsContribution{contribution}, modulepackage.Manifest{}, eventsProtoRoot(t), eventCatalog{})
	if err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Fatalf("expected duplicate-type error, got %v", err)
	}
}

func TestBuildEventCatalogRejectsUnregisteredConsume(t *testing.T) {
	contribution := documentsContribution()
	contribution.Consumes[0].Type = "datasource.source.changed"
	_, err := buildEventCatalog([]EventsContribution{contribution}, modulepackage.Manifest{}, eventsProtoRoot(t), eventCatalog{})
	if err == nil || !strings.Contains(err.Error(), "not a registered published type") {
		t.Fatalf("expected unregistered-consume error, got %v", err)
	}
}

func TestBuildEventCatalogRejectsUndeclaredQueue(t *testing.T) {
	contribution := documentsContribution()
	contribution.Consumes[0].Queue = "documents.other"
	_, err := buildEventCatalog([]EventsContribution{contribution}, modulepackage.Manifest{}, eventsProtoRoot(t), eventCatalog{})
	if err == nil || !strings.Contains(err.Error(), "did not declare") {
		t.Fatalf("expected undeclared-queue error, got %v", err)
	}
}

func TestBuildEventCatalogRejectsBreakingFieldRemoval(t *testing.T) {
	prior := eventCatalog{Schema: eventsCatalogSchema, Publishes: []eventCatalogPublish{{
		Type:  "documents.entry.ingested",
		Major: 1,
		Fields: []eventField{
			{Name: "id", Number: 1, Type: "string"},
			{Name: "boundary_id", Number: 2, Type: "string"},
			{Name: "removed", Number: 3, Type: "string"},
		},
	}}}
	contribution := documentsContribution()
	contribution.Consumes = nil
	_, err := buildEventCatalog([]EventsContribution{contribution}, modulepackage.Manifest{}, eventsProtoRoot(t), prior)
	if err == nil || !strings.Contains(err.Error(), "removes field") {
		t.Fatalf("expected breaking-change error, got %v", err)
	}
}

func TestBuildEventCatalogCatchesBreakingOneofFieldRemoval(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "documents/events/v1/choice.proto"), `syntax = "proto3";
package documents.events.v1;
message Choice {
  string id = 1;
  oneof body {
    string a = 2;
  }
}
`)
	prior := eventCatalog{Schema: eventsCatalogSchema, Publishes: []eventCatalogPublish{{
		Type:  "documents.entry.ingested",
		Major: 1,
		Fields: []eventField{
			{Name: "id", Number: 1, Type: "string"},
			{Name: "a", Number: 2, Type: "string"},
			{Name: "b", Number: 3, Type: "string"},
		},
	}}}
	contribution := documentsContribution()
	contribution.Publishes[0].Schema = "documents/events/v1/choice.proto#Choice"
	contribution.Consumes = nil
	_, err := buildEventCatalog([]EventsContribution{contribution}, modulepackage.Manifest{}, root, prior)
	if err == nil || !strings.Contains(err.Error(), "removes field") {
		t.Fatalf("removing a oneof member without a major bump must fail compose, got %v", err)
	}
}

func TestBuildEventCatalogAllowsBreakingChangeBehindMajorBump(t *testing.T) {
	prior := eventCatalog{Schema: eventsCatalogSchema, Publishes: []eventCatalogPublish{{
		Type:  "documents.entry.ingested",
		Major: 1,
		Fields: []eventField{
			{Name: "id", Number: 1, Type: "string"},
			{Name: "boundary_id", Number: 2, Type: "string"},
		},
	}}}
	contribution := documentsContribution()
	contribution.Publishes[0].Schema = "documents/events/v2/entry.proto#EntryIngested"
	contribution.Consumes = nil
	catalog, err := buildEventCatalog([]EventsContribution{contribution}, modulepackage.Manifest{}, eventsProtoRoot(t), prior)
	if err != nil {
		t.Fatalf("major bump should permit a breaking change: %v", err)
	}
	if catalog.Publishes[0].Major != 2 {
		t.Fatalf("expected major 2, got %d", catalog.Publishes[0].Major)
	}
}

func TestRenderEventCatalogEmitsJSONAndGo(t *testing.T) {
	catalog, err := buildEventCatalog([]EventsContribution{documentsContribution()}, modulepackage.Manifest{}, eventsProtoRoot(t), eventCatalog{})
	if err != nil {
		t.Fatalf("buildEventCatalog: %v", err)
	}
	files, err := renderEventCatalog(catalog)
	if err != nil {
		t.Fatalf("renderEventCatalog: %v", err)
	}
	jsonBody := string(files[EventCatalogOutput])
	if !strings.Contains(jsonBody, `"schema": "codefly/saas/events-catalog/v1"`) || !strings.Contains(jsonBody, `"documents.entry.ingested"`) {
		t.Fatalf("event catalog JSON is malformed:\n%s", jsonBody)
	}
	goBody := string(files[EventGoOutput])
	if !strings.Contains(goBody, "package eventcatalog") || !strings.Contains(goBody, `Type: "documents.entry.ingested"`) {
		t.Fatalf("event catalog Go is malformed:\n%s", goBody)
	}
}

func TestRenderEventCatalogEmitsEmptyArrays(t *testing.T) {
	files, err := renderEventCatalog(eventCatalog{Schema: eventsCatalogSchema, Publishes: []eventCatalogPublish{}, Consumes: []eventCatalogConsume{}})
	if err != nil {
		t.Fatalf("renderEventCatalog: %v", err)
	}
	if !strings.Contains(string(files[EventCatalogOutput]), `"publishes": []`) {
		t.Fatalf("empty catalog must emit an empty publishes array:\n%s", files[EventCatalogOutput])
	}
}

func TestRenderEventCatalogEmitsAsyncAPIAndDocs(t *testing.T) {
	catalog, err := buildEventCatalog([]EventsContribution{documentsContribution()}, modulepackage.Manifest{}, eventsProtoRoot(t), eventCatalog{})
	if err != nil {
		t.Fatalf("buildEventCatalog: %v", err)
	}
	files, err := renderEventCatalog(catalog)
	if err != nil {
		t.Fatalf("renderEventCatalog: %v", err)
	}

	async := string(files[AsyncAPIOutput])
	for _, want := range []string{
		`"asyncapi": "3.0.0"`,
		`"version": "1.0.0"`,
		`"send:documents.entry.ingested"`,
		`"receive:documents.entry.ingested:documents:documents.ingest"`,
		`"action": "send"`,
		`"action": "receive"`,
		`"x-visibility": "tenant"`,
		`"#/channels/documents.entry.ingested"`,
	} {
		if !strings.Contains(async, want) {
			t.Fatalf("asyncapi.json missing %q:\n%s", want, async)
		}
	}

	docs := string(files[CommunicationOutput])
	for _, want := range []string{
		"## documents.entry.ingested",
		"- **Publisher:** documents",
		"- **Consumers:**",
		"documents (queue `documents.ingest`, delivery ordered)",
	} {
		if !strings.Contains(docs, want) {
			t.Fatalf("communication.md missing %q:\n%s", want, docs)
		}
	}
}

func TestRenderAsyncAPIIsDeterministic(t *testing.T) {
	catalog, err := buildEventCatalog([]EventsContribution{documentsContribution()}, modulepackage.Manifest{}, eventsProtoRoot(t), eventCatalog{})
	if err != nil {
		t.Fatalf("buildEventCatalog: %v", err)
	}
	first, err := renderAsyncAPI(catalog)
	if err != nil {
		t.Fatalf("renderAsyncAPI: %v", err)
	}
	second, err := renderAsyncAPI(catalog)
	if err != nil {
		t.Fatalf("renderAsyncAPI: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("asyncapi.json is not byte-deterministic")
	}
}

func TestRenderEventDocsHandlesNoConsumers(t *testing.T) {
	docs := string(renderEventDocs(eventCatalog{
		Schema: eventsCatalogSchema,
		Publishes: []eventCatalogPublish{{
			Type: "documents.entry.ingested", Namespace: "documents", Schema: "documents/events/v1/entry.proto#EntryIngested", Major: 1, Visibility: "tenant",
		}},
	}))
	if !strings.Contains(docs, "- **Consumers:** _none_") {
		t.Fatalf("a type with no consumers must render _none_:\n%s", docs)
	}
}
