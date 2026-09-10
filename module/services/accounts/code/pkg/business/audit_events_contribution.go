package business

import (
	"bytes"
	"fmt"
)

// AuditEventsContributionPath is the committed contribution this package
// renders, relative to the module root. module.package.codefly.yaml passes it to
// module-compose like any other events contribution.
const AuditEventsContributionPath = "contracts/accounts/saas.events.codefly.yaml"

// auditEventsNamespace is reserved in module.package.codefly.yaml so no
// downstream contribution can publish under it. The base module's own
// contribution reaches compose through --base-events, which is the invocation
// that waives the reservation for the module that owns it.
const auditEventsNamespace = "saas"

// Every audit event carries an untyped payload map rather than a generated
// message, so each published type resolves to the envelope itself — the same
// schema reference the hand-written contributions use. The breaking-change gate
// therefore watches the envelope's shape for these types, not a per-event
// payload schema.
const auditEventsSchemaRef = "saas/events/v1/events.proto#EventEnvelope"

// RenderAuditEventsContribution projects the audit registry into the events
// contribution that makes every audit event type a webhook-eligible domain
// event.
//
// Outbound webhooks are subscriptions with `delivery = webhook`, and only an
// `external`-visibility catalog type may be delivered to one. A customer can
// subscribe an endpoint to any registered audit event type, so every registered
// type is published here — a curated subset would silently stop delivering to
// endpoints already subscribed to whatever it left out.
//
// A platform-scope record (one emitted with no organization) is registered but
// never published: the emitter publishes only for a tenant, so those types exist
// in the catalog without ever reaching a subscriber.
func RenderAuditEventsContribution() []byte {
	var out bytes.Buffer
	out.WriteString(`# Generated from the audit registry by RenderAuditEventsContribution.
# Edit pkg/business/audit_registry.go and re-run
# `)
	out.WriteString("`go test ./pkg/business -run AuditEventsContribution -update`")
	out.WriteString(`.
#
# It is passed to module-compose as --base-events: the namespace below is
# reserved against downstream contributions, and only the module that owns the
# reservation may publish under it.
#
# Every audit event type is an external domain event so an outbound webhook can
# subscribe to it (EVENTS.md, "Webhooks are a subscriber kind").
#
# No partition is declared. A partition is a promise of FIFO within it, and the
# publish path buys that promise with a per-partition advisory lock held until
# the producing mutation commits — which on a per-tenant partition would
# serialize every audited mutation in an organization. Nothing consumes the
# ordering: an outbound webhook is dispatched in subscription-id order, and the
# platform namespace is not subscribable by a module.
`)
	fmt.Fprintf(&out, "schema: %s\n", "codefly/saas/events-contribution/v1")
	fmt.Fprintf(&out, "namespace: %s\n", auditEventsNamespace)
	out.WriteString("queues: []\n")
	out.WriteString("publishes:\n")
	for _, def := range AuditEventCatalog() {
		fmt.Fprintf(&out, "  - type: %s\n", def.Type)
		fmt.Fprintf(&out, "    schema: %s\n", auditEventsSchemaRef)
		out.WriteString("    visibility: external\n")
		out.WriteString("    retention: 30d\n")
	}
	return out.Bytes()
}
