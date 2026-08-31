import { timestampDate } from "@bufbuild/protobuf/wkt";
import {
	type Datasource,
	datasource,
	DatasourceProvider,
	DatasourceStatus,
} from "@codefly/saas-sdk";
import {
	Code,
	ConnectError,
	type Interceptor,
	type Transport,
} from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import type { DatasourceClient, DatasourceView } from "./types.js";

/**
 * A solution remote's whole backend seam: the same-origin gateway base and the
 * host-owned token getters (`SolutionPageProps`). Everything the datasource
 * components need to reach the live service, with no ambient transport or auth
 * context to inherit.
 */
export interface GatewayBinding {
	/** Same-origin base the gateway proxies to the backend, e.g. `/api/solutions/{id}/proxy`. */
	apiBase: string;
	/** Reads the current access token (may be null before the first exchange). */
	getAccessToken: () => string | null;
	/**
	 * Exchanges the session for a fresh access token when a request comes back
	 * Unauthenticated — the short-lived token expired, was revoked, or none was
	 * installed yet. The interceptor retries the call once with the returned
	 * token; single-flight coordination is the host's concern. Without it a
	 * request that crosses the token's lifetime fails instead of recovering.
	 */
	refreshAccessToken?: () => Promise<string | null>;
}

/**
 * Wraps a Connect `Transport` in the transport-free `DatasourceClient` the
 * components drive: the `@codefly/saas-sdk` client plus the protobuf→view
 * mapping at the boundary. Callers that already own an authenticated transport
 * (the portal) pass it straight in; `createDatasourceClient` builds one from a
 * gateway binding.
 */
export function datasourceClientOverTransport(
	transport: Transport,
): DatasourceClient {
	const client = datasource.New(transport);
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

/**
 * Builds a `DatasourceClient` from a gateway binding: a scoped transport that
 * stamps the host's bearer token on every request and, on an Unauthenticated
 * response, exchanges for a fresh token and retries the call once — the same
 * mid-session recovery the portal's transport does, so a solution's data calls
 * survive the access token expiring while the page stays open.
 */
export function createDatasourceClient(
	binding: GatewayBinding,
): DatasourceClient {
	const auth: Interceptor = (next) => async (req) => {
		const token = binding.getAccessToken();
		if (token) {
			req.header.set("Authorization", `Bearer ${token}`);
		}
		try {
			return await next(req);
		} catch (error) {
			if (
				!binding.refreshAccessToken ||
				ConnectError.from(error).code !== Code.Unauthenticated
			) {
				throw error;
			}
			const fresh = await binding.refreshAccessToken();
			if (!fresh) {
				throw error;
			}
			req.header.set("Authorization", `Bearer ${fresh}`);
			return next(req);
		}
	};
	return datasourceClientOverTransport(
		createConnectTransport({ baseUrl: binding.apiBase, interceptors: [auth] }),
	);
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
