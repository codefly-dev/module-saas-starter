"use client";

import {
	Badge,
	Banner,
	Button,
	Card,
	DelayedLoading,
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
	Input,
	Label,
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "@codefly-dev/ui/layout";

import { ConnectError } from "@connectrpc/connect";

import {
	QueryClient,
	QueryClientProvider,
	useQuery,
} from "@tanstack/react-query";
import { type ReactNode, useEffect, useMemo, useState } from "react";
import {
	COLLECTION_ACCESS_PATH,
	NoReadableCollection,
} from "../solution/no-readable-collection.js";
import {
	useAccessToken,
	viewerAdministersOrganization,
} from "../solution/viewer.js";
import { CollectionGrants } from "./collection-access.js";
import { ConnectGitHubForm } from "./connect-github-form.js";
import { createDatasourceClient, type GatewayBinding } from "./gateway.js";
import {
	useAccessibleScopes,
	useAddGitHubSource,
	useDeleteSource,
	useListSources,
	useSyncSource,
} from "./queries.js";
import type { ConnectGitHubValues } from "./schema.js";
import { SourceSyncProgress, useSourceSync } from "./sync-progress.js";
import type {
	AccessibleScopeView,
	DatasourceClient,
	DatasourceStatusName,
	DatasourceView,
	SourceSyncView,
} from "./types.js";
import {
	cn,
	formatGrants,
	formatIngest,
	formatSyncedAt,
	parsePaths,
	shortBoundaryId,
} from "./util.js";

interface DatasourcesPanelBaseProps {
	orgId: string;
	/** Called with the durable job id after a sync is enqueued. */
	onSyncEnqueued?: (jobId: string) => void;
	/**
	 * Whether the viewer may connect, sync, reconnect or remove a source and
	 * grant read access — the host's organization-administrator tier. Without
	 * it those controls are not offered; the host refuses the calls either way.
	 * Default: with a `gateway`, read from the viewer's credential
	 * (`viewerAdministersOrganization`); with an injected `client`, true (the
	 * consumer's own page decides who reaches the panel).
	 */
	canManage?: boolean;
	/**
	 * Renders beneath a source's repository name: the extension point through
	 * which a consumer shows what another owner knows about the source. The host
	 * lists, syncs and removes sources and says when it last dispatched files;
	 * what became of those files belongs to whichever module ingests them, and
	 * the consumer composing both hands that module's view in here.
	 */
	renderSourceDetail?: (source: DatasourceView) => ReactNode;
	/**
	 * Renders the durable execution of a source's sync, opened from its row: the
	 * fan-out task the sync became, its items, attempts, receipts and
	 * dead-letters.
	 *
	 * An extension point for the same reason `renderSourceDetail` is one. The
	 * host enqueues a sync and keeps the job; what executes it is whichever
	 * module runs durable work, and this repository may not name one — so the
	 * consumer composing both passes that module's own view in here, built from
	 * its own transport. Absent, no row offers the action: an empty panel
	 * promising an execution view would be worse than not offering one.
	 *
	 * `sync` is the host's projection of the sync the action was opened for, so a
	 * consumer can resolve it to whatever key its execution store is aged on
	 * without re-reading it.
	 *
	 * Three things the host cannot enforce for you, because it may not name the
	 * module that runs the work — and each of them fails by SUCCEEDING, which is
	 * why they are written down rather than left to be discovered:
	 *
	 * 1. **Pass this only for a viewer who may read the organization's
	 *    executions.** Durable work for a sync is admitted by the module that
	 *    ingests it, not by the person reading this panel, so a caller-scoped
	 *    "my own runs" read is not a weaker version of the organization read —
	 *    it is a correct answer to a different question, and its answer here is
	 *    an empty page. The panel's own empty state says "this source has never
	 *    synced", so a viewer without that permission would be told something
	 *    false by two correct components.
	 * 2. **Distinguish "no runs" from "you may not see the runs"** in whatever
	 *    you render, for the same reason. Render `SourceExecutionRestricted`
	 *    for the second, so the sentence reads the same wherever it appears.
	 *    Rendering nothing gets the host's neutral fallback, which names both
	 *    readings rather than letting silence pick one.
	 * 3. **Never report a count taken from a page of results.** One sync can
	 *    produce many runs, and the host already knows how many without paging:
	 *    `sync.changes.snapshot ? 1 : sync.changes.files`, which on the
	 *    incremental path *is* the hand-off count by construction. Show that
	 *    beside the runs you loaded rather than as a count of them — the two
	 *    disagreeing is then real information (work that was never admitted)
	 *    instead of a paging bug. Note it is only final once the sync is
	 *    terminal: read mid-sync it counts the hand-offs enqueued so far.
	 */
	renderSourceExecution?: (context: {
		source: DatasourceView;
		sync?: SourceSyncView;
	}) => ReactNode;
	className?: string;
}

/**
 * Either drive the panel with an injected `client` (the portal adapts its own
 * transport, keeping its ambient `QueryClientProvider`), or hand it a `gateway`
 * binding and it self-wires: it builds the `@codefly-dev/saas-sdk` client and a
 * scoped React-Query provider from that binding, so a solution remote drops it
 * in with only `{ apiBase, getAccessToken }` and no query/auth context.
 */
export type DatasourcesPanelProps = DatasourcesPanelBaseProps &
	(
		| { client: DatasourceClient; gateway?: never }
		| { gateway: GatewayBinding; client?: never }
	);

export function DatasourcesPanel(props: DatasourcesPanelProps) {
	if (props.gateway) {
		const { gateway, ...rest } = props;
		return <GatewayBoundPanel gateway={gateway} {...rest} />;
	}
	const { client, ...rest } = props;
	return <DatasourcesPanelView client={client} {...rest} />;
}

function GatewayBoundPanel({
	gateway,
	...rest
}: DatasourcesPanelBaseProps & { gateway: GatewayBinding }) {
	// The interceptor reads the token at request time, so the client only needs
	// rebuilding when the binding itself changes — not on every render.
	const {
		apiBase,
		getAccessToken,
		refreshAccessToken,
		contentResource,
		subscribeToken,
	} = gateway;
	const token = useAccessToken(getAccessToken, subscribeToken);
	const client = useMemo(
		() =>
			createDatasourceClient({
				apiBase,
				getAccessToken,
				refreshAccessToken,
				contentResource,
			}),
		[apiBase, getAccessToken, refreshAccessToken, contentResource],
	);
	const [queryClient] = useState(
		() => new QueryClient({ defaultOptions: { queries: { retry: 1 } } }),
	);
	return (
		<QueryClientProvider client={queryClient}>
			<DatasourcesPanelView
				client={client}
				{...rest}
				canManage={rest.canManage ?? viewerAdministersOrganization(token)}
			/>
		</QueryClientProvider>
	);
}

interface DatasourcesPanelViewProps extends DatasourcesPanelBaseProps {
	client: DatasourceClient;
}

function DatasourcesPanelView({
	client,
	orgId,
	onSyncEnqueued,
	canManage = true,
	renderSourceDetail,
	renderSourceExecution,
	className,
}: DatasourcesPanelViewProps) {
	const [activitySource, setActivitySource] = useState<DatasourceView | null>(
		null,
	);
	const [executionSource, setExecutionSource] = useState<DatasourceView | null>(
		null,
	);
	const [reconnecting, setReconnecting] = useState<DatasourceView | null>(null);
	const [reconnectPending, setReconnectPending] = useState(false);
	const [reconnectError, setReconnectError] = useState<string>();
	const beginAppSetup = client.beginGitHubAppSetup?.bind(client);
	const completeAppSetup = client.completeGitHubAppSetup?.bind(client);
	const migrateToApp = client.migrateGitHubSourceToApp?.bind(client);
	const reconnectSource = client.reconnectSource?.bind(client);

	// The return leg, captured once during the first render: the redirect's
	// parameters are a property of the URL the page loaded with, so the value has
	// to survive the scrub below rather than be re-read from an address that no
	// longer carries them.
	//
	// Captured whenever this client could redeem them, and deliberately NOT
	// conditioned on the viewer's authority. `canManage` is read from a credential
	// the panel only observes, so on the load that follows the redirect it can
	// still be false for an administrator; freezing that answer into a one-shot
	// initializer would drop a finished installation without a trace and leave the
	// single-use state and the authorization code in the address bar, in history
	// and in same-origin referrers. Who may redeem it is decided every render
	// instead, at `appSetupActive` below.
	//
	// A consumer adapting its own client may implement none of the App calls and
	// handle the redirect itself, and must keep both its parameters and its
	// address bar; and the state is redeemable only by the organization that began
	// it, so re-firing it after an org switch would report a rejection for a setup
	// that in fact succeeded.
	const [appSetupReturn] = useState(() => {
		if (!completeAppSetup) return null;
		const params = readAppSetupReturn();
		return params && { ...params, orgId };
	});
	const [showConnect, setShowConnect] = useState(!!appSetupReturn);
	const [beginPending, setBeginPending] = useState(false);
	const [beginError, setBeginError] = useState<string>();
	// Repositories connected while this panel has been mounted, whatever the
	// method used. The completed setup's answer predates them and cannot be
	// re-asked, so this is the only record that they are now taken.
	const [connectedRepos, setConnectedRepos] = useState<ReadonlySet<string>>(
		() => new Set(),
	);
	// Per-row pending sets, not the shared mutation's single `isPending`, so two
	// rows can sync/delete at once without one clearing the other's spinner and
	// re-enabling a button whose request is still in flight (double-enqueue).
	const [syncingIds, setSyncingIds] = useState<ReadonlySet<string>>(
		() => new Set(),
	);
	const [deletingIds, setDeletingIds] = useState<ReadonlySet<string>>(
		() => new Set(),
	);
	const [migratingIds, setMigratingIds] = useState<ReadonlySet<string>>(
		() => new Set(),
	);
	// Row action errors have no other surface (no toast dependency, no global
	// mutation handler), so they would vanish silently without this.
	const [syncNotice, setSyncNotice] = useState<string | null>(null);
	const [actionError, setActionError] = useState<string | null>(null);

	const list = useListSources(client, orgId);
	const scopes = useAccessibleScopes(client, orgId);
	const collections = useQuery({
		queryKey: ["collection-access", orgId],
		queryFn: () => client.listCollections!(orgId),
		enabled: !!client.listCollections,
		retry: false,
		refetchInterval: 5000,
	});
	const [editingCollection, setEditingCollection] = useState<string>();
	const listedCollections = collections.isError ? undefined : collections.data;
	const selectedCollection = collections.isError
		? undefined
		: collections.data?.find(
				(collection) => collection.nodeId === editingCollection,
			);
	const addMutation = useAddGitHubSource(client);
	const syncMutation = useSyncSource(client);
	const deleteMutation = useDeleteSource(client);

	const handleConnect = (values: ConnectGitHubValues) => {
		addMutation.mutate(
			{
				orgId,
				repo: values.repo,
				paths: parsePaths(values.paths),
				fileExtensions: parsePaths(values.fileExtensions).map((value) =>
					value.toLowerCase(),
				),
				branch: values.branch ?? "",
				targetCollection: values.targetCollection,
				boundaryNodeId: values.boundaryNodeId || undefined,
				// Neither the App nor a public repository sends a token: the host
				// resolves the installation, or confirms with GitHub that the
				// repository is public. A public source takes no webhook either, so
				// a secret typed on another path before switching is not sent.
				accessToken:
					values.method === "app" || values.method === "public"
						? undefined
						: values.accessToken,
				webhookSecret:
					values.method === "public" ? "" : (values.webhookSecret ?? ""),
			},
			{
				onSuccess: () => {
					setConnectedRepos((prev) => new Set(prev).add(values.repo));
					setShowConnect(false);
				},
			},
		);
	};

	// Redeeming is the host's to refuse, so the panel does not ask on behalf of a
	// viewer it can see would be refused. Derived per render rather than captured,
	// so an authority that only arrives after the first paint still redeems the
	// return leg the page loaded with.
	const appSetupClaimed = !!appSetupReturn && appSetupReturn.orgId === orgId;
	const appSetupActive = appSetupClaimed && canManage;
	// A return leg nobody on this screen can redeem. The capture and the scrub ran
	// regardless of authority, so the installation stands on the provider's side
	// while its single-use state is spent: saying nothing would leave an ordinary
	// panel over a connect that never happened.
	//
	// Held until the list has answered SUCCESSFULLY, because a credential that has
	// not been read yet also reads as "not an administrator". One authenticated
	// read is the panel's own proof that the binding is live, so this cannot
	// accuse an administrator mid-load; a list that failed proves nothing and
	// already shows its own error, which this line would only talk over.
	const appSetupUnredeemed = appSetupClaimed && !canManage && list.isSuccess;
	const appSetup = useQuery({
		queryKey: [
			"github-app-setup",
			appSetupReturn?.orgId,
			appSetupReturn?.state,
		],
		queryFn: () =>
			completeAppSetup!(
				appSetupReturn!.orgId,
				appSetupReturn!.state,
				appSetupReturn!.installationId,
				appSetupReturn!.code,
			),
		enabled: appSetupActive,
		// The state is redeemable exactly once, so a retry or a background refetch
		// would report a rejection for a setup that in fact succeeded.
		retry: false,
		staleTime: Number.POSITIVE_INFINITY,
		refetchOnMount: false,
		refetchOnWindowFocus: false,
	});
	// Burn the state out of the address bar for the same reason, and to keep it out
	// of browser history and same-origin referrers.
	useEffect(() => {
		if (appSetupReturn) scrubAppSetupReturn();
	}, [appSetupReturn]);

	// `alreadyConnected` is answered once, when the setup completes, and that
	// answer can never be refreshed: the state behind it is redeemable exactly
	// once, so refetching would report a rejection for a setup that succeeded.
	// A repository connected since therefore has to be folded in here, or the
	// picker keeps offering one this organization already holds.
	const appRepositories = useMemo(() => {
		const granted = appSetupActive ? appSetup.data?.repositories : undefined;
		return granted?.map((candidate) =>
			candidate.alreadyConnected || !connectedRepos.has(candidate.repo)
				? candidate
				: { ...candidate, alreadyConnected: true },
		);
	}, [appSetupActive, appSetup.data, connectedRepos]);
	const appSetupPhase = beginPending
		? "beginning"
		: appSetupActive && appSetup.isFetching
			? "completing"
			: undefined;
	const appSetupError = beginError
		? beginError
		: appSetupActive && appSetup.isError
			? messageOf(appSetup.error)
			: undefined;

	const handleBeginAppSetup = async () => {
		if (!beginAppSetup) return;
		setBeginError(undefined);
		setBeginPending(true);
		try {
			const handle = await beginAppSetup(orgId);
			// Navigating away, so the pending flag is deliberately left set: the
			// button must not re-enable under a browser that is already unloading.
			window.location.assign(handle.installUrl);
		} catch (error) {
			setBeginError(messageOf(error));
			setBeginPending(false);
		}
	};

	const handleMigrateToApp = async (source: DatasourceView) => {
		if (!migrateToApp) return;
		setSyncNotice(null);
		setActionError(null);
		setMigratingIds((prev) => new Set(prev).add(source.id));
		try {
			await migrateToApp(orgId, source.id);
			setSyncNotice(
				`${source.repo} now authenticates through the GitHub App. Its stored token is no longer used.`,
			);
			await list.refetch();
		} catch (error) {
			setActionError(
				`Couldn't move ${source.repo} onto the GitHub App: ${messageOf(error)}`,
			);
		} finally {
			setMigratingIds((prev) => without(prev, source.id));
		}
	};

	const handleSync = async (source: DatasourceView) => {
		setSyncNotice(null);
		setActionError(null);
		setSyncingIds((prev) => new Set(prev).add(source.id));
		// Per-call callbacks on a shared mutation only observe the latest call.
		// Await each request so every row reports its result and clears pending.
		try {
			const jobId = await syncMutation.mutateAsync({ orgId, id: source.id });
			setSyncNotice(
				`Sync queued for ${source.repo}. Ingestion runs in the background; content will appear in the collection when ready.`,
			);
			onSyncEnqueued?.(jobId);
		} catch (error) {
			setActionError(`Couldn't sync ${source.repo}: ${messageOf(error)}`);
		} finally {
			setSyncingIds((prev) => without(prev, source.id));
		}
	};

	const handleDelete = (source: DatasourceView) => {
		const ok = window.confirm(
			`Delete the data source for ${source.repo}?\n\n` +
				"Its stored credentials are removed. This cannot be undone.",
		);
		if (!ok) return;
		setActionError(null);
		setDeletingIds((prev) => new Set(prev).add(source.id));
		deleteMutation.mutate(
			{ orgId, id: source.id },
			{
				onError: (error) =>
					setActionError(`Couldn't delete ${source.repo}: ${messageOf(error)}`),
				onSettled: () => setDeletingIds((prev) => without(prev, source.id)),
			},
		);
	};

	const sources = list.data ?? [];
	const boundaries = useMemo(() => {
		const byNode = new Map<string, AccessibleScopeView>();
		for (const scope of (scopes.isError ? [] : scopes.data) ?? [])
			byNode.set(scope.nodeId, scope);
		return byNode;
	}, [scopes.data, scopes.isError]);
	// The scope lookup answers for every node kind, so a grant on a solution node
	// or on a placed record would silence a headline that speaks about
	// collections. With collections listed the question can be asked exactly; with
	// none listed — not yet loaded, or an organization that has none — there is no
	// collection to be refused, so the scope set is all there is to go on.
	const readableCollection = listedCollections?.length
		? listedCollections.some((collection) => boundaries.has(collection.nodeId))
		: boundaries.size > 0;

	return (
		<div className={cn("space-y-6", className)}>
			<section aria-label="Sources" className="space-y-3">
				<div className="flex items-start justify-between gap-4">
					<div>
						<h3 className="type-section-title">Sources</h3>
						<p className="type-body text-muted-foreground">
							Repositories this organization ingests from.
						</p>
					</div>
					{canManage && (
						<Button type="button" onClick={() => setShowConnect(true)}>
							Connect GitHub
						</Button>
					)}
				</div>

				{scopes.isError ? (
					<p role="alert" className="type-body text-destructive">
						Couldn’t verify collection permissions. This does not mean there is
						no indexed content.
					</p>
				) : scopes.isSuccess && !readableCollection ? (
					<div role="status">
						<NoReadableCollection
							canGrant={canManage}
							subject="ingested documents to show"
							// A client that can list collections makes this panel the grants
							// surface, so the notice names the section below rather than
							// linking to the page the reader is already on.
							grantsOnThisPage={!!client.listCollections}
						/>
					</div>
				) : null}
				{appSetupUnredeemed && (
					<Banner title="Repositories are waiting to be connected">
						The GitHub App installation finished, but only an organization
						administrator can connect the repositories it granted. Ask one to
						connect them in Admin → Data sources; the installation itself is
						already in place.
					</Banner>
				)}
				{selectedCollection && (
					<CollectionGrants
						client={client}
						orgId={orgId}
						collection={selectedCollection}
					/>
				)}
				{activitySource && (
					<SourceHistory
						client={client}
						orgId={orgId}
						source={activitySource}
						onClose={() => setActivitySource(null)}
					/>
				)}

				{/* The enqueue receipt, in a container. It used to be a bare paragraph
				    between the section heading and the table, which read as stray text
				    rather than as this panel's answer to the button that had just been
				    pressed. It is dismissible because the progress below supersedes it
				    within a poll: a notice that cannot be put away outlives the thing
				    it announced. */}
				{syncNotice && (
					<Banner
						title="Sync queued"
						onDismiss={() => setSyncNotice(null)}
						dismissLabel="Dismiss the sync notice"
					>
						{syncNotice}
					</Banner>
				)}
				{actionError && (
					<Card className="border-destructive/40 bg-destructive/10">
						<div
							role="alert"
							className="flex items-center justify-between gap-3 type-body text-destructive"
						>
							<span>{actionError}</span>
							<Button
								type="button"
								variant="outline"
								size="sm"
								onClick={() => setActionError(null)}
							>
								Dismiss
							</Button>
						</div>
					</Card>
				)}

				{/* One card per source whose sync is worth watching, above the table so
				    a reader who just pressed Sync does not have to find the row again. */}
				{client.getSourceSync &&
					sources.map((source) => (
						<SourceSyncWatch
							key={source.id}
							client={client}
							orgId={orgId}
							source={source}
							onOpenExecution={
								renderSourceExecution
									? () => setExecutionSource(source)
									: undefined
							}
						/>
					))}

				{executionSource && renderSourceExecution && (
					<SourceExecution
						client={client}
						orgId={orgId}
						source={executionSource}
						render={renderSourceExecution}
						onClose={() => setExecutionSource(null)}
					/>
				)}

				{list.isLoading ? (
					// Nothing for a fast list, and no blink for a slow one. A cached answer
					// arrives well inside the delay, so the common case renders the table
					// with no intervening state at all.
					<DelayedLoading active label="Loading data sources">
						<PanelMessage>Loading data sources…</PanelMessage>
					</DelayedLoading>
				) : list.isError ? (
					<PanelMessage tone="error">
						Couldn&apos;t load data sources. Retry shortly or check the service
						status.
					</PanelMessage>
				) : sources.length === 0 ? (
					<div className="flex flex-col items-center gap-3 rounded-lg border border-dashed px-6 py-12 text-center">
						<p className="type-emphasis">No data sources connected.</p>
						{canManage ? (
							<>
								<p className="type-body text-muted-foreground">
									Connect a GitHub repository to start ingesting.
								</p>
								<Button
									type="button"
									variant="outline"
									onClick={() => setShowConnect(true)}
								>
									Connect a repository
								</Button>
							</>
						) : (
							<p className="type-body text-muted-foreground">
								An organization administrator connects the repositories this
								organization ingests from.
							</p>
						)}
					</div>
				) : (
					<SourcesTable
						canManage={canManage}
						onActivity={client.listActivity ? setActivitySource : undefined}
						onExecution={renderSourceExecution ? setExecutionSource : undefined}
						onReconnect={(source) => {
							setReconnectError(undefined);
							setReconnecting(source);
						}}
						sources={sources}
						boundaries={boundaries}
						onMigrateToApp={migrateToApp ? handleMigrateToApp : undefined}
						migratingIds={migratingIds}
						permissionsResolved={scopes.isSuccess && !scopes.isError}
						syncingIds={syncingIds}
						deletingIds={deletingIds}
						onSync={handleSync}
						onDelete={handleDelete}
						renderSourceDetail={renderSourceDetail}
					/>
				)}
			</section>

			{client.listCollections ? (
				<section aria-label="Collection access" className="space-y-3">
					<div>
						<h3 className="type-section-title">Collection access</h3>
						<p className="type-body text-muted-foreground">
							Who can read what a source ingests. Connecting or syncing a source
							never grants access; a grant does.
						</p>
					</div>
					{collections.isError ? (
						<p role="alert" className="type-body text-destructive">
							Couldn’t inspect collection grants. Organization administrator
							access is required.
						</p>
					) : (
						<Table>
							<TableHeader>
								<TableRow>
									<TableHead>Collection</TableHead>
									<TableHead>Your access</TableHead>
									<TableHead>Readers</TableHead>
									<TableHead className="w-0" />
								</TableRow>
							</TableHeader>
							<TableBody>
								{(collections.data ?? []).map((collection) => {
									const readable =
										scopes.isSuccess && !scopes.isError
											? boundaries.has(collection.nodeId)
											: undefined;
									const asPlatformAdministrator =
										boundaries.get(collection.nodeId)
											?.viaPlatformAdministrator === true;
									const readers = collection.grants
										.map((grant) => grant.subjectLabel)
										.join(", ");
									return (
										<TableRow key={collection.nodeId}>
											<TableCell className="type-emphasis">
												{collection.label}
											</TableCell>
											<TableCell>
												{readable === undefined ? (
													<span className="text-muted-foreground">
														Read permission unresolved
													</span>
												) : readable ? (
													<Badge variant="secondary">
														{asPlatformAdministrator
															? "You can read this collection (platform administrator)"
															: "You can read this collection"}
													</Badge>
												) : (
													<Badge variant="outline">
														You do not have read access
													</Badge>
												)}
											</TableCell>
											<TableCell className="text-muted-foreground">
												<span className="sr-only">Readers: </span>
												{readers || "No collection read grants"}
											</TableCell>
											<TableCell className="text-right">
												{client.grantCollectionRead &&
													client.revokeCollectionRead &&
													client.listGrantSubjects && (
														<Button
															type="button"
															variant="ghost"
															size="sm"
															onClick={() =>
																setEditingCollection(collection.nodeId)
															}
														>
															Manage read grants
															<span className="sr-only">
																{" "}
																for {collection.label}
															</span>
														</Button>
													)}
											</TableCell>
										</TableRow>
									);
								})}
							</TableBody>
						</Table>
					)}
				</section>
			) : canManage ? (
				<Button
					type="button"
					variant="link"
					size="sm"
					onClick={() => window.location.assign(COLLECTION_ACCESS_PATH)}
				>
					Manage who can read each collection
				</Button>
			) : (
				<p className="type-body text-muted-foreground">
					Read access to a collection is granted by an organization
					administrator, in Admin → Data sources → Collection access.
				</p>
			)}

			{canManage && reconnecting && (
				<ReconnectSource
					source={reconnecting}
					credentialOptional={reconnectSource !== undefined}
					pending={reconnectPending}
					error={reconnectError}
					onCancel={() => {
						if (!reconnectPending) setReconnecting(null);
					}}
					onSubmit={async (token) => {
						setReconnectPending(true);
						setReconnectError(undefined);
						try {
							const jobId = reconnectSource
								? await reconnectSource(
										orgId,
										reconnecting.id,
										token || undefined,
									)
								: await client.syncSource(orgId, reconnecting.id, token);
							setSyncNotice(
								token
									? `Credential replaced. Sync queued for ${reconnecting.repo}. Open History for ingestion results.`
									: `Reconnected. Sync queued for ${reconnecting.repo}. Open History for ingestion results.`,
							);
							setReconnecting(null);
							onSyncEnqueued?.(jobId);
							await list.refetch();
						} catch (error) {
							setReconnectError(messageOf(error));
						} finally {
							setReconnectPending(false);
						}
					}}
				/>
			)}
			{canManage && showConnect && (
				<ConnectGitHubForm
					// Both legs or neither: an install the panel cannot redeem on the
					// way back strands the tenant on a completed GitHub install with
					// nothing to show for it.
					onBeginAppSetup={
						beginAppSetup && completeAppSetup ? handleBeginAppSetup : undefined
					}
					appRepositories={appRepositories}
					appSetupPhase={appSetupPhase}
					appSetupError={appSetupError}
					collections={collections.isError ? [] : collections.data}
					readableNodeIds={
						scopes.isSuccess && !scopes.isError
							? [...boundaries.keys()]
							: undefined
					}
					collectionError={collections.isError}
					onSubmit={handleConnect}
					onCancel={() => setShowConnect(false)}
					isPending={addMutation.isPending}
					errorMessage={
						addMutation.isError ? messageOf(addMutation.error) : undefined
					}
				/>
			)}
		</div>
	);
}

function PanelMessage({
	children,
	tone = "muted",
}: {
	children: ReactNode;
	tone?: "muted" | "error";
}) {
	return (
		<div
			className={cn(
				"rounded-lg border border-dashed px-6 py-12 text-center text-sm",
				tone === "error" ? "text-destructive" : "text-muted-foreground",
			)}
		>
			{children}
		</div>
	);
}

/**
 * The parameters GitHub appends to the App's configured setup URL when it sends
 * the browser back: the installation it claims was installed, and the state we
 * minted. Both are required — `setup_action` is deliberately not consulted, so
 * an existing installation gaining repositories (`update`) lands here exactly as
 * a first install does.
 *
 * Returns null under SSR, where the panel renders before any address exists.
 */
function readAppSetupReturn(): {
	state: string;
	installationId: string;
	code: string;
} | null {
	if (typeof window === "undefined") return null;
	const params = new URLSearchParams(window.location.search);
	const state = params.get("state");
	const installationId = params.get("installation_id");
	// `code` is deliberately not part of the trigger. It is absent when the App
	// was registered without "Request user authorization (OAuth) during
	// installation", and the host answers that with the error naming the setting
	// — which an operator can act on, where ignoring the return says nothing.
	return state && installationId
		? { state, installationId, code: params.get("code") ?? "" }
		: null;
}

function scrubAppSetupReturn(): void {
	const params = new URLSearchParams(window.location.search);
	for (const key of ["state", "installation_id", "setup_action", "code"])
		params.delete(key);
	const query = params.toString();
	window.history.replaceState(
		null,
		"",
		`${window.location.pathname}${query ? `?${query}` : ""}${window.location.hash}`,
	);
}

function without(set: ReadonlySet<string>, id: string): ReadonlySet<string> {
	const next = new Set(set);
	next.delete(id);
	return next;
}

function messageOf(error: unknown): string {
	const message =
		error instanceof ConnectError
			? error.rawMessage
			: error instanceof Error
				? error.message
				: "unexpected error";
	return message.replace(/^rpc error: code = \w+ desc = /, "");
}

/**
 * A row's clocks. At most one of them ticks for a given source: a github
 * source's ingest is advanced by the change-set compiler and never sets
 * `lastSyncedAt`, while the pulled providers advance `lastSyncedAt` alone. So
 * the "Never" is dropped whenever there is an ingest to show — left in, it
 * would sit above live provenance telling the reader a healthy source has
 * never synced.
 */
function LastSyncCell({ source }: { source: DatasourceView }) {
	const ingest = formatIngest(source.lastIngestedAt, source.lastIngestedCommit);
	return (
		<>
			{(source.lastSyncedAt || !ingest) && (
				<div>{formatSyncedAt(source.lastSyncedAt)}</div>
			)}
			{ingest && <div className="text-xs">{ingest}</div>}
		</>
	);
}

/**
 * How each status that is not `active` presents. Active is deliberately absent:
 * it is the state of nearly every row, so badging it too would bury the states
 * that need a reader under a column of noise — and it and `unknown` would then
 * differ by their label alone.
 */
const statusPresentation: Record<
	Exclude<DatasourceStatusName, "active">,
	{ label: string; variant: "outline" | "secondary" | "destructive" }
> = {
	paused: { label: "Paused", variant: "secondary" },
	degraded: { label: "Degraded", variant: "destructive" },
	unknown: { label: "Unknown", variant: "outline" },
};

/**
 * A source's lifecycle state and, once it has left active, the reason the host
 * published. Shown in the row rather than behind History because the tenant
 * never caused this state and so has no reason to go looking for it.
 *
 * The reason renders for whatever status carries one. The wire scopes it to
 * "why the source left active" rather than to one particular way of leaving,
 * so keying it to `degraded` would drop a paused source's explanation exactly
 * as this panel used to drop a degraded one.
 *
 * Degrading clears the source's reconcile schedule and an incremental delivery
 * never lifts it, so nothing resumes on its own: the line has to name Sync, or
 * a reader who fixes the cause waits for a pull that cannot come. It also says
 * what stopped, since a red badge alone reads as "your content is gone" and
 * would send them to re-ingest content that is still there.
 *
 * A source of a provider that does not meet the connector envelope is flagged
 * whatever its status: it keeps running, and the flag says why its provider
 * cannot be connected again.
 */
function StatusCell({ source }: { source: DatasourceView }) {
	const presentation =
		source.status === "active" ? undefined : statusPresentation[source.status];
	return (
		<div className="space-y-0.5">
			{presentation && (
				<Badge variant={presentation.variant}>{presentation.label}</Badge>
			)}
			{source.statusReason && <p className="text-xs">{source.statusReason}</p>}
			{source.status === "degraded" && (
				<p className="text-xs text-muted-foreground">
					Scheduled pulls have stopped; use Sync to retry once the cause is
					fixed. Content already ingested stays readable.
				</p>
			)}
			{source.conformant === false && (
				<>
					<Badge variant="outline">Non-conformant provider</Badge>
					<p className="type-caption-plain text-muted-foreground">
						This source keeps syncing, but new sources of its provider cannot be
						connected until it meets the datasource connector requirements.
						{source.conformanceGap && ` ${source.conformanceGap}.`}
					</p>
				</>
			)}
		</div>
	);
}

const headerClass = "px-3 py-2 text-left font-medium text-muted-foreground";
const cellClass = "px-3 py-2 align-middle";
/** Prose cells wrap instead of widening the table past its column. */
const wrapClass = "min-w-32 whitespace-normal";

function SourcesTable({
	canManage,
	sources,
	boundaries,
	permissionsResolved,
	syncingIds,
	deletingIds,
	migratingIds,
	onSync,
	onDelete,
	onActivity,
	onExecution,
	onReconnect,
	onMigrateToApp,
	renderSourceDetail,
}: {
	canManage: boolean;
	sources: DatasourceView[];
	boundaries: ReadonlyMap<string, AccessibleScopeView>;
	permissionsResolved: boolean;
	syncingIds: ReadonlySet<string>;
	deletingIds: ReadonlySet<string>;
	migratingIds: ReadonlySet<string>;
	onSync: (source: DatasourceView) => void;
	onDelete: (source: DatasourceView) => void;
	onActivity?: (source: DatasourceView) => void;
	/** Present only when a consumer handed the panel an execution view. */
	onExecution?: (source: DatasourceView) => void;
	onReconnect: (source: DatasourceView) => void;
	onMigrateToApp?: (source: DatasourceView) => void;
	renderSourceDetail?: (source: DatasourceView) => ReactNode;
}) {
	// A viewer who manages nothing is offered nothing to do but read a row's
	// history or its execution, so the column is there only when there is
	// something in it.
	const showActions = canManage || !!onActivity || !!onExecution;
	return (
		<div className="overflow-x-auto rounded-lg border">
			<Table className="w-full text-sm">
				<TableHeader className="border-b bg-muted/40">
					<TableRow>
						<TableHead className={headerClass}>Repository</TableHead>
						{/* Always present, even when every row is quiet. Deriving the
						    column from the data made it appear the moment a source
						    degraded and vanish when it recovered — and `sources` refetches
						    on an interval, so the table gained or lost a column under
						    whoever was reading it, shifting every column after Status
						    sideways at exactly the moment something had just broken. An
						    always-empty cell costs the column's padding; a shifting table
						    costs the reader their place. */}
						<TableHead className={headerClass}>Status</TableHead>
						<TableHead className={headerClass}>Paths</TableHead>
						<TableHead className={headerClass}>Branch</TableHead>
						<TableHead className={headerClass}>Boundary</TableHead>
						<TableHead className={headerClass}>Webhook</TableHead>
						<TableHead className={headerClass}>Last sync dispatch</TableHead>
						{showActions && (
							<TableHead className={cn(headerClass, "text-right")}>
								Actions
							</TableHead>
						)}
					</TableRow>
				</TableHeader>
				<TableBody>
					{sources.map((source) => (
						<TableRow key={source.id} className="border-b last:border-0">
							<TableCell className={cn(cellClass, "font-mono")}>
								{source.repo}
								{renderSourceDetail && (
									<div className="mt-1 font-sans">
										{renderSourceDetail(source)}
									</div>
								)}
							</TableCell>
							<TableCell className={cn(cellClass, wrapClass)}>
								<StatusCell source={source} />
							</TableCell>
							<TableCell className={cellClass}>
								{source.paths.length === 0 ? (
									<span className="text-muted-foreground">All</span>
								) : (
									source.paths.join(", ")
								)}
								{!!source.fileExtensions?.length && (
									<div className="text-xs text-muted-foreground">
										Only {source.fileExtensions.join(", ")}
									</div>
								)}
							</TableCell>
							<TableCell className={cellClass}>
								{source.branch || "default"}
							</TableCell>
							<TableCell className={cn(cellClass, wrapClass)}>
								<BoundaryCell
									nodeId={source.boundaryNodeId}
									label={source.boundaryLabel}
									permissionsResolved={permissionsResolved}
									scope={boundaries.get(source.boundaryNodeId)}
								/>
							</TableCell>
							<TableCell className={cellClass}>
								{source.webhookConfigured
									? "Signing secret configured"
									: "Not configured"}
							</TableCell>
							<TableCell
								className={cn(cellClass, wrapClass, "text-muted-foreground")}
							>
								<LastSyncCell source={source} />
							</TableCell>
							{showActions && (
								<TableCell className={cn(cellClass, "text-right")}>
									{!canManage ? (
										<div className="inline-flex items-center gap-2">
											{onActivity && (
												<Button
													type="button"
													variant="ghost"
													size="sm"
													aria-label={`History of ${source.repo}`}
													onClick={() => onActivity(source)}
												>
													History
												</Button>
											)}
											{onExecution && (
												<Button
													type="button"
													variant="ghost"
													size="sm"
													aria-label={`Execution of ${source.repo}`}
													onClick={() => onExecution(source)}
												>
													Execution
												</Button>
											)}
										</div>
									) : (
										/* Sync is the row's one routine action; the rest sit behind
								    a menu so the table fits a content column instead of
								    pushing its last actions out of view. */
										<div className="inline-flex items-center gap-2">
											<Button
												type="button"
												variant="outline"
												size="sm"
												disabled={syncingIds.has(source.id)}
												onClick={() => onSync(source)}
											>
												{syncingIds.has(source.id) ? "Syncing…" : "Sync"}
											</Button>
											<DropdownMenu>
												<DropdownMenuTrigger
													render={
														<Button
															type="button"
															variant="ghost"
															size="sm"
															aria-label={`More actions for ${source.repo}`}
														/>
													}
												>
													More
												</DropdownMenuTrigger>
												<DropdownMenuContent
													align="end"
													className="w-auto min-w-44"
												>
													{source.provider === "github" && (
														<DropdownMenuItem
															onClick={() => onReconnect(source)}
														>
															Reconnect
														</DropdownMenuItem>
													)}
													{onMigrateToApp && source.provider === "github" && (
														<DropdownMenuItem
															disabled={migratingIds.has(source.id)}
															onClick={() => onMigrateToApp(source)}
														>
															{migratingIds.has(source.id)
																? "Moving to the App…"
																: "Use GitHub App"}
														</DropdownMenuItem>
													)}
													{onActivity && (
														<DropdownMenuItem
															onClick={() => onActivity(source)}
														>
															History
														</DropdownMenuItem>
													)}
													{onExecution && (
														<DropdownMenuItem
															onClick={() => onExecution(source)}
														>
															Execution
														</DropdownMenuItem>
													)}
													<DropdownMenuSeparator />
													<DropdownMenuItem
														variant="destructive"
														disabled={deletingIds.has(source.id)}
														onClick={() => onDelete(source)}
													>
														{deletingIds.has(source.id)
															? "Deleting…"
															: "Delete"}
													</DropdownMenuItem>
												</DropdownMenuContent>
											</DropdownMenu>
										</div>
									)}
								</TableCell>
							)}
						</TableRow>
					))}
				</TableBody>
			</Table>
		</div>
	);
}

function BoundaryCell({
	permissionsResolved,
	nodeId,
	label,
	scope,
}: {
	nodeId: string;
	/** The collection's name as the host lists it with the source. */
	label?: string;
	scope: AccessibleScopeView | undefined;
	permissionsResolved: boolean;
}) {
	if (scope) {
		return (
			<div className="space-y-0.5">
				<div>{scope.label || label || shortBoundaryId(nodeId)}</div>
				<div className="text-xs text-muted-foreground">
					{formatGrants(scope.actions)}
					{scope.viaPlatformAdministrator && " (platform administrator)"}
				</div>
			</div>
		);
	}
	return (
		<div>
			{label ? (
				<div>{label}</div>
			) : (
				<div className="font-mono text-xs">{shortBoundaryId(nodeId)}</div>
			)}
			<p className="text-xs">
				{permissionsResolved ? "No read access" : "Read permission unresolved"}
			</p>
		</div>
	);
}

/**
 * How long a finished sync's card stays up. Its result belongs on the surface
 * that ran it for long enough to be read, and no longer: History is where an
 * older sync lives, and a panel that keeps every outcome becomes a list of
 * cards above the table it is meant to introduce.
 *
 * A failure is not special-cased into permanence. The source's own Status cell
 * carries a source that went degraded, and that one does not age out.
 */
const SETTLED_SYNC_VISIBLE_MS = 600_000;

/**
 * One source's live sync, or nothing.
 *
 * A component per source rather than a loop over one hook, so each row's watch
 * has its own query and mounting a new source cannot change how many hooks the
 * panel calls.
 */
function SourceSyncWatch({
	client,
	orgId,
	source,
	onOpenExecution,
}: {
	client: DatasourceClient;
	orgId: string;
	source: DatasourceView;
	onOpenExecution?: () => void;
}) {
	// `now` comes from the hook's ticking clock rather than being read here: a
	// stalled sync produces an identical read on every poll, so the clock
	// advancing is the only thing that can re-render this — and reading it during
	// render would make two renders of the same data disagree.
	const { sync, report, now } = useSourceSync(client, orgId, source);
	if (!sync || !report) return null;
	if (!report.active) {
		const finishedAt = sync.finishedAt
			? Date.parse(sync.finishedAt)
			: Number.NaN;
		// An unparseable or absent finish stamp on a settled sync says nothing
		// about how old it is, so it is treated as old: showing it would pin a
		// card of unknown age above the table for as long as the page is open.
		if (
			Number.isNaN(finishedAt) ||
			now - finishedAt > SETTLED_SYNC_VISIBLE_MS
		) {
			return null;
		}
	}
	return (
		<SourceSyncProgress
			source={source}
			report={report}
			{...(onOpenExecution ? { onOpenExecution } : {})}
		/>
	);
}

/**
 * The consumer's rendering, or the host saying plainly that there is none.
 *
 * A consumer that renders nothing would otherwise leave an empty card directly
 * beneath a panel whose own empty state says the source has never synced — and
 * an empty answer here most often means the viewer may not read these runs, not
 * that there are none. The host cannot tell those apart (it may not name the
 * module that runs the work, let alone evaluate its permissions), so it says
 * the one true thing it knows and points at the two readings rather than
 * letting silence pick one.
 *
 * A consumer that *can* tell them apart renders `SourceExecutionRestricted`
 * instead, and never reaches this.
 */
function SourceExecutionBody({ children }: { children: ReactNode }) {
	// `null`, `undefined`, `false` and `[]` all mean "rendered nothing" from a
	// render prop, and a caller returning any of them meant the same thing.
	const rendered =
		children !== null &&
		children !== undefined &&
		children !== false &&
		!(Array.isArray(children) && children.length === 0);
	if (rendered) return <>{children}</>;
	return (
		<p role="status" className="type-body text-muted-foreground">
			No runs to show for this sync. If you expected some, you may not have
			permission to see them.
		</p>
	);
}

/**
 * The consumer's execution view for one source, in a card the host owns.
 *
 * The host reads the sync so the consumer is handed a resolved projection
 * rather than having to ask for it again, and renders the frame — heading, close
 * — so an execution view drops in looking like the rest of the panel. What is
 * inside it is entirely the consumer's.
 */
function SourceExecution({
	client,
	orgId,
	source,
	render,
	onClose,
}: {
	client: DatasourceClient;
	orgId: string;
	source: DatasourceView;
	render: (context: {
		source: DatasourceView;
		sync?: SourceSyncView;
	}) => ReactNode;
	onClose: () => void;
}) {
	const { sync, pending } = useSourceSync(client, orgId, source);
	return (
		<section aria-label={`Execution of ${source.repo}`}>
			<Card
				title={`Execution · ${source.repo}`}
				actions={
					<Button variant="outline" size="sm" onClick={onClose}>
						Close execution
					</Button>
				}
			>
				{/* The consumer is handed the resolved sync so it can key its own view
				    on it, so its view waits for that read rather than mounting against
				    an absent key and remounting when one arrives. A cached read
				    resolves well inside the delay, so opening the view normally shows
				    no indicator at all. */}
				{pending ? (
					<DelayedLoading
						active
						label={`Loading the execution of ${source.repo}`}
					/>
				) : (
					<SourceExecutionBody>
						{render({ source, ...(sync ? { sync } : {}) })}
					</SourceExecutionBody>
				)}
			</Card>
		</section>
	);
}

function SourceHistory({
	client,
	orgId,
	source,
	onClose,
}: {
	client: DatasourceClient;
	orgId: string;
	source: DatasourceView;
	onClose: () => void;
}) {
	const history = useQuery({
		queryKey: ["source-history", orgId, source.id],
		queryFn: () => client.listActivity!(orgId, source.id),
		refetchInterval: 15000,
	});
	const names: Record<string, string> = {
		"saas.datasource.source.synced": "Sync requested",
		"saas.datasource.source.added": "Source connected",
		"saas.datasource.credential.updated": "Credential replaced",
		"saas.datasource.change_set_compiled": "Files queued for ingestion",
		"saas.datasource.sync.completed": "Ingestion completed",
		"saas.datasource.sync.failed": "Sync attempt failed",
	};
	return (
		<section aria-label="Sync history">
			<Card
				className="space-y-3"
				title={`Sync history · ${source.repo}`}
				actions={
					<Button variant="outline" size="sm" onClick={onClose}>
						Close history
					</Button>
				}
			>
				<p className="type-body text-muted-foreground">
					Sync requests, dispatched files, and ingestion results. History
					refreshes automatically.
				</p>
				{history.isPending ? (
					<DelayedLoading active label="Loading sync history">
						<p>Loading history…</p>
					</DelayedLoading>
				) : history.error ? (
					<p role="alert">Could not load history: {messageOf(history.error)}</p>
				) : !history.data?.length ? (
					<p>No recorded activity yet.</p>
				) : (
					<ol
						className="space-y-3"
						style={{ maxHeight: 360, overflowY: "auto" }}
					>
						{history.data.map((e) => (
							<li key={e.id} className="border-t pt-3 text-sm">
								<div className="flex justify-between gap-3">
									<strong>{names[e.type] ?? e.type}</strong>
									<time className="text-xs text-muted-foreground">
										{e.at ? new Date(e.at).toLocaleString() : "Unknown time"}
									</time>
								</div>
								<p className="text-xs text-muted-foreground">
									Actor:{" "}
									{e.actor === source.id ? "Source sync worker" : e.actor}
								</p>
								<dl className="flex flex-wrap gap-x-4 gap-y-1 text-xs">
									{Object.entries(e.fields)
										.filter(([k]) => k !== "solution")
										.map(([k, v]) => (
											<div key={k}>
												<dt className="inline text-muted-foreground">
													{k.replaceAll("_", " ")}:{" "}
												</dt>
												<dd className="inline break-all">{String(v)}</dd>
											</div>
										))}
								</dl>
							</li>
						))}
					</ol>
				)}
			</Card>
		</section>
	);
}

function ReconnectSource({
	source,
	credentialOptional,
	pending,
	error,
	onSubmit,
	onCancel,
}: {
	source: DatasourceView;
	/** The client reconnects a source that holds no credential without one. */
	credentialOptional: boolean;
	pending: boolean;
	error?: string;
	onSubmit: (token: string) => Promise<void>;
	onCancel: () => void;
}) {
	const [token, setToken] = useState("");
	return (
		<form
			aria-label="Reconnect GitHub source"
			className="space-y-3 rounded-lg border p-4"
			onSubmit={(event) => {
				event.preventDefault();
				if (!pending && (credentialOptional || token.trim())) {
					const replacement = token.trim();
					setToken("");
					void onSubmit(replacement);
				}
			}}
		>
			<h3 className="font-medium">Reconnect {source.repo}</h3>
			<p className="text-sm text-muted-foreground">
				{credentialOptional
					? "Its syncs will run on your behalf from now on. Enter a new PAT to replace the saved one, or leave it empty for a public repository. A sync starts right away; your source, collection, content, and history are preserved."
					: "Replace the saved PAT and start a sync. Your source, collection, content, and history are preserved."}
			</p>
			<Label className="block text-sm">
				{credentialOptional ? "New GitHub PAT (optional)" : "New GitHub PAT"}
				<Input
					type="password"
					autoComplete="new-password"
					required={!credentialOptional}
					maxLength={4096}
					value={token}
					disabled={pending}
					onChange={(event) => setToken(event.target.value)}
				/>
			</Label>
			{error && <p role="alert">{error}</p>}
			<div className="flex gap-2">
				<Button
					type="submit"
					disabled={pending || (!credentialOptional && !token.trim())}
				>
					{pending ? "Validating and reconnecting…" : "Reconnect and sync"}
				</Button>
				<Button
					type="button"
					variant="outline"
					size="sm"
					disabled={pending}
					onClick={onCancel}
				>
					Cancel
				</Button>
			</div>
		</form>
	);
}
