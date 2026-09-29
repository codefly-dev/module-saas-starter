import { timestampDate } from "@bufbuild/protobuf/wkt";
import {
	AccessBasis,
	accounts,
	type Datasource,
	type DatasourceAccountLink,
	DatasourceDomainStatus,
	DatasourceLiveDelivery,
	DatasourceProvider,
	type DatasourceVerifiedDomain,
	DatasourceStatus,
	type SourceSyncProgress,
	SourceSyncFailureReason,
	SourceSyncPhase,
	SourceSyncTrigger,
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
	AccountLinkView,
	DatasourceClient,
	DatasourceLiveDeliveryName,
	DatasourceStatusName,
	DatasourceView,
	DomainView,
	SourceSyncFailureReasonName,
	SourceSyncPhaseName,
	SourceSyncTriggerName,
	SourceSyncView,
} from "./types.js";
import { notifySourceSyncRequested } from "./sync-requests.js";

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
	 * Optional host-owned notification that the token changed (see the kit's
	 * `SolutionBinding.subscribeToken`). Without it the panel re-reads the
	 * credential on a timer to keep `canManage` honest across a rotation.
	 */
	subscribeToken?: (listener: () => void) => () => void;
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
		async beginAccountLink(orgId, connector, redirectUri) {
			const response = await client.beginDatasourceAccountLink({
				orgId,
				connector,
				redirectUri,
			});
			return { authorizeUrl: response.authorizeUrl, state: response.state };
		},
		async completeAccountLink(orgId, state, code) {
			const response = await client.completeDatasourceAccountLink({
				orgId,
				state,
				code,
			});
			if (!response.link) throw new Error("the host returned no account link");
			return toAccountLinkView(response.link);
		},
		async listMyAccountLinks(orgId) {
			const response = await client.listMyDatasourceAccountLinks({ orgId });
			return response.links.map(toAccountLinkView);
		},
		async deleteAccountLink(orgId, id) {
			await client.deleteDatasourceAccountLink({ orgId, id });
		},
		async getDirectory(orgId) {
			const response = await client.getDatasourceDirectory({ orgId });
			return {
				links: response.links.map(toAccountLinkView),
				bindings: response.bindings.map((b) => ({
					id: b.id,
					connector: b.connector,
					providerGroupId: b.providerGroupId,
					teamId: b.teamId,
				})),
				domains: response.domains.map(toDomainView),
				teams: response.teams.map((t) => ({ id: t.id, name: t.name })),
			};
		},
		async bindGroup(orgId, connector, providerGroupId, teamId) {
			const response = await client.bindDatasourceGroup({
				orgId,
				connector,
				providerGroupId,
				teamId,
			});
			const b = response.binding;
			if (!b) throw new Error("the host returned no group binding");
			return {
				id: b.id,
				connector: b.connector,
				providerGroupId: b.providerGroupId,
				teamId: b.teamId,
			};
		},
		async unbindGroup(orgId, id) {
			await client.unbindDatasourceGroup({ orgId, id });
		},
		async claimDomain(orgId, domain) {
			const response = await client.claimDatasourceDomain({ orgId, domain });
			if (!response.domain) throw new Error("the host returned no domain");
			return toDomainView(response.domain);
		},
		async verifyDomain(orgId, id) {
			const response = await client.verifyDatasourceDomain({ orgId, id });
			if (!response.domain) throw new Error("the host returned no domain");
			return toDomainView(response.domain);
		},
		async deleteDomain(orgId, id) {
			await client.deleteDatasourceDomain({ orgId, id });
		},
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
			const response = await client.addGitHubSource({
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
			// The host starts a GitHub source's first sync as it connects it.
			notifySourceSyncRequested(response.datasource?.id ?? "");
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
			notifySourceSyncRequested(id);
			return response.jobId;
		},
		async reconnectSource(orgId, id, accessToken) {
			const response = await client.syncSource({
				orgId,
				id,
				reconnect: true,
				...(accessToken ? { accessToken } : {}),
			});
			notifySourceSyncRequested(id);
			return response.jobId;
		},
		async getSourceSync(orgId, sourceId, jobId) {
			try {
				const response = await client.getSourceSync({
					orgId,
					sourceId,
					jobId: jobId ?? "",
				});
				return toSourceSyncView(response.jobId, response.progress);
			} catch (error) {
				// No sync yet is an answer, not a failure: a source connected before
				// the host enqueued first syncs at connect has none until one runs.
				if (ConnectError.from(error).code === Code.NotFound) return undefined;
				throw error;
			}
		},
		async deleteSource(orgId, id) {
			await client.deleteSource({ orgId, id });
		},
	};
}

const syncPhaseNames: Partial<Record<SourceSyncPhase, SourceSyncPhaseName>> = {
	[SourceSyncPhase.QUEUED]: "queued",
	[SourceSyncPhase.FETCHING]: "fetching",
	[SourceSyncPhase.COMPILED]: "compiled",
	[SourceSyncPhase.HANDED_OFF]: "handed_off",
	[SourceSyncPhase.DONE]: "done",
	[SourceSyncPhase.FAILED]: "failed",
};

const syncTriggerNames: Partial<Record<SourceSyncTrigger, SourceSyncTriggerName>> = {
	[SourceSyncTrigger.MANUAL]: "manual",
	[SourceSyncTrigger.SCHEDULED]: "scheduled",
	[SourceSyncTrigger.WEBHOOK]: "webhook",
};

const syncFailureNames: Partial<
	Record<SourceSyncFailureReason, SourceSyncFailureReasonName>
> = {
	[SourceSyncFailureReason.RATE_LIMITED]: "rate_limited",
	[SourceSyncFailureReason.CREDENTIAL]: "credential",
	[SourceSyncFailureReason.ACCESS_DENIED]: "access_denied",
	[SourceSyncFailureReason.NOT_FOUND]: "not_found",
	[SourceSyncFailureReason.TOO_LARGE]: "too_large",
	[SourceSyncFailureReason.HOST_UNAVAILABLE]: "host_unavailable",
	[SourceSyncFailureReason.DELIVERY_FAILED]: "delivery_failed",
	[SourceSyncFailureReason.OTHER]: "other",
};

type Stamp = Parameters<typeof timestampDate>[0] | undefined;

function iso(stamp: Stamp): string | undefined {
	return stamp ? timestampDate(stamp).toISOString() : undefined;
}

/** Maps the wire progress to the plain view; exported for its tests. */
export function toSourceSyncView(
	jobId: string,
	progress: SourceSyncProgress | undefined,
): SourceSyncView {
	const view: SourceSyncView = {
		jobId,
		phase: (progress && syncPhaseNames[progress.phase]) ?? "unknown",
		trigger: (progress && syncTriggerNames[progress.trigger]) ?? "unknown",
		queuedAt: iso(progress?.queuedAt),
		fetchingAt: iso(progress?.fetchingAt),
		compiledAt: iso(progress?.compiledAt),
		handedOffAt: iso(progress?.handedOffAt),
		finishedAt: iso(progress?.finishedAt),
		attempt: progress?.attempt ?? 0,
		maxAttempts: progress?.maxAttempts ?? 0,
	};
	const changes = progress?.changes;
	if (changes) {
		view.changes = {
			files: changes.files,
			added: changes.added,
			modified: changes.modified,
			deleted: changes.deleted,
			splitKnown: changes.splitKnown,
			snapshot: changes.snapshot,
			commit: changes.commit,
		};
	}
	const failure = progress?.failure;
	if (failure) {
		view.failure = {
			reason: syncFailureNames[failure.reason] ?? "other",
			code: failure.code,
			message: failure.message,
			retrying: failure.retrying,
			retryAt: iso(failure.retryAt),
		};
	}
	return view;
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

function toAccountLinkView(link: DatasourceAccountLink): AccountLinkView {
	return {
		id: link.id,
		userId: link.userId,
		connector: link.connector,
		providerAccountId: link.providerAccountId,
		providerAccountLogin: link.providerAccountLogin,
	};
}

function toDomainView(domain: DatasourceVerifiedDomain): DomainView {
	return {
		id: domain.id,
		domain: domain.domain,
		verified: domain.status === DatasourceDomainStatus.VERIFIED,
		txtRecordName: domain.txtRecordName,
		txtRecordValue: domain.txtRecordValue,
	};
}

const liveDeliveryNames: Partial<
	Record<DatasourceLiveDelivery, DatasourceLiveDeliveryName>
> = {
	[DatasourceLiveDelivery.NONE]: "none",
	[DatasourceLiveDelivery.SOURCE_WEBHOOK]: "source_webhook",
	[DatasourceLiveDelivery.APP_WEBHOOK]: "app_webhook",
};

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
		// UNSPECIFIED is what an older host sends (the field decodes to its proto
		// default), and it is left undefined rather than mapped to "none": "we
		// could not tell" and "nothing pushes" are different claims and only one
		// of them may be guessed.
		liveDelivery: liveDeliveryNames[source.liveDelivery],
		reconcileIntervalSeconds: source.reconcileInterval
			? Number(source.reconcileInterval.seconds)
			: undefined,
		status: statusNames[source.status] ?? "unknown",
		statusReason: source.statusReason || undefined,
		// Keyed on the gap, which is never empty for a non-conformant source: an
		// older host sends neither field, and a missing bool decodes as false,
		// which would flag every source it serves.
		conformant: source.conformanceGap ? false : undefined,
		conformanceGap: source.conformanceGap || undefined,
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
