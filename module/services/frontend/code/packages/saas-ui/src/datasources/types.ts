/**
 * The datasource contract the components drive. A consumer adapts its generated
 * `DatasourceService` Connect client — or, once it ships, `@codefly-dev/saas-sdk` — to
 * this shape, so the package carries no transport and no generated protobuf.
 */

import type { JsonObject } from "@bufbuild/protobuf";
import type { SourceReconcileStatus } from "./reconcile-passes.js";

export type DatasourceProviderName = "github" | "api" | "unknown";

export type DatasourceStatusName = "active" | "paused" | "degraded" | "unknown";

/**
 * How a change at a source reaches the deployment: through the source's own
 * repository webhook, through the deployment's single GitHub App webhook, or not
 * at all — in which case the source is only as current as its last reconcile.
 */
export type DatasourceLiveDeliveryName =
	| "none"
	| "source_webhook"
	| "app_webhook";

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
	/**
	 * That node's display label — the collection's name — which the host lists
	 * with the source for any member, whether or not they may read the
	 * collection. Optional so a consumer adapting its own client keeps compiling.
	 */
	boundaryLabel?: string | undefined;
	webhookConfigured: boolean;
	/**
	 * How a change at the source reaches this deployment without waiting for the
	 * periodic reconcile.
	 *
	 * Strictly more informative than `webhookConfigured`, which reports only
	 * whether a signing secret is stored against this source. A source connected
	 * through the GitHub App — the recommended path — holds no secret of its
	 * own, so that flag is false for it whether or not the operator registered
	 * the App webhook; rendering it alone showed every App-backed source as
	 * having no live delivery, and gave a reader no way to tell a deployment
	 * with the webhook wired from one without it.
	 *
	 * Optional, and absent from an older host: a consumer reading one must treat
	 * absence as "not known", never as "none".
	 */
	liveDelivery?: DatasourceLiveDeliveryName | undefined;
	/**
	 * How often the host reconciles this source from scratch when nothing
	 * pushes — the bound on how stale it may be — in seconds. 0 means the
	 * periodic reconcile is off for it. Absent from an older host.
	 */
	reconcileIntervalSeconds?: number | undefined;
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
	/**
	 * False when the source's provider does not yet meet the host's datasource
	 * connector envelope. Such a source keeps running, but the host takes no
	 * new source of that provider until it conforms, so the panel flags it.
	 *
	 * Optional so that a consumer adapting its own client keeps compiling
	 * without it; absent is treated as conformant, since only the host can
	 * say otherwise.
	 */
	conformant?: boolean | undefined;
	/** What keeps a non-conformant provider off the envelope, in the host's words. */
	conformanceGap?: string | undefined;
	lastSyncedAt: string | undefined;
	/**
	 * When the change-set compiler last durably enqueued a change set — advanced
	 * by a webhook delivery or by a reconcile pass — a declared schedule's
	 * occurrence or a person's "Sync now", which are the same admission and
	 * reach a github source as a conditional reconcile rather than a pull. A
	 * separate clock from `lastSyncedAt`, which only the pulled providers
	 * advance; at most one of the two ever ticks for a given source.
	 *
	 * Optional so that a consumer adapting its own client to `DatasourceClient`
	 * — the pattern this file's header documents — keeps compiling without them.
	 */
	lastIngestedAt?: string | undefined;
	/** Head commit the `lastIngestedAt` change set covered. */
	lastIngestedCommit?: string | undefined;
	/**
	 * The non-secret configuration of an `api` source; absent for every other
	 * provider. Optional so a consumer adapting its own client keeps compiling.
	 */
	api?: ApiSourceView | undefined;
	createdAt: string | undefined;
}

/** How an `api` source presents its credential, as the host reports it. */
export type ApiCredentialKindName =
	| "bearer"
	| "basic"
	| "header"
	| "query"
	| "oauth2"
	| "unknown";

/**
 * Which OAuth 2.0 grant an `oauth2` source holds. `authorization_code` is the
 * one a person signs in for: the host makes such a source personal to whoever
 * created it, and resolves its client, endpoints and scopes from the
 * deployment's provider registration rather than from the source.
 */
export type ApiOAuth2GrantName =
	| "refresh_token"
	| "client_credentials"
	| "authorization_code"
	| "unknown";

/** An `api` source's configuration, already mapped out of protobuf. */
export interface ApiSourceView {
	baseUrl: string;
	resourcePath: string;
	credentialKind: ApiCredentialKindName;
	/** Set only for an `oauth2` source. */
	oauth2Grant?: ApiOAuth2GrantName | undefined;
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
	/**
	 * True when no grant or share reaches the node and the caller reads it only
	 * as a platform administrator, whose read spans every collection. Absent or
	 * false for access a grant confers, and from a client that cannot tell.
	 */
	viaPlatformAdministrator?: boolean;
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
	/**
	 * Omitted connects without a token: through the App when an installation
	 * this organization claimed covers the repository, otherwise with no
	 * credential at all when GitHub reports the repository public.
	 */
	accessToken?: string;
	webhookSecret: string;
}

/**
 * The person's own `api` source, ready for `addPersonalApiSource`: an API base
 * URL the person signs in to through the deployment's provider registration.
 * There is no credential field — the credential is the sign-in, which the
 * account link stores on the source when it completes.
 */
export interface ConnectPersonalApiInput {
	orgId: string;
	/** Absolute http(s) base URL of the API, e.g. "https://api.example.com". */
	baseUrl: string;
	/** A path under `baseUrl` to pull; empty for a source that is only called. */
	resourcePath?: string;
	/** The collection the source writes into, minted or reused by label. */
	targetCollection: string;
	/** An existing scope node instead of `targetCollection`. */
	boundaryNodeId?: string;
}

/**
 * One operation a caller may invoke on an `api` source: a route under the
 * source's base URL and the bounded JSON contract it is called with, as
 * `DatasourceService/DeclareSourceOperations` admits it. Never a credential;
 * the host checks the schemas and the route when it is declared.
 */
export interface SourceOperationDeclaration {
	name: string;
	description?: string;
	method: string;
	/** Relative to the source's base path; `{name}` segments are template parameters. */
	path: string;
	/** Input fields sent as query parameters rather than in the body. */
	query?: string[];
	/** A closed JSON Schema object for the operation's input. */
	inputSchema: JsonObject;
	/** A JSON Schema object for the provider's output. */
	outputSchema: JsonObject;
	effect: "read_only" | "mutation";
	/** Upper bound on the provider's output; 0 or absent applies the host's. */
	maxOutputBytes?: number;
}

/**
 * What an edit of a connected source leaves it fetching, ready for
 * `updateSource`. Every field is the whole value, not a patch: empty `paths`
 * ingests the whole repository and empty `fileExtensions` admits every file
 * type, exactly as on connect. The repository, the credential and the
 * collection are absent because they are not editable — a source pointed at
 * another repository, or writing into another collection, is a different
 * source.
 */
export interface EditSourceInput {
	orgId: string;
	id: string;
	branch: string;
	paths: string[];
	fileExtensions?: string[];
	/**
	 * The scope the editor was shown, exactly as it read it, which the host
	 * accepts the replacement against. Required, because every field above is a
	 * whole-field replacement: a save applied unconditionally after someone else
	 * narrowed the source would restore the wider scope and re-ingest everything
	 * they had just excluded. A mismatch is refused, not applied.
	 */
	expected: SourceFetchScope;
}

/** What a source fetches — the three values an edit replaces, as a whole. */
export interface SourceFetchScope {
	branch: string;
	paths: string[];
	fileExtensions: string[];
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
/** The stage the host has reached in one sync of a source. */
export type SourceSyncPhaseName =
	| "queued"
	| "fetching"
	| "compiled"
	| "handed_off"
	| "done"
	| "failed"
	| "unknown";

/**
 * What started a sync: a tenant (or the connect), the periodic reconcile, a
 * webhook, or an edit of the source itself.
 *
 * `reconfigured` is the one that changes how a change set should be read: the
 * source's branch or filters moved, so the files it brings differ because the
 * source was re-aimed rather than because the repository changed.
 */
export type SourceSyncTriggerName =
	| "manual"
	| "scheduled"
	| "webhook"
	| "reconfigured"
	| "unknown";

/** Why a sync waits to retry, or failed — typed so a view offers the remedy. */
export type SourceSyncFailureReasonName =
	| "rate_limited"
	| "credential"
	| "access_denied"
	| "not_found"
	| "too_large"
	| "host_unavailable"
	| "delivery_failed"
	| "other";

/**
 * One sync of a source as the host reports it: its phase and when it reached
 * each, the change set it compiled, and why it waits or failed. The phases are
 * the host's own work — queued, fetching over git, compiled into a change set,
 * handed to the consuming module's queue, done — so what a module does with
 * each file afterwards is that module's to report, never this view's.
 *
 * Timestamps are ISO strings, absent until the phase is reached.
 */
export interface SourceSyncView {
	jobId: string;
	phase: SourceSyncPhaseName;
	trigger: SourceSyncTriggerName;
	queuedAt?: string;
	fetchingAt?: string;
	compiledAt?: string;
	handedOffAt?: string;
	finishedAt?: string;
	/**
	 * The compiled change set, relative to the commit the host last handed off.
	 * Absent until compiled; absent at `done` means the source had not changed.
	 */
	changes?: {
		files: number;
		added: number;
		modified: number;
		deleted: number;
		/** False when only `files` is known (e.g. a snapshot after a force push). */
		splitKnown: boolean;
		snapshot: boolean;
		commit: string;
	};
	failure?: {
		reason: SourceSyncFailureReasonName;
		code: string;
		/** Host-authored prose from a closed set; safe to render as it arrives. */
		message: string;
		retrying: boolean;
		/** When the next attempt may run; for a rate limit, when it resets. */
		retryAt?: string;
	};
	attempt: number;
	maxAttempts: number;
}

/**
 * The receipt one reconcile effect carries, as `ReconcileSource` and
 * `LookupReconcileSource` both return it.
 *
 * `status` is "committed" when the pass is recorded, and "unknown" when a
 * recovery found no receipt for the identity. "unknown" is INCONCLUSIVE, never
 * "it did not happen": a caller that read it as a refusal would start a second
 * pass over work that may well be running.
 */
export interface SourceReconcileReceipt {
	effectId: string;
	status: SourceReconcileStatus;
	/**
	 * The job the pass handed to the source's own sync engine, which is what
	 * traces it. Empty when the pass committed nothing to trace.
	 */
	jobId: string;
}

/** Catalog additions are optional so existing injected clients keep compiling. */
export interface DatasourceConnectField {
	key: string;
	displayName: string;
	help: string;
	required: boolean;
	format: "text" | "lines" | "json";
}

export interface DatasourceProviderView {
	provider: number;
	connector: string;
	displayName: string;
	description: string;
	acceptsNewSources: boolean;
	gap: string;
	fields: DatasourceConnectField[];
	credentialModes: string[];
	supportsWebhook: boolean;
	/** A newer host may describe a field this installed SDK cannot encode. */
	unsupportedReason?: string;
	/** Whether the existing organization-app setup operations serve this provider. */
	supportsOrganizationAppSetup?: boolean;
}

/** Transient operation input. Never put this value in query/mutation state. */
export type DatasourceCredential =
	| { kind: "none" | "org_app" }
	| {
			kind: "static_secret";
			secret: string;
			webhookSecret?: string;
			oauth2ClientSecret?: string;
	  };

export interface ConnectSourceInput {
	orgId: string;
	provider: DatasourceProviderView;
	/** Raw form values; the adapter encodes them using the SDK descriptor. */
	config: Record<string, string>;
	credential: DatasourceCredential;
	targetCollection: string;
	boundaryNodeId?: string;
}

export interface DatasourceClient {
	getCatalog?(): Promise<DatasourceProviderView[]>;
	addSource?(input: ConnectSourceInput): Promise<void>;
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
	/**
	 * Creates the caller's own `api` source with the OAuth 2.0
	 * authorization-code grant (`API_CREDENTIAL_KIND_OAUTH2` +
	 * `GRANT_AUTHORIZATION_CODE`), which the host makes personal to the caller,
	 * and resolves to it. It holds no credential until the caller signs in
	 * through `beginAccountLink` with its id.
	 *
	 * Optional, and without it no surface offers to connect an API.
	 */
	addPersonalApiSource?(
		input: ConnectPersonalApiInput,
	): Promise<DatasourceView>;
	/**
	 * Replaces the operation set of an `api` source, atomically
	 * (`DatasourceService/DeclareSourceOperations`). An empty list removes
	 * every declaration.
	 */
	declareSourceOperations?(
		orgId: string,
		sourceId: string,
		operations: readonly SourceOperationDeclaration[],
	): Promise<void>;
	/**
	 * Admits one reconcile pass over one source under `effectId` — the same host
	 * operation (`DatasourceService/ReconcileSource`) a declared reconcile
	 * schedule's occurrence admits, so a person's pass and a scheduled pass are
	 * one admission of one shape rather than two.
	 *
	 * The identity is the caller's, not the host's, and the kit mints it
	 * (a `ReconcilePassLedger`): two calls under one identity are one pass and
	 * one job, which is what makes a double press cost one re-send of the
	 * source rather than two. Resolves to the receipt the operation returns.
	 */
	reconcileSource(
		orgId: string,
		id: string,
		effectId: string,
	): Promise<SourceReconcileReceipt>;
	/**
	 * Reads back the receipt of one reconcile effect, starting nothing. Resolves
	 * `undefined` when the host knows no pass under that identity.
	 *
	 * Required rather than optional because it is the other half of the
	 * operation: an effect whose outcome cannot be recovered is not idempotent
	 * in any way a caller can use, so a client that offers the pass must offer
	 * the lookup.
	 */
	lookupReconcileSource(
		effectId: string,
	): Promise<SourceReconcileReceipt | undefined>;
	/**
	 * Reconnects a source as the viewer and enqueues a sync; resolves to the
	 * durable job id. From then on the source's syncs run on behalf of the
	 * viewer, as after a connect. With `accessToken` the saved PAT is replaced;
	 * without one only a source that holds no credential (a public repository)
	 * reconnects.
	 *
	 * Optional, and without it the surfaces offer no Reconnect control at all.
	 * It used to fall back to a plain sync carrying a replacement credential,
	 * which the host reads as a reconnect; that path went with `syncSource`, and
	 * a reconnect is not a reconcile — it rebinds whose authority the source's
	 * syncs run under, which no effect identity makes idempotent.
	 */
	reconnectSource?(
		orgId: string,
		id: string,
		accessToken?: string,
	): Promise<string>;
	/**
	 * Reads one sync of a source — the job a reconcile pass or a reconnect
	 * returned, or with no job id the source's latest, whatever started it.
	 * Resolves `undefined` when the source has no sync yet. Optional so a
	 * consumer adapting its own client keeps compiling without it.
	 */
	getSourceSync?(
		orgId: string,
		sourceId: string,
		jobId?: string,
	): Promise<SourceSyncView | undefined>;
	/**
	 * Changes what an already-connected source fetches — its branch and its path
	 * and file-type filters — keeping its credential, its sync history and the
	 * collection it writes into. Resolves to the id of the sync the host
	 * enqueued because the change moved what is fetched, or undefined when the
	 * request changed nothing.
	 *
	 * Optional, and without it the surfaces offer no Edit control at all: a
	 * consumer adapting its own client to an older host keeps compiling, and the
	 * panel never offers an edit its client cannot perform.
	 */
	updateSource?(input: EditSourceInput): Promise<string | undefined>;
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

	/**
	 * The datasource directory: the explicit mappings a connector that
	 * translates a provider's per-item access lists may name. A person links
	 * their own provider account by signing in to the provider; an
	 * administrator binds provider groups to teams and verifies domains.
	 * Optional so that a consumer's own adapter keeps compiling without them.
	 *
	 * `sourceId` names the source a generic OAuth link (`connector: "api"`)
	 * authorizes: the caller's own source, from `addPersonalApiSource`, which
	 * the host requires for that connector and ignores for the others.
	 */
	beginAccountLink?(
		orgId: string,
		connector: string,
		redirectUri: string,
		sourceId?: string,
	): Promise<AccountLinkHandle>;
	completeAccountLink?(
		orgId: string,
		state: string,
		code: string,
	): Promise<AccountLinkView>;
	listMyAccountLinks?(orgId: string): Promise<AccountLinkView[]>;
	deleteAccountLink?(orgId: string, id: string): Promise<void>;
	getDirectory?(orgId: string): Promise<DatasourceDirectoryView>;
	bindGroup?(
		orgId: string,
		connector: string,
		providerGroupId: string,
		teamId: string,
	): Promise<GroupBindingView>;
	unbindGroup?(orgId: string, id: string): Promise<void>;
	claimDomain?(orgId: string, domain: string): Promise<DomainView>;
	verifyDomain?(orgId: string, id: string): Promise<DomainView>;
	deleteDomain?(orgId: string, id: string): Promise<void>;
}

/** One person's provider account, proven by signing in to the provider. */
export interface AccountLinkView {
	id: string;
	userId: string;
	connector: string;
	providerAccountId: string;
	/** The account's current handle at the provider; not identity. */
	providerAccountLogin: string;
}

/** Where to send the browser to sign in, and the state it echoes back. */
export interface AccountLinkHandle {
	authorizeUrl: string;
	state: string;
}

/** A provider group an administrator bound to a team. */
export interface GroupBindingView {
	id: string;
	connector: string;
	providerGroupId: string;
	teamId: string;
}

/** A domain an administrator claimed, and the TXT record that proves it. */
export interface DomainView {
	id: string;
	domain: string;
	verified: boolean;
	txtRecordName: string;
	txtRecordValue: string;
}

/** An organization's whole directory, with the teams a group may bind to. */
export interface DatasourceDirectoryView {
	links: AccountLinkView[];
	bindings: GroupBindingView[];
	domains: DomainView[];
	teams: { id: string; name: string }[];
}
