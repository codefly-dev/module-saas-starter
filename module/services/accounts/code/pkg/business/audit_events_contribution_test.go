package business

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

var updateAuditEventsContribution = flag.Bool("update", false, "rewrite the committed audit events contribution")

func auditEventsContributionFile() string {
	return filepath.Join("..", "..", "..", "..", "..", AuditEventsContributionPath)
}

// The committed contribution is an input to module-compose, so drift between it
// and the registry would silently drop a type out of the catalog — and an
// unregistered type is not `external`, which is what makes a webhook endpoint
// subscribed to it stop being delivered.
func TestAuditEventsContributionMatchesRegistry(t *testing.T) {
	rendered := RenderAuditEventsContribution()
	path := auditEventsContributionFile()
	if *updateAuditEventsContribution {
		if err := os.WriteFile(path, rendered, 0o644); err != nil {
			t.Fatalf("write contribution: %v", err)
		}
		return
	}
	committed, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read contribution: %v", err)
	}
	if string(committed) != string(rendered) {
		t.Fatalf("%s is stale; re-run go test ./pkg/business -run AuditEventsContribution -update", AuditEventsContributionPath)
	}
}

// domain_events.type and event_subscriptions.type_pattern both constrain an
// event type to dotted lowercase segments. An audit type that cannot satisfy
// that grammar could be registered in the catalog and then fail to publish at
// runtime, so the two vocabularies are checked against each other here.
func TestAuditEventTypesArePublishableEventTypes(t *testing.T) {
	publishable := regexp.MustCompile(`^[a-z][a-z0-9_]*(?:\.[a-z0-9_]+)+$`)
	for _, def := range AuditEventCatalog() {
		if !publishable.MatchString(string(def.Type)) {
			t.Errorf("audit event type %q is not a publishable domain event type", def.Type)
		}
	}
}
