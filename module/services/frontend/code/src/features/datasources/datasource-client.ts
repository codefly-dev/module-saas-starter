import {
	type AccessibleScopeView,
	type DatasourceClient,
	datasourceClientOverTransport,
} from "@codefly-dev/saas-ui";
import { createClient } from "@connectrpc/connect";
import { PermissionService } from "@/gen/saas/accounts/v1/authorization_pb";
import { apiTransport } from "@/lib/connect/transport";

const permissions = createClient(PermissionService, apiTransport);

// The vocabulary a boundary grant is expressed in belongs to the module that
// owns the ingested content, not to saas-starter — `ListMyAccessibleScopes`
// matches `role_permissions.resource`, so this must be the resource those grants
// were written with. `knowledge:read`/`knowledge:write` is this module's own
// published vocabulary and the closest fit for ingested documents, but no grant
// in this tree exercises a collection boundary, so it is a documented default
// rather than a verified fact: a deployment whose grants use another resource
// must change it here.
//
// A wrong value fails one way only — every lookup comes back empty, so the panel
// shows boundary ids and no grants line. It never renders that as denial (see
// BoundaryCell), so the cost of being wrong is a missing name, not a false claim
// about someone's authority.
const BOUNDARY_RESOURCE = "knowledge";
const BOUNDARY_ACTIONS = ["read", "write"];

// The server caps a page at 1000; asking for the cap halves the round trips on a
// tenant whose grants reach many nodes.
const BOUNDARY_PAGE_SIZE = 1000;

async function scopesForAction(orgId: string, action: string) {
	// A broad grant at the org root reaches every node beneath it, so the RPC is
	// paginated and a single page is not the whole answer.
	const collected = [];
	let pageToken = "";
	do {
		const page = await permissions.listMyAccessibleScopes({
			orgId,
			resourceType: BOUNDARY_RESOURCE,
			action,
			pageSize: BOUNDARY_PAGE_SIZE,
			pageToken,
		});
		collected.push(...page.scopes);
		pageToken = page.nextPageToken;
	} while (pageToken);
	return collected;
}

async function listAccessibleScopes(
	orgId: string,
): Promise<AccessibleScopeView[]> {
	// One walk per action, run together rather than end to end — the actions are
	// independent queries and this is already the most expensive call the panel
	// makes.
	const walks = await Promise.all(
		BOUNDARY_ACTIONS.map(async (action) => ({
			action,
			scopes: await scopesForAction(orgId, action),
		})),
	);
	// Merged after both settle, in a fixed action order, so the summary does not
	// depend on which walk finished first.
	const byNode = new Map<string, AccessibleScopeView>();
	for (const walk of walks) {
		for (const scope of walk.scopes) {
			const seen = byNode.get(scope.nodeId);
			if (seen) {
				seen.actions.push(walk.action);
			} else {
				byNode.set(scope.nodeId, {
					nodeId: scope.nodeId,
					label: scope.label,
					kind: scope.kind,
					actions: [walk.action],
				});
			}
		}
	}
	return [...byNode.values()];
}

// The portal drives the shared datasource components over its own transport —
// the one that injects the bearer token and does single-flight refresh-and-retry
// on 401. `@codefly-dev/saas-ui` owns the DatasourceService client and the
// protobuf→view mapping, so the portal and solution remotes share one adapter;
// the boundary lookup is supplied here because the accessible-scopes RPC is not
// part of the published SDK surface the gateway-bound client is limited to.
export const datasourceClient: DatasourceClient = {
	...datasourceClientOverTransport(apiTransport),
	listAccessibleScopes,
};
