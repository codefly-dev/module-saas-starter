import { timestampDate } from "@bufbuild/protobuf/wkt";
import {
	type Datasource,
	datasource,
	DatasourceProvider,
	DatasourceStatus,
} from "@codefly/saas-sdk";
import type { Interceptor } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import type { DatasourceClient, DatasourceView } from "./types.js";

/**
 * A solution remote's whole backend seam: the same-origin gateway base and a
 * host-owned access-token getter (`SolutionPageProps`). Everything the
 * datasource components need to reach the live service, with no ambient
 * transport or auth context to inherit.
 */
export interface GatewayBinding {
	/** Same-origin base the gateway proxies to the backend, e.g. `/api/solutions/{id}/proxy`. */
	apiBase: string;
	/** Reads the current access token; the host keeps it fresh. */
	getAccessToken: () => string | null;
}

/**
 * Builds the transport-free `DatasourceClient` the components drive from a
 * gateway binding: a `@codefly/saas-sdk` Connect client over a scoped transport
 * that stamps the host's bearer token on every request, plus the protobuf→view
 * mapping at the boundary.
 */
export function createDatasourceClient(
	binding: GatewayBinding,
): DatasourceClient {
	const auth: Interceptor = (next) => (req) => {
		const token = binding.getAccessToken();
		if (token) {
			req.header.set("Authorization", `Bearer ${token}`);
		}
		return next(req);
	};
	const client = datasource.New(
		createConnectTransport({ baseUrl: binding.apiBase, interceptors: [auth] }),
	);
	return {
		async listSources(orgId) {
			const response = await client.listSources({ orgId });
			return response.datasources.map(toDatasourceView);
		},
		async addGitHubSource(input) {
			await client.addGitHubSource(input);
		},
		async syncSource(orgId, id) {
			const response = await client.syncSource({ orgId, id });
			return response.jobId;
		},
		async deleteSource(orgId, id) {
			await client.deleteSource({ orgId, id });
		},
	};
}

function toDatasourceView(source: Datasource): DatasourceView {
	return {
		id: source.id,
		orgId: source.orgId,
		provider:
			source.provider === DatasourceProvider.GITHUB ? "github" : "unknown",
		repo: source.github?.repo ?? "",
		paths: source.github ? [...source.github.paths] : [],
		branch: source.github?.branch ?? "",
		targetCollection: source.targetCollection,
		webhookConfigured: source.webhookConfigured,
		status:
			source.status === DatasourceStatus.ACTIVE
				? "active"
				: source.status === DatasourceStatus.PAUSED
					? "paused"
					: "unknown",
		lastSyncedAt: source.lastSyncedAt
			? timestampDate(source.lastSyncedAt).toISOString()
			: undefined,
		createdAt: source.createdAt
			? timestampDate(source.createdAt).toISOString()
			: undefined,
	};
}
