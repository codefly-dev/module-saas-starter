package main

import (
	"encoding/json"
	"log"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/solutionhost"
)

// A solution's manifest is what the host's frontend renders its page from.
// Its author-declared half is the frontend block of the applied presence —
// accounts copies that block onto the record's frontend half in the
// transaction that applies the generation — and the rest is this host's own:
// where it serves the solution's assets, the alias it proxies the backend
// under, and where the backend's capability document is. Nothing in it is
// reported by a running process.
const (
	// solutionManifestSchemaMajor is the major of the manifest wire shape this
	// host projects. The frontend serves exactly this one
	// (SOLUTION_MANIFEST_SCHEMA_MAJOR, services/frontend/code/src/solutions/host-runtime.ts).
	solutionManifestSchemaMajor = 1
	// solutionAssetsManifestPath is where a solution's Module Federation
	// manifest is on its own origin. The frontend loads it through
	// /solutions/<alias>/assets/*, which the proxy serves without a bearer.
	solutionAssetsManifestPath = "/assets/mf-manifest.json"
	// solutionCapabilityPath is where a solution's backend publishes its
	// capability document.
	solutionCapabilityPath = "/.well-known/capabilities"
	// solutionPagePathPrefix is where the frontend mounts a solution's page.
	solutionPagePathPrefix = "/s/"
)

// solutionManifest builds a record's manifest from the frontend block its
// applied presence declared, or reports that the record has none to render. A
// block this host cannot read is "none" too: the projection then omits the
// manifest rather than publishing one the frontend would refuse for a reason
// it cannot name, and the cause is logged here, where it is readable.
func solutionManifest(record *accountsv1.SolutionRegistration, prepared bool) (string, bool) {
	block := record.GetFrontend().GetManifest()
	if block == "" {
		return "", false
	}
	var frontend solutionhost.Frontend
	if err := json.Unmarshal([]byte(block), &frontend); err != nil {
		log.Printf("solution registry: record %q carries a frontend block this host cannot read: %v", record.GetSolutionId(), err)
		return "", false
	}
	if frontend.Payload != nil && !prepared {
		return "", false
	}
	alias := record.GetSolutionId()
	manifest := map[string]any{
		"id":            alias,
		"schemaVersion": solutionManifestSchemaMajor,
		"nav":           map[string]any{"title": frontend.Title, "path": solutionPagePathPrefix + alias, "order": frontend.Order},
		"frontend": map[string]any{
			"type":          "module-federation",
			"manifestUrl":   solutionAssetsManifestPath,
			"exposedModule": frontend.ExposedModule,
			"hostContract":  frontend.HostContract,
		},
		"backend": map[string]any{"serviceAlias": alias, "capabilityPath": solutionCapabilityPath},
	}
	if group := record.GetGroup(); group != nil && record.GetDeclared().GetKind() == accountsv1.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_SOLUTION {
		manifest["group"] = map[string]any{"id": group.GetId(), "title": group.GetTitle(), "order": group.GetOrder()}
	}
	// Only when declared: a null dashboard would reach the frontend's data-graph
	// validator as a present slot.
	if frontend.Dashboard != nil {
		manifest["dashboard"] = frontend.Dashboard
	}
	if len(frontend.Surfaces) > 0 {
		surfaces := make([]map[string]any, 0, len(frontend.Surfaces))
		for _, surface := range frontend.Surfaces {
			entry := map[string]any{"id": surface.ID, "client": surface.Client, "title": surface.Title,
				"module": surface.Module, "contract": surface.Contract}
			if surface.Description != "" {
				entry["description"] = surface.Description
			}
			// Only when declared: the frontend reads an absent slot as "the
			// client decides" and a null one as malformed.
			if surface.Applies != nil {
				entry["applies"] = surface.Applies
			}
			if len(surface.Events) > 0 {
				entry["events"] = surface.Events
			}
			surfaces = append(surfaces, entry)
		}
		manifest["surfaces"] = surfaces
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		log.Printf("solution registry: record %q: cannot encode its manifest: %v", record.GetSolutionId(), err)
		return "", false
	}
	return string(encoded), true
}

// Old applied records may predate the preparation gate in Accounts. Their
// signed payload must never fall through to the mutable backend asset proxy.
func frontendRequiresPreparation(record *accountsv1.SolutionRegistration) bool {
	block := record.GetFrontend().GetManifest()
	if block == "" {
		return false
	}
	var frontend solutionhost.Frontend
	return json.Unmarshal([]byte(block), &frontend) != nil || frontend.Payload != nil
}
