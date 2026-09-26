import { timestampDate } from "@bufbuild/protobuf/wkt";
import {
	AccessBasis,
	accounts,
	type Datasource,
	DatasourceProvider,
	DatasourceStatus,
} from "@codefly-dev/saas-sdk";
import {
	Code,
	ConnectError,
	type Interceptor,
	type Transport,
} from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import type {
	AccessibleScopeView,
	DatasourceClient,
	DatasourceStatusName,
	DatasourceView,
} from "./types.js";

/**
 * A solution remote's whole backend seam: the same-origin gateway base and the
 * host-owned token getters (`SolutionPageProps`). Everything the datasource
 * components need to reach the live service, with no ambient transport or auth
 * context to inherit.
 */
export interface GatewayBinding {
	/** Same-origin base the gateway proxies to the backend, e.g. `/api/solutions/{id}/proxy`. */
	apiBase: string;
	/**
	 * Permission resource type the collection's content is governed by, as the
	 * composition declares it. This kit ships with the host and holds no domain
	 * content, so it cannot know whether a collection holds documents, rows or
	 * models — the consumer mounting it says. Omitted means undeclared, and
	 * `listAccessibleScopes` then rejects: with no resource to ask about there is
	 * no verdict to report, and the panel reports the lookup as unresolved rather
	 * than claiming the viewer was refused.
	 */
	contentResource?: string;
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
 * components drive: the `@codefly-dev/saas-sdk` client plus the protobuf→view
 * mapping at the boundary. Callers that already own an authenticated transport
 * (the portal) pass it straight in; `createDatasourceClient` builds one from a
 * gateway binding.
 */
export function datasourceClientOverTransport(
	transport: Transport,
	contentResource?: string,
): DatasourceClient {
	const client = accounts.New(transport).datasource();
	return {
		async listAccessibleScopes(orgId) {
			// Nothing declared the content's resource type, so there is no question
			// to ask the permission service. Rejecting is how this contract already
			// reports an answer it could not obtain; resolving with an empty set
			// would instead state that the viewer holds no read access, a verdict
			// about their authority that an undeclared composition gave nobody the
			// standing to make.
			if (!contentResource) {
				throw new Error(
					"no collection content resource is declared for this deployment, so read access cannot be resolved",
				);
			}
			const scopes: AccessibleScopeView[] = [];
			let pageToken = "";
			do {
				const page = await accounts
					.New(transport)
					.accessibleScope()
					.listMyAccessibleScopes({
						orgId,
						resourceType: contentResource,
						action: "read",
						pageSize: 1000,
						pageToken,
					});
				scopes.push(
					...page.scopes.map((scope) => ({
						nodeId: scope.nodeId,
						label: scope.label,
						kind: scope.kind,
						actions: ["read"],
						viaPlatformAdministrator:
							scope.basis === AccessBasis.PLATFORM_ADMINISTRATOR,
					})),
				);
				pageToken = page.nextPageToken;
			} while (pageToken);
			return scopes;
		},
		async listActivity(orgId, sourceId) {
			const audit = accounts.New(transport).audit();
			const types = [
				"saas.datasource.source.added",
				"saas.datasource.credential.updated",
				"saas.datasource.source.synced",
				"saas.datasource.source.removed",
				"saas.datasource.change_set_compiled",
				"saas.datasource.sync.completed",
				"saas.datasource.sync.failed",
			];
			const pages = await Promise.all(
				types.map((eventType) =>
					audit.queryAuditLog({
						orgId,
						resourceId: sourceId,
						eventType,
						pageSize: 10,
					}),
				),
			);
			const events = [
				...new Map(
					pages
						.flatMap((page) => page.events)
						.map((event) => [event.id, event]),
				).values(),
			];
			events.sort(
				(a, b) =>
					Number(b.createdAt?.seconds ?? 0) - Number(a.createdAt?.seconds ?? 0),
			);
			return events.slice(0, 50).map((event) => ({
				id: event.id,
				type: event.eventType,
				actor: event.actorId,
				at: event.createdAt
					? timestampDate(event.createdAt).toISOString()
					: undefined,
				fields: event.payload ?? {},
			}));
		},
		async listSources(orgId) {
			const response = await client.listSources({ orgId });
			return response.datasources.map(toDatasourceView);
		},
		async addGitHubSource(input) {
			await client.addGitHubSource({
				orgId: input.orgId,
				repo: input.repo,
				paths: input.paths,
				fileExtensions: input.fileExtensions ?? [],
				branch: input.branch,
				accessToken: input.accessToken ?? "",
				webhookSecret: input.webhookSecret,
				boundary: input.boundaryNodeId
					? { case: "boundaryNodeId", value: input.boundaryNodeId }
					: { case: "collectionLabel", value: input.targetCollection },
			});
		},
		async beginGitHubAppSetup(orgId) {
			const response = await client.beginGitHubAppSetup({ orgId });
			return {
				installUrl: response.installUrl,
				state: response.state,
				expiresAt: response.expiresAt
					? timestampDate(response.expiresAt).toISOString()
					: undefined,
			};
		},
		async completeGitHubAppSetup(orgId, state, installationId, code) {
			const response = await client.completeGitHubAppSetup({
				orgId,
				state,
				installationId,
				code,
			});
			return {
				installationId: response.installationId,
				repositories: response.repositories.map((repository) => ({
					repo: repository.repo,
					defaultBranch: repository.defaultBranch,
					alreadyConnected: repository.alreadyConnected,
				})),
			};
		},
		async migrateGitHubSourceToApp(orgId, id) {
			await client.migrateGitHubSourceToApp({ orgId, id });
		},
		async syncSource(orgId, id, accessToken) {
			const response = await client.syncSource({
				orgId,
				id,
				...(accessToken ? { accessToken } : {}),
			});
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
		binding.contentResource,
	);
}

const statusNames: Partial<Record<DatasourceStatus, DatasourceStatusName>> = {
	[DatasourceStatus.ACTIVE]: "active",
	[DatasourceStatus.PAUSED]: "paused",
	[DatasourceStatus.DEGRADED]: "degraded",
};

function toDatasourceView(source: Datasource): DatasourceView {
	return {
		id: source.id,
		orgId: source.orgId,
		provider:
			source.provider === DatasourceProvider.GITHUB ? "github" : "unknown",
		repo: source.github?.repo ?? "",
		paths: source.github ? [...source.github.paths] : [],
		fileExtensions: source.github ? [...source.github.fileExtensions] : [],
		branch: source.github?.branch ?? "",
		boundaryNodeId: source.boundaryNodeId,
		boundaryLabel: source.boundaryLabel || undefined,
		webhookConfigured: source.webhookConfigured,
		status: statusNames[source.status] ?? "unknown",
		statusReason: source.statusReason || undefined,
		lastSyncedAt: source.lastSyncedAt
			? timestampDate(source.lastSyncedAt).toISOString()
			: undefined,
		lastIngestedAt: source.lastIngestedAt
			? timestampDate(source.lastIngestedAt).toISOString()
			: undefined,
		lastIngestedCommit: source.lastIngestedCommit || undefined,
		createdAt: source.createdAt
			? timestampDate(source.createdAt).toISOString()
			: undefined,
	};
}
