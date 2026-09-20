/**
 * The datasource contract the components drive. A consumer adapts its generated
 * `DatasourceService` Connect client — or, once it ships, `@codefly-dev/saas-sdk` — to
 * this shape, so the package carries no transport and no generated protobuf.
 */

export type DatasourceProviderName = "github" | "unknown";

export type DatasourceStatusName = "active" | "paused" | "degraded" | "unknown";

/** A connected datasource, already mapped out of protobuf at the boundary. */
export interface DatasourceView {
	id: string;
	orgId: string;
	provider: DatasourceProviderName;
	repo: string;
	paths: string[];
	fileExtensions?: string[];
	branch: string;
	/** The scope node the source's Entries land in (issue #473). */
	boundaryNodeId: string;
	webhookConfigured: boolean;
	status: DatasourceStatusName;
	/**
	 * Why the source left `active`, in prose a tenant can act on; absent while it
	 * is active. The host writes it from a closed set of named reasons — never
	 * the tenant, and never raw provider error text — so it is safe to render as
	 * it arrives.
	 *
	 * Optional so that a consumer adapting its own client to `DatasourceClient`
	 * keeps compiling without it.
	 */
	statusReason?: string | undefined;
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

/**
 * A data boundary (scope node) the caller may act on, as reported by the
 * accessible-scopes RPC. `scopePath` is deliberately absent: for a
 * machine-minted boundary it is the node's own UUID re-encoded as an ltree
 * label, so it never reads as a name — `label` is the only human-facing one.
 */
export interface AccessibleScopeView {
	nodeId: string;
	label: string;
	kind: string;
	/** Actions the caller holds on the node, e.g. `["read", "write"]`. */
	actions: string[];
}

/**
 * One repository a verified App installation grants this host read access to.
 *
 * `alreadyConnected` is answered per organization — the calling org already has
 * a GitHub source for this repo — not as a property of the installation, so two
 * organizations sharing an installation see it differently.
 */
export interface GitHubAppRepositoryView {
	repo: string;
	/** Empty when GitHub reported no default branch, so it is a suggestion. */
	defaultBranch: string;
	alreadyConnected: boolean;
}

/** Where to send the browser to install the App, and the state binding the return. */
export interface GitHubAppSetupHandle {
	installUrl: string;
	state: string;
	expiresAt: string | undefined;
}

/** The installation the host verified and claimed, and what it grants. */
export interface GitHubAppInstallationView {
	installationId: string;
	repositories: GitHubAppRepositoryView[];
}

/** The connect form's resolved output, ready for `addGitHubSource`. */
export interface ConnectGitHubInput {
	orgId: string;
	repo: string;
	paths: string[];
	fileExtensions?: string[];
	branch: string;
	targetCollection: string;
	boundaryNodeId?: string;
	/** Omitted connects through the App: the host resolves the installation. */
	accessToken?: string;
	webhookSecret: string;
}

export interface SourceActivity {
	id: string;
	type: string;
	actor: string;
	at?: string;
	fields: Record<string, unknown>;
}
export interface CollectionGrantView {
	id: string;
	subjectId: string;
	subjectKind: "principal" | "team";
	scopePath: string;
	roleId: string;
	subjectLabel: string;
	roleName: string;
	actorLabel: string;
}
export interface CollectionAccessView {
	nodeId: string;
	label: string;
	scopePath: string;
	grants: CollectionGrantView[];
}
export interface CollectionGrantSubject {
	id: string;
	kind: "principal" | "team";
	label: string;
}
export interface DatasourceClient {
	listCollections?(orgId: string): Promise<CollectionAccessView[]>;
	listGrantSubjects?(orgId: string): Promise<CollectionGrantSubject[]>;
	grantCollectionRead?(
		orgId: string,
		scopePath: string,
		subject: CollectionGrantSubject,
	): Promise<void>;
	revokeCollectionRead?(
		orgId: string,
		grant: CollectionGrantView,
	): Promise<void>;

	listActivity?(orgId: string, sourceId: string): Promise<SourceActivity[]>;
	listSources(orgId: string): Promise<DatasourceView[]>;
	addGitHubSource(input: ConnectGitHubInput): Promise<void>;
	/** Enqueues an async pull; resolves to the durable job id. */
	syncSource(orgId: string, id: string, accessToken?: string): Promise<string>;
	deleteSource(orgId: string, id: string): Promise<void>;
	/**
	 * Enumerates the caller’s readable collection boundaries; failure must
	 * reject, and so must a lookup that cannot be made at all — resolving empty
	 * would report the caller as refused rather than unanswered.
	 */
	listAccessibleScopes?(orgId: string): Promise<AccessibleScopeView[]>;

	/**
	 * Mints the one-time setup state and the URL that installs the deployment's
	 * App. Absent on a client that cannot drive App onboarding, which is what
	 * drops the App path and leaves the PAT one.
	 */
	beginGitHubAppSetup?(orgId: string): Promise<GitHubAppSetupHandle>;
	/**
	 * Redeems the state the redirect echoed back and claims the installation.
	 * `code` is the authorization code GitHub appends when the App requests user
	 * authorization during installation; the host trades it to prove the caller
	 * can reach the installation they name, so an empty one is refused there
	 * rather than filtered out here.
	 */
	completeGitHubAppSetup?(
		orgId: string,
		state: string,
		installationId: string,
		code: string,
	): Promise<GitHubAppInstallationView>;
	/** Rebinds an existing source's credential onto the App, in place. */
	migrateGitHubSourceToApp?(orgId: string, id: string): Promise<void>;
}
