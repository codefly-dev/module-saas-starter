/**
 * The datasource contract the components drive. A consumer adapts its generated
 * `DatasourceService` Connect client — or, once it ships, `@codefly/saas-sdk` — to
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
	targetCollection: string;
	webhookConfigured: boolean;
	status: DatasourceStatusName;
	lastSyncedAt: string | undefined;
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
