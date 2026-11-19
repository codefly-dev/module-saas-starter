"use client";

import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
	Badge,
	Banner,
	Button,
	Card,
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuTrigger,
	EmptyState,
	Input,
	Label,
	Spinner,
	type StatusTone,
	useLoadingPhase,
} from "@codefly-dev/ui/layout";

import {
	QueryClient,
	QueryClientProvider,
	useQuery,
} from "@tanstack/react-query";
import { type ReactNode, useEffect, useId, useMemo, useState } from "react";
import { NoReadableCollection } from "../solution/no-readable-collection.js";
import {
	useAccessToken,
	viewerAdministersOrganization,
} from "../solution/viewer.js";
import { CollectionGrants } from "./collection-access.js";
import { ConnectGitHubForm } from "./connect-github-form.js";
import {
	ConnectSource,
	connectableProviders,
	useDatasourceCatalog,
} from "./connect-source.js";
import { EditSourceForm } from "./edit-source-form.js";
import { messageOf } from "./errors.js";
import { createDatasourceClient, type GatewayBinding } from "./gateway.js";
import { readAppSetupReturn, scrubAppSetupReturn } from "./github-app-setup.js";
import {
	useAccessibleScopes,
	useDeleteSource,
	useListSources,
	useReconcileSource,
	useUpdateSource,
} from "./queries.js";
import type { AdmittedReconcilePass } from "./reconcile-passes.js";
import type { ConnectGitHubValues, EditSourceValues } from "./schema.js";
import { SourceSyncProgress, useSourceSync } from "./sync-progress.js";
import type {
	CollectionAccessView,
	DatasourceClient,
	DatasourceStatusName,
	DatasourceView,
	SourceGovernanceView,
	SourceSyncView,
} from "./types.js";
import { cn, parsePaths } from "./util.js";

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
	 * Ties every source connected from this panel to a solution's installation:
	 * a solution's page passes its own installation and the scope its roles
	 * declare for managing sources. The host then decides each of those
	 * sources by that scope alone — an editor who administers nothing connects
	 * and changes them, an administrator holding no such role does not — and
	 * writes them only into the installation's own collections. With it,
	 * `canManage` is the solution's own answer to whether the viewer holds that
	 * scope, and defaults to false rather than to the organization tier.
	 */
	governance?: SourceGovernanceView;
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
				canManage={
					rest.canManage ??
					(rest.governance ? false : viewerAdministersOrganization(token))
				}
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
	governance,
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
	const [editing, setEditing] = useState<DatasourceView | null>(null);
	const [reconnecting, setReconnecting] = useState<DatasourceView | null>(null);
	const [reconnectPending, setReconnectPending] = useState(false);
	const [reconnectError, setReconnectError] = useState<string>();
	const beginAppSetup = client.beginGitHubAppSetup?.bind(client);
	const completeAppSetup = client.completeGitHubAppSetup?.bind(client);
	const migrateToApp = client.migrateGitHubSourceToApp?.bind(client);
	const reconnectSource = client.reconnectSource?.bind(client);
	const removeSourceCredential = client.removeSourceCredential?.bind(client);
	const canEditSource = !!client.updateSource;

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
	const [syncNotice, setSyncNotice] = useState<SyncNotice | null>(null);
	const [actionError, setActionError] = useState<string | null>(null);
	const [confirmation, setConfirmation] = useState<{
		kind: "source" | "token";
		source: DatasourceView;
	} | null>(null);
	const [confirming, setConfirming] = useState(false);
	const [confirmationError, setConfirmationError] = useState<string | null>(
		null,
	);

	const list = useListSources(client, orgId);
	const { indicator: listIndicator, quiet: listQuiet } = useLoadingPhase(
		list.isLoading,
	);
	const headingId = useId();
	// A member who manages nothing is told when they can read no collection and
	// whom to ask; an administrator reads that on each source's readers line.
	const scopes = useAccessibleScopes(client, canManage ? "" : orgId);
	const readsNothing =
		!canManage &&
		scopes.isSuccess &&
		!scopes.data.some((scope) => scope.actions.includes("read"));
	const collections = useQuery({
		queryKey: ["collection-access", orgId],
		queryFn: () => client.listCollections!(orgId),
		enabled: !!client.listCollections,
		retry: false,
		refetchInterval: 5000,
	});
	const [editingCollection, setEditingCollection] = useState<string>();
	// What was just connected, so the read grant can be offered where the person
	// already is and knows what they connected — rather than as a separate later
	// journey through Manage read grants, which is the whole of what made the
	// flow unreasonable. Held as what the form said rather than as a node id,
	// because a connect that MINTED a collection has no id until the boundary
	// listing comes back with it.
	const [justConnected, setJustConnected] = useState<{
		repo: string;
		nodeId?: string | undefined;
		label: string;
	} | null>(null);
	const listedCollections = collections.isError ? undefined : collections.data;
	// The collection the connect landed in, resolved out of the boundary listing
	// the connect invalidated. Matched by node id when an existing collection was
	// picked, and by label when one was minted — the label is what the host
	// resolved the new node from, and it is unique within the organization.
	//
	// Choosing readers for another collection wins when both are open: the
	// person asked for that one after the connect, so the offer is superseded.
	const connectedCollection =
		justConnected && !editingCollection && !collections.isError
			? collections.data?.find((collection) =>
					justConnected.nodeId
						? collection.nodeId === justConnected.nodeId
						: collection.label === justConnected.label,
				)
			: undefined;
	const [connectPending, setConnectPending] = useState(false);
	const [connectError, setConnectError] = useState<string>();
	const reconcileMutation = useReconcileSource(client);
	const deleteMutation = useDeleteSource(client);
	const updateMutation = useUpdateSource(client);

	/** Connects a repository; true when it is connected and the form may close. */
	const handleConnect = async (
		values: ConnectGitHubValues,
	): Promise<boolean> => {
		setConnectPending(true);
		setConnectError(undefined);
		try {
			// The app-install return uses the existing exported form. Keep its
			// credentials out of the mutation cache too.
			await client.addGitHubSource({
				orgId,
				repo: values.repo,
				paths: parsePaths(values.paths),
				fileExtensions: parsePaths(values.fileExtensions).map((value) =>
					value.toLowerCase(),
				),
				branch: values.branch ?? "",
				targetCollection: values.targetCollection,
				boundaryNodeId: values.boundaryNodeId || undefined,
				accessToken:
					values.method === "app" || values.method === "public"
						? undefined
						: values.accessToken,
				webhookSecret:
					values.method === "public" ? "" : (values.webhookSecret ?? ""),
				...(governance ? { governance } : {}),
			});
			setConnectedRepos((prev) => new Set(prev).add(values.repo));
			setJustConnected({
				repo: values.repo,
				nodeId: values.boundaryNodeId || undefined,
				label: values.targetCollection,
			});
			void list.refetch();
			if (client.listCollections) void collections.refetch();
			return true;
		} catch {
			setConnectError(
				"Couldn't connect the repository. Check its name and access, then paste the token again.",
			);
			return false;
		} finally {
			setConnectPending(false);
		}
	};

	// Redeeming is the host's to refuse, so the panel does not ask on behalf of a
	// viewer it can see would be refused. Derived per render rather than captured,
	// so an authority that only arrives after the first paint still redeems the
	// return leg the page loaded with.
	const appSetupClaimed = !!appSetupReturn && appSetupReturn.orgId === orgId;
	const catalog = useDatasourceCatalog(client, orgId);
	const appSetupAdmitted =
		!catalog.isError &&
		catalog.data?.some(
			(provider) =>
				provider.acceptsNewSources &&
				!provider.unsupportedReason &&
				provider.supportsOrganizationAppSetup,
		);
	const appSetupActive = appSetupClaimed && canManage && !!appSetupAdmitted;
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

	// Removing a token turns the source into a public repository. The host
	// refuses it when GitHub serves the repository only with a token, and the
	// token then stays, so the question names that outcome too.
	const handleRemoveToken = async (source: DatasourceView) => {
		if (!removeSourceCredential) return;
		setSyncNotice(null);
		setActionError(null);
		try {
			await removeSourceCredential(orgId, source.id);
			setSyncNotice({
				title: "Token removed",
				body: `${source.repo} no longer uses a token; it syncs as a public repository.`,
			});
			await list.refetch();
			setConfirmation(null);
		} catch {
			setConfirmationError(
				"Couldn’t remove the token. Check that the repository can be read without it and that you have access to manage sources.",
			);
		}
	};

	const handleMigrateToApp = async (source: DatasourceView) => {
		if (!migrateToApp) return;
		setSyncNotice(null);
		setActionError(null);
		setMigratingIds((prev) => new Set(prev).add(source.id));
		try {
			await migrateToApp(orgId, source.id);
			setSyncNotice({
				title: "Now using the GitHub app",
				body: `${source.repo} connects through the GitHub app; its saved token is no longer used.`,
			});
			await list.refetch();
		} catch (error) {
			setActionError(
				`Couldn't switch ${source.repo} to the GitHub app: ${messageOf(error)}`,
			);
		} finally {
			setMigratingIds((prev) => without(prev, source.id));
		}
	};

	// "Sync now" is an admission, not a sweep: it runs the same host operation a
	// declared reconcile schedule's occurrence does, under an effect identity
	// the press minted. The notice therefore reports the OUTCOME the operation
	// returned — the identity and the job — rather than announcing that a
	// request was sent.
	const handleSync = async (source: DatasourceView) => {
		setSyncNotice(null);
		setActionError(null);
		setSyncingIds((prev) => new Set(prev).add(source.id));
		// Per-call callbacks on a shared mutation only observe the latest call.
		// Await each request so every row reports its result and clears pending.
		try {
			const pass = await reconcileMutation.mutateAsync({
				orgId,
				id: source.id,
			});
			setSyncNotice(describePass(source.repo, pass));
			if (pass.jobId) onSyncEnqueued?.(pass.jobId);
		} catch (error) {
			setActionError(`Couldn't sync ${source.repo}: ${messageOf(error)}`);
		} finally {
			setSyncingIds((prev) => without(prev, source.id));
		}
	};

	// Editing a source replaces what it fetches, so the notice reports what the
	// host answered: the sync it queued because the scope moved, or that nothing
	// changed. The second is not a failure and must not read as one — a person
	// who reopened the dialog and saved it unchanged is owed that sentence, not
	// a "sync queued" for a sync that never ran.
	const handleEdit = async (values: EditSourceValues) => {
		if (!editing) return;
		const source = editing;
		setSyncNotice(null);
		setActionError(null);
		try {
			const jobId = await updateMutation.mutateAsync({
				orgId,
				id: source.id,
				branch: values.branch ?? "",
				paths: parsePaths(values.paths),
				fileExtensions: parsePaths(values.fileExtensions).map((value) =>
					value.toLowerCase(),
				),
				// The scope this dialog was seeded from — the same object the form
				// read its defaults out of, which is what the person was looking at
				// while they typed. The host refuses the save if the source no
				// longer holds it, so a dialog left open across someone else's
				// change cannot silently undo it.
				expected: {
					branch: source.branch,
					paths: source.paths,
					fileExtensions: source.fileExtensions ?? [],
				},
			});
			setEditing(null);
			setSyncNotice(
				jobId
					? {
							title: "Saved",
							body: `${source.repo} is syncing what you chose.`,
						}
					: {
							title: "Nothing changed",
							body: `${source.repo} already brings in exactly that.`,
						},
			);
			if (jobId) onSyncEnqueued?.(jobId);
			await list.refetch();
		} catch {
			// The dialog stays open and shows the host's refusal — an unreadable
			// branch is a correctable mistake, and closing it would discard what
			// the person typed.
			//
			// The list is refreshed anyway: one of the refusals is "the source
			// changed after you opened this", and the remedy the message gives is
			// to reopen the form. Reopening it over a stale row would seed the
			// same expectation and be refused again, so the row behind the dialog
			// has to be current before they can act on what they were told.
			await list.refetch();
		}
	};

	const handleDelete = (source: DatasourceView) => {
		setActionError(null);
		setDeletingIds((prev) => new Set(prev).add(source.id));
		deleteMutation.mutate(
			{ orgId, id: source.id },
			{
				onSuccess: () => setConfirmation(null),
				onError: () =>
					setConfirmationError(
						"Couldn’t remove this source. Ask your organization administrator to check your access.",
					),
				onSettled: () => {
					setDeletingIds((prev) => without(prev, source.id));
					setConfirming(false);
				},
			},
		);
	};

	const sources = list.data ?? [];
	const providerNames = new Map(
		(catalog.data ?? []).map((provider) => [
			provider.connector,
			provider.displayName,
		]),
	);
	const connectable = connectableProviders(catalog.data).map(
		(provider) => provider.displayName,
	);
	const collectionOf = (source: DatasourceView) =>
		listedCollections?.find(
			(collection) => collection.nodeId === source.boundaryNodeId,
		);
	// Collections no listed source writes into — one a solution made, say. This
	// page is where every collection's readers are chosen, so they stay here.
	const otherCollections =
		canManage && list.isSuccess
			? (listedCollections ?? []).filter(
					(collection) =>
						!sources.some(
							(source) => source.boundaryNodeId === collection.nodeId,
						),
				)
			: [];
	const canGrant =
		canManage &&
		!!client.grantCollectionRead &&
		!!client.revokeCollectionRead &&
		!!client.listGrantSubjects;
	const accessOpenFor = (nodeId: string) =>
		editingCollection === nodeId || connectedCollection?.nodeId === nodeId;
	const closeAccess = () => {
		setEditingCollection(undefined);
		setJustConnected(null);
	};

	return (
		<div className={cn("space-y-8", className)}>
			<section aria-labelledby={headingId} className="space-y-4">
				<div className="space-y-1">
					<h3 id={headingId} className="type-section-title">
						Connected sources
					</h3>
					<p className="type-body text-muted-foreground">
						Where this organization&apos;s content comes from, and who can read
						it.
					</p>
				</div>

				{readsNothing && (
					<div role="status">
						<NoReadableCollection
							canGrant={false}
							subject="documents to show"
						/>
					</div>
				)}
				{appSetupUnredeemed && (
					<Banner
						tone="warning"
						title="Repositories are waiting to be connected"
					>
						The GitHub app is installed, but only an organization administrator
						can connect its repositories. Ask one to finish here.
					</Banner>
				)}
				<AlertDialog
					open={!!confirmation}
					onOpenChange={(open) => {
						if (!open && !confirming) setConfirmation(null);
					}}
				>
					<AlertDialogContent>
						<AlertDialogHeader>
							<AlertDialogTitle>
								{confirmation?.kind === "token"
									? "Remove the token for"
									: "Remove"}{" "}
								{confirmation?.source.repo}?
							</AlertDialogTitle>
							<AlertDialogDescription>
								{confirmation?.kind === "token"
									? "It will sync as a public repository. If GitHub needs a token to read it, the token stays."
									: "This source will stop syncing and its saved credentials will be deleted. This cannot be undone."}
							</AlertDialogDescription>
						</AlertDialogHeader>
						{confirmationError && (
							<p role="alert" className="type-body text-destructive">
								{confirmationError}
							</p>
						)}
						<AlertDialogFooter>
							<AlertDialogCancel disabled={confirming}>
								Cancel
							</AlertDialogCancel>
							<AlertDialogAction
								variant="destructive"
								disabled={confirming}
								onClick={(event) => {
									event.preventDefault();
									if (!confirmation || confirming) return;
									setConfirming(true);
									setConfirmationError(null);
									if (confirmation.kind === "source")
										handleDelete(confirmation.source);
									else
										void handleRemoveToken(confirmation.source).finally(() =>
											setConfirming(false),
										);
								}}
							>
								{confirming
									? "Removing…"
									: confirmation?.kind === "token"
										? "Remove token"
										: "Remove source"}
							</AlertDialogAction>
						</AlertDialogFooter>
					</AlertDialogContent>
				</AlertDialog>
				{syncNotice && (
					<Banner
						tone="info"
						title={syncNotice.title}
						onDismiss={() => setSyncNotice(null)}
						dismissLabel="Dismiss"
					>
						{syncNotice.body}
					</Banner>
				)}
				{actionError && (
					<Banner
						tone="danger"
						title={actionError}
						onDismiss={() => setActionError(null)}
						dismissLabel="Dismiss the error"
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
				{executionSource && renderSourceExecution && (
					<SourceExecution
						client={client}
						orgId={orgId}
						source={executionSource}
						render={renderSourceExecution}
						onClose={() => setExecutionSource(null)}
					/>
				)}

				{/* The hook holds the timing and this branch only reads it: `quiet`
				    renders nothing rather than flash "No sources yet" over a list
				    that is still loading. */}
				{listQuiet ? null : listIndicator ? (
					<PanelMessage>Loading sources…</PanelMessage>
				) : list.isError ? (
					<PanelMessage tone="error">
						Couldn&apos;t load this organization&apos;s sources. Try again in a
						moment.
					</PanelMessage>
				) : sources.length === 0 ? (
					<EmptyState
						className="rounded-lg border border-dashed"
						heading="No sources yet"
						description={
							canManage
								? connectable.length
									? `Connect ${joinNames(connectable)} below to bring content in.`
									: "Sources you connect appear here."
								: "An organization administrator connects the sources this organization reads from."
						}
					/>
				) : (
					<ul
						aria-label="Connected sources"
						className="divide-y rounded-lg border"
					>
						{sources.map((source) => {
							const collection = collectionOf(source);
							return (
								<SourceRow
									key={source.id}
									source={source}
									providerName={providerNames.get(source.provider)}
									collection={collection}
									canManage={canManage}
									syncing={syncingIds.has(source.id)}
									deleting={deletingIds.has(source.id)}
									migrating={migratingIds.has(source.id)}
									onSync={() => void handleSync(source)}
									onRemove={() => {
										setConfirmationError(null);
										setConfirmation({ kind: "source", source });
									}}
									onEdit={
										canEditSource && source.provider === "github"
											? () => {
													updateMutation.reset();
													setEditing(source);
												}
											: undefined
									}
									onReplaceToken={
										reconnectSource &&
										source.provider === "github" &&
										source.credentialMode !== "app"
											? () => {
													setReconnectError(undefined);
													setReconnecting(source);
												}
											: undefined
									}
									onRemoveToken={
										removeSourceCredential &&
										source.provider === "github" &&
										source.credentialMode === "token"
											? () => {
													setConfirmationError(null);
													setConfirmation({ kind: "token", source });
												}
											: undefined
									}
									onUseApp={
										migrateToApp &&
										source.provider === "github" &&
										source.credentialMode !== "app"
											? () => void handleMigrateToApp(source)
											: undefined
									}
									onActivity={
										client.listActivity
											? () => setActivitySource(source)
											: undefined
									}
									onExecution={
										renderSourceExecution
											? () => setExecutionSource(source)
											: undefined
									}
									onChooseReaders={
										canGrant && collection
											? () => {
													setJustConnected(null);
													setEditingCollection(collection.nodeId);
												}
											: undefined
									}
									detail={renderSourceDetail?.(source)}
									access={
										collection && accessOpenFor(collection.nodeId) ? (
											<CollectionGrants
												client={client}
												orgId={orgId}
												collection={collection}
												connectedRepo={
													connectedCollection?.nodeId === collection.nodeId
														? justConnected?.repo
														: undefined
												}
												onDismiss={closeAccess}
											/>
										) : undefined
									}
									replaceToken={
										canManage &&
										reconnectSource &&
										reconnecting?.id === source.id ? (
											<ReplaceToken
												source={source}
												pending={reconnectPending}
												error={reconnectError}
												onCancel={() => {
													if (!reconnectPending) setReconnecting(null);
												}}
												onSubmit={async (token) => {
													setReconnectPending(true);
													setReconnectError(undefined);
													try {
														const jobId = await reconnectSource(
															orgId,
															source.id,
															token || undefined,
														);
														setSyncNotice({
															title: token ? "Token replaced" : "Reconnected",
															body: `${source.repo} is syncing with it now.`,
														});
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
										) : undefined
									}
									progress={
										client.getSourceSync ? (
											<SourceSyncWatch
												client={client}
												orgId={orgId}
												source={source}
												onOpenExecution={
													renderSourceExecution
														? () => setExecutionSource(source)
														: undefined
												}
											/>
										) : undefined
									}
								/>
							);
						})}
					</ul>
				)}

				{otherCollections.length > 0 && (
					<div className="space-y-2 pt-2">
						<h4 className="type-label">Other collections</h4>
						<p className="type-caption-plain text-muted-foreground">
							Collections no source above brings content into.
						</p>
						<ul
							aria-label="Other collections"
							className="divide-y rounded-lg border"
						>
							{otherCollections.map((collection) => (
								<li key={collection.nodeId} className="space-y-3 p-4">
									<span className="type-emphasis">{collection.label}</span>
									{!accessOpenFor(collection.nodeId) && (
										<Readers
											name={collection.label}
											readers={collection.grants.map(
												(grant) => grant.subjectLabel,
											)}
											onChoose={
												canGrant
													? () => setEditingCollection(collection.nodeId)
													: undefined
											}
										/>
									)}
									{accessOpenFor(collection.nodeId) && (
										<CollectionGrants
											client={client}
											orgId={orgId}
											collection={collection}
											onDismiss={closeAccess}
										/>
									)}
								</li>
							))}
						</ul>
					</div>
				)}
			</section>

			{canManage && (
				<ConnectSource
					key={orgId}
					client={client}
					orgId={orgId}
					governance={governance}
					collections={listedCollections}
					initialConnector={appSetupReturn ? "github" : undefined}
					renderForm={(provider, close) =>
						provider.connector === "github" ? (
							<ConnectGitHubForm
								// Both legs or neither: an install the panel cannot redeem on
								// the way back strands the organization on a completed GitHub
								// install with nothing to show for it.
								onBeginAppSetup={
									beginAppSetup && completeAppSetup
										? handleBeginAppSetup
										: undefined
								}
								defaultMethod={appSetupActive ? "app" : undefined}
								appRepositories={appRepositories}
								appSetupPhase={appSetupPhase}
								appSetupError={appSetupError}
								collections={listedCollections}
								collectionError={collections.isError}
								onSubmit={async (values) => {
									if (await handleConnect(values)) close();
								}}
								onCancel={close}
								isPending={connectPending}
								errorMessage={connectError}
							/>
						) : undefined
					}
					onConnected={(boundary) => {
						setJustConnected({
							repo: boundary.targetCollection,
							label: boundary.targetCollection,
							nodeId: boundary.boundaryNodeId,
						});
						void list.refetch();
						if (client.listCollections) void collections.refetch();
					}}
				/>
			)}

			{canManage && editing && canEditSource && (
				<EditSourceForm
					source={editing}
					onSubmit={handleEdit}
					onCancel={() => {
						if (!updateMutation.isPending) setEditing(null);
					}}
					isPending={updateMutation.isPending}
					errorMessage={
						updateMutation.isError ? messageOf(updateMutation.error) : undefined
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
				"rounded-lg border border-dashed px-6 py-12 text-center type-body",
				tone === "error" ? "text-destructive" : "text-muted-foreground",
			)}
		>
			{children}
		</div>
	);
}

function without(set: ReadonlySet<string>, id: string): ReadonlySet<string> {
	const next = new Set(set);
	next.delete(id);
	return next;
}

/** "GitHub", "GitHub or Object storage", "A, B or C". */
function joinNames(names: string[]): string {
	return names.length < 2
		? (names[0] ?? "")
		: `${names.slice(0, -1).join(", ")} or ${names[names.length - 1]}`;
}

/**
 * What a source brings in, in one line: its folders, its file types and, for a
 * repository, its branch.
 */
export function describeContents(source: DatasourceView): string {
	const parts = [source.paths.length ? source.paths.join(", ") : "All files"];
	if (source.fileExtensions?.length)
		parts.push(`${source.fileExtensions.join(", ")} files`);
	if (source.provider === "github")
		parts.push(source.branch ? `${source.branch} branch` : "default branch");
	return parts.join(" · ");
}

/**
 * How the source connects, in the words a person chose it by. A saved token is
 * named and never shown; an older host that does not say gets no claim at all.
 */
function describeCredential(source: DatasourceView): string | undefined {
	switch (source.credentialMode) {
		case "token":
			return source.provider === "github" ? "Token saved" : "Key saved";
		case "app":
			return "GitHub app";
		case "personal":
			return "Your sign-in";
		case "public":
			return source.provider === "github" ? "Public repository" : undefined;
	}
	return undefined;
}

/**
 * When the source last brought content in. At most one of the two clocks
 * ticks for a given source — a repository advances its ingest, a pulled
 * provider its sync — so whichever is set is the answer.
 */
function describeLastSync(source: DatasourceView): string {
	const at = source.lastIngestedAt ?? source.lastSyncedAt;
	const when = at ? new Date(at) : undefined;
	const synced =
		when && !Number.isNaN(when.getTime())
			? `Synced ${when.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" })}`
			: "Not synced yet";
	return source.liveDelivery === "app_webhook" ||
		source.liveDelivery === "source_webhook"
		? `${synced} · updates on every push`
		: synced;
}

/**
 * How each status presents. Paused waits on someone and is not a fault, so it
 * is a warning rather than the red of a source that stopped.
 */
const statusPresentation: Record<
	DatasourceStatusName,
	{ label: string; tone: StatusTone }
> = {
	active: { label: "Active", tone: "success" },
	paused: { label: "Paused", tone: "warning" },
	degraded: { label: "Needs attention", tone: "danger" },
	unknown: { label: "Status unknown", tone: "neutral" },
};

/** Who can read a collection, and the way to change it. */
function Readers({
	name,
	readers,
	onChoose,
}: {
	/** What the source or collection is called, for the button's accessible name. */
	name: string;
	readers: string[];
	onChoose?: (() => void) | undefined;
}) {
	return (
		<div className="flex flex-wrap items-center justify-between gap-2 rounded-md bg-muted/50 px-3 py-2">
			<p className="type-caption-plain">
				<span className="type-label">Who can read it: </span>
				{readers.length ? (
					readers.join(", ")
				) : (
					<span className="text-muted-foreground">
						{onChoose
							? "Nobody yet. Connecting doesn't share anything; choose who can read it."
							: "Nobody yet."}
					</span>
				)}
			</p>
			{onChoose && (
				<Button
					type="button"
					variant="outline"
					size="sm"
					aria-label={`Choose who can read ${name}`}
					onClick={onChoose}
				>
					{readers.length ? "Change" : "Choose"}
				</Button>
			)}
		</div>
	);
}

/**
 * One connected source: what it is, what it brings in, when it last synced,
 * whether it is healthy, who can read it, and what can be done to it.
 */
function SourceRow({
	source,
	providerName,
	collection,
	canManage,
	syncing,
	deleting,
	migrating,
	onSync,
	onRemove,
	onEdit,
	onReplaceToken,
	onRemoveToken,
	onUseApp,
	onActivity,
	onExecution,
	onChooseReaders,
	detail,
	access,
	replaceToken,
	progress,
}: {
	source: DatasourceView;
	providerName: string | undefined;
	collection: CollectionAccessView | undefined;
	canManage: boolean;
	syncing: boolean;
	deleting: boolean;
	migrating: boolean;
	onSync: () => void;
	onRemove: () => void;
	/** Absent when the client cannot edit this source; no control is offered. */
	onEdit?: (() => void) | undefined;
	onReplaceToken?: (() => void) | undefined;
	/** Present only for a repository connected with a token. */
	onRemoveToken?: (() => void) | undefined;
	onUseApp?: (() => void) | undefined;
	onActivity?: (() => void) | undefined;
	/** Present only when a consumer handed the panel an execution view. */
	onExecution?: (() => void) | undefined;
	onChooseReaders?: (() => void) | undefined;
	detail?: ReactNode;
	access?: ReactNode;
	replaceToken?: ReactNode;
	progress?: ReactNode;
}) {
	const name = source.repo || source.boundaryLabel || "Untitled source";
	const status = statusPresentation[source.status];
	const lands = collection?.label ?? source.boundaryLabel;
	const more = [
		canManage &&
			onReplaceToken && {
				// A public repository has no token to replace; it can be given one.
				label:
					source.credentialMode === "public" ? "Add a token" : "Replace token",
				run: onReplaceToken,
			},
		canManage && onRemoveToken && { label: "Remove token", run: onRemoveToken },
		canManage &&
			onUseApp && {
				label: migrating
					? "Switching to the GitHub app…"
					: "Use the GitHub app",
				run: onUseApp,
				disabled: migrating,
			},
		onActivity && { label: "History", run: onActivity },
		onExecution && { label: "Sync details", run: onExecution },
	].filter((item) => !!item) as {
		label: string;
		run: () => void;
		disabled?: boolean;
	}[];
	return (
		<li className="space-y-3 p-4">
			<div className="flex flex-wrap items-start justify-between gap-3">
				<div className="min-w-0 flex-1 basis-64 space-y-1">
					<div className="flex flex-wrap items-center gap-2">
						<span className="type-emphasis [overflow-wrap:anywhere]">
							{name}
						</span>
						<Badge tone={status.tone} dot>
							{status.label}
						</Badge>
					</div>
					<p className="type-caption-plain text-muted-foreground">
						{[
							providerName,
							describeCredential(source),
							describeContents(source),
						]
							.filter(Boolean)
							.join(" · ")}
						{lands && ` → ${lands}`}
					</p>
					<p className="type-caption-plain text-muted-foreground">
						{describeLastSync(source)}
					</p>
					{source.statusReason && (
						<p className="type-caption-plain">{source.statusReason}</p>
					)}
					{source.status === "degraded" && (
						<p className="type-caption-plain text-muted-foreground">
							Automatic syncs have stopped. Fix the cause, then use Sync now;
							what it already brought in stays readable.
						</p>
					)}
					{source.conformant === false && (
						<p className="type-caption-plain text-muted-foreground">
							This kind of source can no longer be added, but this one keeps
							syncing.
						</p>
					)}
					{detail}
				</div>
				<div className="flex flex-wrap items-center gap-1.5">
					{canManage && (
						<>
							<Button
								type="button"
								variant="outline"
								size="sm"
								aria-label={`${syncing ? "Syncing" : "Sync now"} ${name}`}
								disabled={syncing}
								onClick={onSync}
							>
								{syncing ? "Syncing…" : "Sync now"}
							</Button>
							{onEdit && (
								<Button
									type="button"
									variant="ghost"
									size="sm"
									aria-label={`Edit ${name}`}
									onClick={onEdit}
								>
									Edit
								</Button>
							)}
							<Button
								type="button"
								variant="ghost"
								size="sm"
								className="text-destructive"
								aria-label={`Remove ${name}`}
								disabled={deleting}
								onClick={onRemove}
							>
								{deleting ? "Removing…" : "Remove"}
							</Button>
						</>
					)}
					{more.length > 0 && (
						<DropdownMenu>
							<DropdownMenuTrigger
								render={
									<Button
										type="button"
										variant="ghost"
										size="sm"
										aria-label={`More for ${name}`}
									/>
								}
							>
								More
							</DropdownMenuTrigger>
							<DropdownMenuContent align="end" className="w-auto min-w-44">
								{more.map((item) => (
									<DropdownMenuItem
										key={item.label}
										disabled={item.disabled}
										onClick={item.run}
									>
										{item.label}
									</DropdownMenuItem>
								))}
							</DropdownMenuContent>
						</DropdownMenu>
					)}
				</div>
			</div>
			{/* While its readers are being chosen, the panel below lists them. */}
			{collection && !access && (
				<Readers
					name={name}
					readers={collection.grants.map((grant) => grant.subjectLabel)}
					onChoose={onChooseReaders}
				/>
			)}
			{access}
			{replaceToken}
			{progress}
		</li>
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
	const { indicator: executionIndicator, quiet: executionQuiet } =
		useLoadingPhase(pending);
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
				    no indicator at all — and the timing lives in the hook above this
				    branch, so an answer landing just past the delay still leaves the
				    indicator up long enough to read. */}
				{executionQuiet ? null : executionIndicator ? (
					<Spinner label={`Loading the execution of ${source.repo}`} />
				) : (
					<SourceExecutionBody>
						{render({ source, ...(sync ? { sync } : {}) })}
					</SourceExecutionBody>
				)}
			</Card>
		</section>
	);
}

/** A UUID, wherever it appears in a value. */
const IDENTIFIER =
	/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/i;

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
	const { indicator: historyIndicator, quiet: historyQuiet } = useLoadingPhase(
		history.isPending,
	);
	const names: Record<string, string> = {
		"saas.datasource.source.synced": "Sync requested",
		"saas.datasource.source.added": "Source connected",
		"saas.datasource.credential.updated": "Token replaced",
		"saas.datasource.change_set_compiled": "Changed files found",
		"saas.datasource.sync.completed": "Sync finished",
		"saas.datasource.sync.failed": "Sync attempt failed",
	};
	return (
		<section aria-label="Sync history">
			<Card
				className="space-y-3"
				title={`History · ${source.repo}`}
				actions={
					<Button variant="outline" size="sm" onClick={onClose}>
						Close history
					</Button>
				}
			>
				<p className="type-body text-muted-foreground">
					Syncs, changes and results, newest first.
				</p>
				{/* Same shape as the source list: the timing lives in the hook above the
				    branch, and the pre-delay window renders nothing rather than "No
				    recorded activity yet." over a history that is still loading. */}
				{historyQuiet ? null : historyIndicator ? (
					<p>Loading history…</p>
				) : history.error ? (
					<p role="alert">Could not load history: {messageOf(history.error)}</p>
				) : !history.data?.length ? (
					<p>No recorded activity yet.</p>
				) : (
					<ol className="max-h-96 space-y-3 overflow-y-auto">
						{history.data.map((e) => (
							<li key={e.id} className="border-t pt-3 type-body">
								<div className="flex justify-between gap-3">
									<strong>{names[e.type] ?? "Activity"}</strong>
									<time className="type-caption-plain text-muted-foreground">
										{e.at ? new Date(e.at).toLocaleString() : "Unknown time"}
									</time>
								</div>
								<dl className="flex flex-wrap gap-x-4 gap-y-1 type-caption-plain">
									{/* Identifiers — the actor, a job, an entry — are an
									    operator's handles and stay out of a person's history. */}
									{Object.entries(e.fields)
										.filter(
											([k, v]) =>
												k !== "solution" &&
												!/(^|_)(id|job|actor|effect)/.test(k) &&
												!IDENTIFIER.test(String(v)),
										)
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

/** What the panel says about one action, with the heading it says it under. */
interface SyncNotice {
	title: string;
	body: string;
}

/**
 * What a person is told after pressing Sync now. The pass's own identity and
 * its job are an operator's handles, not the administrator's, so the notice
 * says only what happened to their source:
 *
 * - started — new work;
 * - already running — the press repeated one the host already holds, so nothing
 *   was sent twice;
 * - unconfirmed — the host did not report it as committed, which is
 *   inconclusive rather than refused, so pressing again recovers the same pass.
 */
function describePass(repo: string, pass: AdmittedReconcilePass): SyncNotice {
	if (pass.repeat)
		return {
			title: "Already syncing",
			body: `${repo} is already syncing; nothing was sent twice.`,
		};
	if (pass.status !== "committed")
		return {
			title: "Sync not confirmed",
			body: `We couldn't confirm the sync of ${repo}. Press Sync now again; it picks up the same sync rather than starting another.`,
		};
	return {
		title: "Sync started",
		body: `${repo} is syncing. New content appears in a few minutes.`,
	};
}

/**
 * Replaces the token a repository connects with. The saved one is never shown:
 * the field starts empty and is cleared the moment it is sent.
 */
function ReplaceToken({
	source,
	pending,
	error,
	onSubmit,
	onCancel,
}: {
	source: DatasourceView;
	pending: boolean;
	error?: string | undefined;
	onSubmit: (token: string) => Promise<void>;
	onCancel: () => void;
}) {
	const id = useId();
	const [token, setToken] = useState("");
	return (
		<form
			aria-label={`Replace the token for ${source.repo}`}
			className="space-y-3 rounded-lg border p-4"
			onSubmit={(event) => {
				event.preventDefault();
				if (!pending) {
					const replacement = token.trim();
					setToken("");
					void onSubmit(replacement);
				}
			}}
		>
			{/* The credential is optional in one direction only: a source that
			    holds one is refused a credential-less reconnect by the host, which
			    is the only place that knows whether it holds one. */}
			<div className="space-y-2">
				<Label htmlFor={id}>New access token</Label>
				<Input
					id={id}
					type="password"
					autoComplete="new-password"
					spellCheck={false}
					maxLength={4096}
					value={token}
					disabled={pending}
					aria-describedby={`${id}-help`}
					onChange={(event) => setToken(event.target.value)}
				/>
				<p
					id={`${id}-help`}
					className="type-caption-plain text-muted-foreground"
				>
					A saved token is never shown. Paste a new one to replace it, or leave
					this empty to keep what is saved and reconnect as you. A sync starts
					right away.
				</p>
			</div>
			{error && (
				<p role="alert" className="type-body text-destructive">
					{error}
				</p>
			)}
			<div className="flex flex-wrap gap-2">
				<Button type="submit" size="sm" disabled={pending}>
					{pending ? "Checking access…" : "Save token"}
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
