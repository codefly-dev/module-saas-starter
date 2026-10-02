"use client";

import {
	Badge,
	Banner,
	Button,
	Card,
	useLoadingPhase,
} from "@codefly-dev/ui/layout";
import {
	QueryClient,
	QueryClientProvider,
	useQuery,
} from "@tanstack/react-query";
import { type ReactNode, useEffect, useId, useMemo, useState } from "react";
import {
	useAccessToken,
	viewerAdministersOrganization,
} from "../solution/viewer.js";
import {
	AccessTokenField,
	AppInstallPrompt,
	type CredentialMethod,
	CredentialMethodField,
	fieldErrorClass,
	WebhookSecretField,
} from "./credential-mode.js";
import {
	type DeclaredSource,
	declaredCollectionLabel,
	matchDeclaredSources,
} from "./declared-source.js";
import { messageOf } from "./errors.js";
import { createDatasourceClient, type GatewayBinding } from "./gateway.js";
import { readAppSetupReturn, scrubAppSetupReturn } from "./github-app-setup.js";
import { useAddGitHubSource, useListSources } from "./queries.js";
import type { DatasourceClient, DatasourceView } from "./types.js";
import {
	cn,
	formatIngest,
	formatLiveDelivery,
	formatReconcileInterval,
	formatSyncedAt,
} from "./util.js";

interface DeclaredSourceCardBaseProps {
	orgId: string;
	/**
	 * The source the solution declares it is built on. Read, never edited: the
	 * repository, its paths and its ref are the solution's statement about
	 * itself, so this card renders them and submits them, and offers no control
	 * that could change them.
	 */
	declared: DeclaredSource;
	/**
	 * Whether the viewer may connect, reconnect or sync — the host's
	 * organization-administrator tier. Without it the card still says what the
	 * source is and how it is doing, and offers nothing to do about it. The host
	 * refuses the calls either way. Default: with a `gateway`, read from the
	 * viewer's credential; with an injected `client`, true.
	 */
	canManage?: boolean;
	/** Called with the durable job id after a sync is enqueued. */
	onSyncEnqueued?: (jobId: string) => void;
	/**
	 * Renders beneath the connected source: the same extension point
	 * `DatasourcesPanel` offers, through which the consumer shows what the
	 * module that ingests this source's files has made of them. The host names
	 * no such module.
	 */
	renderSourceDetail?: (source: DatasourceView) => ReactNode;
	className?: string;
}

/**
 * One declared source, in one card, in one of three states: **Set up**,
 * **Connected**, **Error**.
 *
 * For a solution built on one known repository, the repository is not a
 * question. The solution declares it (`sources:` in its registration manifest,
 * handed back to the remote as `SolutionBinding.declaredSources`); the person
 * supplies only the credential, in the same three modes the full connect form
 * offers, because both render the kit's one credential block.
 *
 * It reads the organization's connected sources and matches the declaration
 * against them. It adds no RPC of its own and takes no query or auth context:
 * either drive it with an injected `client`, or hand it a `gateway` binding and
 * it self-wires, exactly like `DatasourcesPanel`.
 *
 * Do not mount this and `DatasourcesPanel` on the same page: both redeem the
 * GitHub App's single-use return state, and the loser reports a rejection for
 * an installation that succeeded.
 */
export type DeclaredSourceCardProps = DeclaredSourceCardBaseProps &
	(
		| { client: DatasourceClient; gateway?: never }
		| { gateway: GatewayBinding; client?: never }
	);

export function DeclaredSourceCard(props: DeclaredSourceCardProps) {
	if (props.gateway) {
		const { gateway, ...rest } = props;
		return <GatewayBoundCard gateway={gateway} {...rest} />;
	}
	const { client, ...rest } = props;
	return <DeclaredSourceCardView client={client} {...rest} />;
}

function GatewayBoundCard({
	gateway,
	...rest
}: DeclaredSourceCardBaseProps & { gateway: GatewayBinding }) {
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
			<DeclaredSourceCardView
				client={client}
				{...rest}
				canManage={rest.canManage ?? viewerAdministersOrganization(token)}
			/>
		</QueryClientProvider>
	);
}

function DeclaredSourceCardView({
	client,
	orgId,
	declared,
	canManage = true,
	onSyncEnqueued,
	renderSourceDetail,
	className,
}: DeclaredSourceCardBaseProps & { client: DatasourceClient }) {
	const list = useListSources(client, orgId);
	const { indicator, quiet } = useLoadingPhase(list.isLoading);
	const matches = useMemo(
		() => matchDeclaredSources(declared, list.data ?? []),
		[declared, list.data],
	);
	const source = matches.length === 1 ? matches[0] : undefined;

	const [syncing, setSyncing] = useState(false);
	const [notice, setNotice] = useState<string | null>(null);
	const [actionError, setActionError] = useState<string | null>(null);

	const beginAppSetup = client.beginGitHubAppSetup?.bind(client);
	const completeAppSetup = client.completeGitHubAppSetup?.bind(client);
	const reconnectSource = client.reconnectSource?.bind(client);
	const appSetup = useAppSetupReturn(orgId, canManage, completeAppSetup);
	const addMutation = useAddGitHubSource(client);
	const [reconnectPending, setReconnectPending] = useState(false);
	const [reconnectError, setReconnectError] = useState<string>();

	const handleSync = async () => {
		if (!source) return;
		setNotice(null);
		setActionError(null);
		setSyncing(true);
		try {
			const jobId = await client.syncSource(orgId, source.id);
			setNotice(
				"Sync queued. Ingestion runs in the background; content appears when it is ready.",
			);
			onSyncEnqueued?.(jobId);
		} catch (error) {
			setActionError(`Couldn't sync ${source.repo}: ${messageOf(error)}`);
		} finally {
			setSyncing(false);
		}
	};

	const state: DeclaredSourceState =
		matches.length > 1
			? "ambiguous"
			: !source
				? "setup"
				: source.status === "active"
					? "connected"
					: "error";

	return (
		<Card
			className={cn("space-y-4", className)}
			title={declared.label?.trim() || declared.repo}
			actions={
				list.isSuccess ? <StateBadge state={state} source={source} /> : null
			}
		>
			<Declaration declared={declared} />

			{/* The capture and the scrub ran regardless of authority — they have
			    to, because the credential `canManage` is read from may not have
			    arrived yet on the load that follows the redirect. So a viewer who
			    really cannot manage has just spent a single-use state on an
			    installation that stands at GitHub, and saying nothing would leave
			    an ordinary card over a connect that never happened. Held until
			    the list has answered SUCCESSFULLY: a credential not yet read also
			    reads as "not an administrator", and a failed list proves nothing
			    and already shows its own error. */}
			{appSetup.unredeemable && list.isSuccess && (
				<Banner title="The installation finished, but it is not yours to connect">
					The GitHub App is installed. Only an organization administrator can
					connect this source with it; ask one to open this page. The
					installation itself is already in place.
				</Banner>
			)}

			{/* Every state below is a claim about a read that ANSWERED, so the
			    body is gated on success rather than on `isLoading` being false. A
			    query disabled for want of an org id sits at `isPending` with
			    `fetchStatus: "idle"`, so `isLoading` is false and a branch keyed on
			    it falls straight through to "Set up" — offering a connect form for
			    a source nobody has looked for yet, which may already be connected.
			    The hook holds the indicator's timing; `quiet` is its pre-delay
			    window, which renders nothing rather than flashing. */}
			{list.isError ? (
				<p role="alert" className="type-body text-destructive">
					Couldn&apos;t read this organization&apos;s sources, so the state of
					this one is unknown. It has not been reported as disconnected.{" "}
					{messageOf(list.error)}
				</p>
			) : !list.isSuccess ? (
				quiet || !indicator ? null : (
					<p role="status" className="type-body text-muted-foreground">
						Checking this source…
					</p>
				)
			) : state === "ambiguous" ? (
				<AmbiguousMatches declared={declared} matches={matches} />
			) : state === "setup" ? (
				canManage ? (
					<CredentialForm
						heading="Connect this source"
						intro="The repository, paths and branch come from the solution. Choose how this organization reads them."
						submitLabel="Connect"
						pendingLabel="Validating GitHub access…"
						offerWebhookSecret
						pending={addMutation.isPending}
						{...(addMutation.isError
							? { error: messageOf(addMutation.error) }
							: {})}
						appAvailable={!!beginAppSetup && !!completeAppSetup}
						appDescription="Installing sends you to GitHub to grant this organization read access to the repository above. You come back here; there is nothing else to choose."
						appInstalled={appSetup.installed}
						{...(appSetup.phase ? { appSetupPhase: appSetup.phase } : {})}
						{...(appSetup.error ? { appSetupError: appSetup.error } : {})}
						onBeginAppSetup={
							beginAppSetup && completeAppSetup
								? () => void appSetup.begin(beginAppSetup, orgId)
								: undefined
						}
						onSubmit={(credential) =>
							addMutation.mutate({
								orgId,
								repo: declared.repo,
								paths: declared.paths ?? [],
								branch: declared.ref ?? "",
								targetCollection: declaredCollectionLabel(declared),
								// Neither the App nor a public repository sends a token:
								// the host resolves the installation, or confirms with
								// GitHub that the repository is public. A public source
								// takes no webhook either.
								...(credential.method === "pat"
									? { accessToken: credential.accessToken }
									: {}),
								webhookSecret:
									credential.method === "public"
										? ""
										: credential.webhookSecret,
							})
						}
					/>
				) : (
					<p className="type-body text-muted-foreground">
						This source is not connected yet. An organization administrator
						connects it; nothing is needed from you.
					</p>
				)
			) : source ? (
				<>
					<SourceState source={source} />
					{renderSourceDetail && <div>{renderSourceDetail(source)}</div>}
					{notice && (
						<Banner
							title="Sync queued"
							onDismiss={() => setNotice(null)}
							dismissLabel="Dismiss the sync notice"
						>
							{notice}
						</Banner>
					)}
					{actionError && (
						<p role="alert" className={fieldErrorClass}>
							{actionError}
						</p>
					)}
					{canManage && state === "connected" && (
						<Button
							type="button"
							variant="outline"
							size="sm"
							disabled={syncing}
							onClick={() => void handleSync()}
						>
							{syncing ? "Syncing…" : "Sync now"}
						</Button>
					)}
					{canManage && state === "error" && (
						<CredentialForm
							heading="Reconnect this source"
							intro="Supply the credential again. The source, its collection, its content and its history are preserved, and a sync starts right away."
							submitLabel="Reconnect and sync"
							pendingLabel="Validating and reconnecting…"
							pending={reconnectPending}
							{...(reconnectError ? { error: reconnectError } : {})}
							appAvailable={!!beginAppSetup && !!completeAppSetup}
							appDescription="Installing sends you to GitHub to grant this organization read access to the repository above. Use it if the App's access to this repository was removed."
							appInstalled={appSetup.installed}
							{...(appSetup.phase ? { appSetupPhase: appSetup.phase } : {})}
							{...(appSetup.error ? { appSetupError: appSetup.error } : {})}
							onBeginAppSetup={
								beginAppSetup && completeAppSetup
									? () => void appSetup.begin(beginAppSetup, orgId)
									: undefined
							}
							onSubmit={async (credential) => {
								setReconnectPending(true);
								setReconnectError(undefined);
								setActionError(null);
								const token =
									credential.method === "pat"
										? credential.accessToken
										: undefined;
								try {
									const jobId = reconnectSource
										? await reconnectSource(orgId, source.id, token)
										: await client.syncSource(orgId, source.id, token);
									setNotice(
										"Reconnected. Sync queued; ingestion runs in the background.",
									);
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
					{!canManage && state === "error" && (
						<p className="type-body text-muted-foreground">
							An organization administrator reconnects this source.
						</p>
					)}
				</>
			) : null}
		</Card>
	);
}

type DeclaredSourceState = "setup" | "connected" | "error" | "ambiguous";

/**
 * The three states the declaration can be in, plus the one it must never
 * silently resolve. "Set up" is a state, not an instruction: it is what the
 * card says when nothing connected serves the declaration, whether or not the
 * reader can do anything about it.
 */
function StateBadge({
	state,
	source,
}: {
	state: DeclaredSourceState;
	source: DatasourceView | undefined;
}) {
	if (state === "connected")
		return <Badge variant="secondary">Connected</Badge>;
	if (state === "setup") return <Badge variant="outline">Set up</Badge>;
	if (state === "ambiguous")
		return <Badge variant="destructive">More than one source</Badge>;
	return (
		<Badge variant="destructive">
			Error{source?.status === "paused" ? " · paused" : ""}
		</Badge>
	);
}

/** What the solution declared, as text. Never a control: it is not editable. */
function Declaration({ declared }: { declared: DeclaredSource }) {
	const paths = declared.paths ?? [];
	return (
		<dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 type-body">
			<dt className="text-muted-foreground">Repository</dt>
			<dd className="font-mono">{declared.repo}</dd>
			<dt className="text-muted-foreground">Paths</dt>
			<dd className={paths.length ? "font-mono" : undefined}>
				{paths.length ? paths.join(", ") : "The whole repository"}
			</dd>
			<dt className="text-muted-foreground">Branch</dt>
			<dd className={declared.ref ? "font-mono" : undefined}>
				{declared.ref || "The repository default"}
			</dd>
		</dl>
	);
}

/**
 * Two connected sources serve one declaration. Reported rather than resolved:
 * picking one would show its status and sync its content while the other kept
 * ingesting invisibly, and the two can disagree about everything that matters
 * — credential, branch, scope, health.
 */
function AmbiguousMatches({
	declared,
	matches,
}: {
	declared: DeclaredSource;
	matches: DatasourceView[];
}) {
	return (
		<Banner title="More than one source matches this declaration">
			<p>
				{`${matches.length} connected sources read `}
				<span className="font-mono">{declared.repo}</span>
				{`, so this card cannot say which one serves the solution. An organization administrator should remove the ones that do not, in Admin → Data sources.`}
			</p>
			{/* Each one by its id: every other column can be identical, and the
			    id is what names the row to remove in Admin → Data sources. */}
			<ul>
				{matches.map((match) => (
					<li key={match.id} className="font-mono">
						{`${match.id} · ${match.branch || "default branch"} · ${
							match.paths.length
								? match.paths.join(", ")
								: "whole repository"
						} · ${match.status}`}
					</li>
				))}
			</ul>
		</Banner>
	);
}

/** How the connected source is doing, in the host's own words. */
function SourceState({ source }: { source: DatasourceView }) {
	const ingest = formatIngest(source.lastIngestedAt, source.lastIngestedCommit);
	const delivery = formatLiveDelivery(source.liveDelivery);
	const reconcile = formatReconcileInterval(source.reconcileIntervalSeconds);
	return (
		<div className="space-y-1 type-body">
			{/* At most one of the two clocks ticks for a given source: a github
			    source's ingest is advanced by the change-set compiler and never
			    sets lastSyncedAt. Showing "Never" above live provenance would tell
			    a reader a healthy source has never synced. */}
			<p>{ingest ?? `Last sync ${formatSyncedAt(source.lastSyncedAt)}`}</p>
			{delivery === undefined ? (
				<p className="text-muted-foreground">
					This host does not report how live updates reach this source.
				</p>
			) : (
				<p className="text-muted-foreground">
					{delivery}
					{reconcile ? ` · ${reconcile}` : ""}
				</p>
			)}
			{/* Verbatim. The host writes status_reason from a closed set of named
			    reasons — never the tenant, never raw provider error text — so it is
			    safe to render as it arrives, and rewording it would lose the one
			    thing a reader can act on. */}
			{source.statusReason && <p role="status">{source.statusReason}</p>}
			{source.status === "degraded" && (
				<p className="text-muted-foreground">
					Scheduled pulls have stopped; reconnecting retries once the cause is
					fixed. Content already ingested stays readable.
				</p>
			)}
		</div>
	);
}

/** What the person supplies: the credential, and nothing else. */
interface CredentialSubmission {
	method: CredentialMethod;
	accessToken: string;
	webhookSecret: string;
}

function CredentialForm({
	heading,
	intro,
	submitLabel,
	pendingLabel,
	pending,
	error,
	offerWebhookSecret = false,
	appAvailable,
	appDescription,
	appInstalled,
	appSetupPhase,
	appSetupError,
	onBeginAppSetup,
	onSubmit,
}: {
	heading: string;
	intro: string;
	submitLabel: string;
	pendingLabel: string;
	pending: boolean;
	error?: string;
	offerWebhookSecret?: boolean;
	appAvailable: boolean;
	appDescription: string;
	appInstalled: boolean;
	appSetupPhase?: "beginning" | "completing";
	appSetupError?: string;
	onBeginAppSetup?: () => void;
	onSubmit: (credential: CredentialSubmission) => void;
}) {
	const fieldId = useId();
	const idFor = (name: string) => `${fieldId}-${name}`;
	const [method, setMethod] = useState<CredentialMethod>(
		appAvailable ? "app" : "pat",
	);
	const [accessToken, setAccessToken] = useState("");
	const [webhookSecret, setWebhookSecret] = useState("");
	// The host refuses a connect with no credential on the PAT path, so the
	// button is not offered for a submission that cannot succeed.
	const incomplete = method === "pat" && !accessToken.trim();

	return (
		<form
			aria-label={heading}
			className="space-y-4 rounded-lg border p-4"
			onSubmit={(event) => {
				event.preventDefault();
				if (pending || incomplete) return;
				onSubmit({
					method,
					accessToken: accessToken.trim(),
					webhookSecret: webhookSecret.trim(),
				});
			}}
		>
			<div className="space-y-1">
				<h4 className="type-section-title">{heading}</h4>
				<p className="type-body text-muted-foreground">{intro}</p>
			</div>

			<CredentialMethodField
				id={idFor("method")}
				value={method}
				appAvailable={appAvailable}
				onChange={setMethod}
			/>

			{method === "app" && onBeginAppSetup && (
				<>
					<AppInstallPrompt
						description={appDescription}
						onBeginAppSetup={onBeginAppSetup}
						{...(appSetupPhase ? { phase: appSetupPhase } : {})}
						{...(appSetupError ? { error: appSetupError } : {})}
					/>
					{appInstalled && (
						<p role="status" className="type-body">
							The installation is in place. Connect to finish.
						</p>
					)}
					<p className="type-caption-plain text-muted-foreground">
						If this organization already installed the App on this repository,
						there is nothing to install — continue.
					</p>
				</>
			)}

			{method === "pat" && (
				<AccessTokenField
					id={idFor("token")}
					errorId={idFor("token-error")}
					inputProps={{
						value: accessToken,
						autoComplete: "new-password",
						maxLength: 1024,
						disabled: pending,
						onChange: (event) => setAccessToken(event.target.value),
					}}
				/>
			)}

			{offerWebhookSecret && method !== "public" && (
				<WebhookSecretField
					id={idFor("secret")}
					errorId={idFor("secret-error")}
					inputProps={{
						value: webhookSecret,
						autoComplete: "new-password",
						maxLength: 1024,
						disabled: pending,
						onChange: (event) => setWebhookSecret(event.target.value),
					}}
				/>
			)}

			{error && (
				<p role="alert" className={fieldErrorClass}>
					{error}
				</p>
			)}

			<Button
				type="submit"
				disabled={pending || incomplete}
				aria-busy={pending}
			>
				{pending ? pendingLabel : submitLabel}
			</Button>
		</form>
	);
}

/**
 * The GitHub App's return leg, redeemed once.
 *
 * Same shape as the panel's: the parameters are captured in a lazy initializer
 * because they are a property of the address the page loaded with and the
 * scrub below destroys them; redemption is a `useQuery` that never retries,
 * because the state is single-use and a retry would report a rejection for an
 * installation that in fact succeeded; and whether the viewer may redeem is
 * decided every render rather than frozen, since the credential `canManage` is
 * read from may not have arrived on the load that follows the redirect.
 */
function useAppSetupReturn(
	orgId: string,
	canManage: boolean,
	completeAppSetup: DatasourceClient["completeGitHubAppSetup"] | undefined,
): {
	installed: boolean;
	/** A return leg this viewer may not redeem, so nothing on screen will. */
	unredeemable: boolean;
	phase: "beginning" | "completing" | undefined;
	error: string | undefined;
	begin: (
		beginAppSetup: NonNullable<DatasourceClient["beginGitHubAppSetup"]>,
		orgId: string,
	) => Promise<void>;
} {
	const [appSetupReturn] = useState(() => {
		if (!completeAppSetup) return null;
		const params = readAppSetupReturn();
		return params && { ...params, orgId };
	});
	const [beginPending, setBeginPending] = useState(false);
	const [beginError, setBeginError] = useState<string>();
	const claimed = !!appSetupReturn && appSetupReturn.orgId === orgId;
	const active = claimed && canManage;
	const setup = useQuery({
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
		enabled: active,
		retry: false,
		staleTime: Number.POSITIVE_INFINITY,
		refetchOnMount: false,
		refetchOnWindowFocus: false,
	});
	useEffect(() => {
		if (appSetupReturn) scrubAppSetupReturn();
	}, [appSetupReturn]);

	return {
		installed: active && setup.isSuccess,
		unredeemable: claimed && !canManage,
		phase: beginPending
			? "beginning"
			: active && setup.isFetching
				? "completing"
				: undefined,
		error:
			beginError ??
			(active && setup.isError ? messageOf(setup.error) : undefined),
		begin: async (beginAppSetup, targetOrgId) => {
			setBeginError(undefined);
			setBeginPending(true);
			try {
				const handle = await beginAppSetup(targetOrgId);
				// Navigating away, so the pending flag is deliberately left set: the
				// button must not re-enable under a browser that is already
				// unloading.
				window.location.assign(handle.installUrl);
			} catch (error) {
				setBeginError(messageOf(error));
				setBeginPending(false);
			}
		},
	};
}
