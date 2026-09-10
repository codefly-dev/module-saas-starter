import {
	type AccessibleScopeView,
	type DatasourceClient,
	datasourceClientOverTransport,
} from "@codefly-dev/saas-ui";
import { createClient } from "@connectrpc/connect";
import { PermissionService } from "@/gen/saas/accounts/v1/authorization_pb";
import { apiTransport } from "@/lib/connect/transport";

const permissions = createClient(PermissionService, apiTransport);

// Entries pulled from a datasource are domain knowledge resources, so a grant on
// their boundary is a `knowledge` permission. ListMyAccessibleScopes answers for
// one action at a time; asking for each is what turns a list-objects RPC into
// the per-boundary grants summary the panel renders.
const BOUNDARY_RESOURCE = "knowledge";
const BOUNDARY_ACTIONS = ["read", "write"];

async function listAccessibleScopes(
	orgId: string,
): Promise<AccessibleScopeView[]> {
	const byNode = new Map<string, AccessibleScopeView>();
	for (const action of BOUNDARY_ACTIONS) {
		// A broad grant at the org root reaches every node beneath it, so the RPC
		// is paginated and a single page is not the whole answer.
		let pageToken = "";
		do {
			const page = await permissions.listMyAccessibleScopes({
				orgId,
				resourceType: BOUNDARY_RESOURCE,
				action,
				pageToken,
			});
			for (const scope of page.scopes) {
				const seen = byNode.get(scope.nodeId);
				if (seen) {
					seen.actions.push(action);
				} else {
					byNode.set(scope.nodeId, {
						nodeId: scope.nodeId,
						label: scope.label,
						kind: scope.kind,
						actions: [action],
					});
				}
			}
			pageToken = page.nextPageToken;
		} while (pageToken);
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
