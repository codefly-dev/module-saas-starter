/**
 * The datasource contract the components drive. A consumer adapts its generated
 * `DatasourceService` Connect client — or, once it ships, `@codefly-dev/saas-sdk` — to
 * this shape, so the package carries no transport and no generated protobuf.
 */

export type DatasourceProviderName = "github" | "unknown";

export type DatasourceStatusName = "active" | "paused" | "unknown";

/** A connected datasource, already mapped out of protobuf at the boundary. */
export interface DatasourceView {
	id: string;
	orgId: string;
	provider: DatasourceProviderName;
	repo: string;
	paths: string[];
	branch: string;
	/** The scope node the source's Entries land in (issue #473). */
	boundaryNodeId: string;
	webhookConfigured: boolean;
	status: DatasourceStatusName;
	lastSyncedAt: string | undefined;
	/**
	 * When the change-set compiler last durably enqueued a change set — advanced
	 * by a webhook delivery, the periodic reconcile, or a tenant's "Sync now"
	 * (which for a github source dispatches a forced reconcile rather than a
	 * pull). A separate clock from `lastSyncedAt`, which only the pulled
	 * providers advance; at most one of the two ever ticks for a given source.
	 *
	 * Optional so that a consumer adapting its own client to `DatasourceClient`
	 * — the pattern this file's header documents — keeps compiling without them.
	 */
	lastIngestedAt?: string | undefined;
	/** Head commit the `lastIngestedAt` change set covered. */
	lastIngestedCommit?: string | undefined;
	createdAt: string | undefined;
}

/** The connect form's resolved output, ready for `addGitHubSource`. */
export interface ConnectGitHubInput {
	orgId: string;
	repo: string;
	paths: string[];
	branch: string;
	targetCollection: string;
	accessToken: string;
	webhookSecret: string;
}

export interface DatasourceClient {
	listSources(orgId: string): Promise<DatasourceView[]>;
	addGitHubSource(input: ConnectGitHubInput): Promise<void>;
	/** Enqueues an async pull; resolves to the durable job id. */
	syncSource(orgId: string, id: string): Promise<string>;
	deleteSource(orgId: string, id: string): Promise<void>;
}
