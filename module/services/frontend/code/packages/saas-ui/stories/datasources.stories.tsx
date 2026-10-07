import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import {
	ConnectGitHubForm,
	type DatasourceClient,
	DatasourcesPanel,
	type DatasourceView,
	DeclaredSourceCard,
	type DeclaredSource,
	describeSync,
	SourceExecutionRestricted,
	SourceSyncProgress,
	type SourceSyncView,
} from "../src/index.js";

export default { title: "SaaS UI/Datasources" };
const source: DatasourceView = {
	id: "example-source",
	orgId: "example-org",
	provider: "github",
	repo: "example/docs",
	paths: ["docs/"],
	branch: "main",
	boundaryNodeId: "example-boundary",
	webhookConfigured: true,
	status: "active",
	lastSyncedAt: undefined,
	createdAt: undefined,
};
const degradedSource: DatasourceView = {
	...source,
	id: "example-degraded-source",
	status: "degraded",
	statusReason:
		"snapshot manifest is 12582912 bytes, over the 8388608-byte ingest limit",
};
function Preview({
	state,
}: {
	state: "populated" | "empty" | "loading" | "error" | "degraded";
}) {
	const [queryClient] = useState(
		() => new QueryClient({ defaultOptions: { queries: { retry: false } } }),
	);
	const [client] = useState<DatasourceClient>(() => ({
		listSources: async () => {
			if (state === "loading") return new Promise(() => {});
			if (state === "error") throw new Error("Example provider unavailable");
			if (state === "degraded") return [degradedSource];
			return state === "empty" ? [] : [source];
		},
		listAccessibleScopes: async () => [
			{
				nodeId: "example-boundary",
				label: "Example documents",
				kind: "collection",
				actions: ["read"],
			},
		],
		addGitHubSource: async () => {
			throw new Error("Connection is unavailable in this preview.");
		},
		syncSource: async () => {
			throw new Error("Synchronization is unavailable in this preview.");
		},
		deleteSource: async () => {
			throw new Error("Deletion is unavailable in this preview.");
		},
	}));
	return (
		<QueryClientProvider client={queryClient}>
			<DatasourcesPanel client={client} orgId="example-org" />
		</QueryClientProvider>
	);
}
export const Populated = { render: () => <Preview state="populated" /> };
export const Empty = { render: () => <Preview state="empty" /> };
export const Loading = { render: () => <Preview state="loading" /> };
export const ProviderError = { render: () => <Preview state="error" /> };
export const Degraded = { render: () => <Preview state="degraded" /> };
export const ConnectionValidation = {
	render: function ConnectionValidationStory() {
		const [message, setMessage] = useState<string>();
		return (
			<ConnectGitHubForm
				isPending={false}
				errorMessage={message}
				onCancel={() => setMessage("Close this story to leave the preview.")}
				onSubmit={() =>
					setMessage("Connection is unavailable in this preview.")
				}
			/>
		);
	},
};
/**
 * Every sync state, side by side. A phase bar is the one thing in this panel a
 * reader judges at a glance, so the states have to be told apart at a glance —
 * which is only checkable with them on one canvas.
 */
const STORY_NOW = Date.parse("2026-09-28T12:00:00.000Z");
const storyAgo = (ms: number) => new Date(STORY_NOW - ms).toISOString();

function storySync(overrides: Partial<SourceSyncView>): SourceSyncView {
	return {
		jobId: "example-job",
		phase: "queued",
		trigger: "manual",
		queuedAt: storyAgo(2_000),
		attempt: 1,
		maxAttempts: 5,
		...overrides,
	};
}

const storyChanges = {
	files: 162,
	added: 12,
	modified: 150,
	deleted: 0,
	splitKnown: true,
	snapshot: false,
	commit: "0f1e2d3c4b5a6978",
};

const SYNC_STATES: Array<[string, SourceSyncView]> = [
	["Queued", storySync({})],
	["Fetching", storySync({ phase: "fetching", fetchingAt: storyAgo(3_000) })],
	[
		"Compiled, with counts",
		storySync({
			phase: "compiled",
			fetchingAt: storyAgo(20_000),
			compiledAt: storyAgo(2_000),
			changes: storyChanges,
		}),
	],
	[
		"Done",
		storySync({
			phase: "done",
			compiledAt: storyAgo(30_000),
			handedOffAt: storyAgo(10_000),
			finishedAt: storyAgo(10_000),
			changes: storyChanges,
		}),
	],
	["No changes", storySync({ phase: "done", finishedAt: storyAgo(10_000) })],
	[
		"No progress (stalled)",
		storySync({ phase: "fetching", fetchingAt: storyAgo(900_000) }),
	],
	[
		// `queued`, which is what the host actually stamps for a job the queue is
		// retrying — there is no `retrying` phase on the wire. Shown with the
		// stamps and the change set the first attempt left behind, because that is
		// the combination that used to send the bar backwards to a quarter.
		"Retrying",
		storySync({
			phase: "queued",
			fetchingAt: storyAgo(300_000),
			compiledAt: storyAgo(120_000),
			changes: storyChanges,
			attempt: 3,
			maxAttempts: 5,
			failure: {
				reason: "rate_limited",
				code: "datasource.rate_limited",
				message: "The provider rate-limited this sync.",
				retrying: true,
				retryAt: new Date(STORY_NOW + 420_000).toISOString(),
			},
		}),
	],
	[
		"Waiting for a worker, for too long",
		storySync({ phase: "queued", queuedAt: storyAgo(1_800_000) }),
	],
	[
		"Failed",
		storySync({
			phase: "failed",
			fetchingAt: storyAgo(60_000),
			finishedAt: storyAgo(10_000),
			attempt: 5,
			maxAttempts: 5,
			failure: {
				reason: "credential",
				code: "datasource.credential_invalid",
				message:
					"The stored credential was refused. Reconnect the source with a new token.",
				retrying: false,
			},
		}),
	],
];

export const SyncProgressStates = {
	render: () => (
		<div className="space-y-4">
			{SYNC_STATES.map(([label, view]) => (
				<div key={label} className="space-y-1">
					<p className="type-body text-muted-foreground">{label}</p>
					<SourceSyncProgress
						source={source}
						report={describeSync(view, { now: STORY_NOW })}
					/>
				</div>
			))}
		</div>
	),
};

/**
 * The same bar with an execution view to open — what a consumer that composes a
 * durable-work module sees. The host renders the frame; the panel below it is
 * the consumer's own.
 */
export const SyncProgressWithExecution = {
	render: () => (
		<SourceSyncProgress
			source={source}
			report={describeSync(
				storySync({ phase: "fetching", fetchingAt: storyAgo(3_000) }),
				{ now: STORY_NOW },
			)}
			onOpenExecution={() => {}}
		/>
	),
};

/**
 * A source that still syncs and still hands off, with nothing on the other end
 * to accept it. The host's hand-off jobs succeed, so the sync reports handed
 * off; the consuming module refuses each one at its own admission, which the
 * host cannot see. The line is the half the host CAN see.
 */
export const SyncProgressWithoutDelegation = {
	render: () => (
		<div className="space-y-4">
			<SourceSyncProgress
				source={source}
				delegation="none"
				report={describeSync(
					storySync({
						phase: "done",
						compiledAt: storyAgo(30_000),
						handedOffAt: storyAgo(10_000),
						finishedAt: storyAgo(10_000),
						changes: storyChanges,
					}),
					{ now: STORY_NOW },
				)}
			/>
			{/* The same source, read by someone who cannot act on it. */}
			<SourceSyncProgress
				source={source}
				delegation="none"
				canManage={false}
				report={describeSync(
					storySync({
						phase: "done",
						handedOffAt: storyAgo(10_000),
						finishedAt: storyAgo(10_000),
						changes: storyChanges,
					}),
					{ now: STORY_NOW },
				)}
			/>
		</div>
	),
};

export const ConnectionPending = {
	render: () => (
		<ConnectGitHubForm
			isPending
			errorMessage="Example pending validation; no request is running."
			onCancel={() => {}}
			onSubmit={() => {}}
		/>
	),
};

/**
 * What a viewer sees when the runs behind a sync are not theirs to read. The
 * point of the story is the wording: it has to be obviously different from
 * "this source has never synced", which is what the panel says a few lines
 * above when there is genuinely nothing.
 */
export const ExecutionRestricted = {
	render: () => <SourceExecutionRestricted />,
};

/**
 * A solution that declares the one repository it is built on: the repository
 * is shown, never asked for, and the three states are the whole of what a
 * person can be told about it.
 */
const declaredSource: DeclaredSource = {
	provider: "github",
	repo: "example-org/handbook",
	paths: ["proposals/"],
	ref: "main",
	label: "Proposals",
};
const declaredConnected: DatasourceView = {
	...source,
	id: "example-declared-source",
	repo: "example-org/handbook",
	paths: ["proposals/"],
	liveDelivery: "app_webhook",
	reconcileIntervalSeconds: 3600,
	lastIngestedAt: new Date(Date.now() - 42 * 60_000).toISOString(),
	lastIngestedCommit: "9f2c1ab0d4e5",
};
function DeclaredPreview({
	state,
}: {
	state: "setup" | "connected" | "error";
}) {
	const [queryClient] = useState(
		() => new QueryClient({ defaultOptions: { queries: { retry: false } } }),
	);
	const [client] = useState<DatasourceClient>(() => ({
		listSources: async () =>
			state === "setup"
				? []
				: [
						state === "error"
							? {
									...declaredConnected,
									status: "degraded" as const,
									statusReason:
										"the stored credential was rejected by GitHub (401) on the last 3 attempts",
								}
							: declaredConnected,
					],
		addGitHubSource: async () => {
			throw new Error("Connection is unavailable in this preview.");
		},
		syncSource: async () => {
			throw new Error("Synchronization is unavailable in this preview.");
		},
		deleteSource: async () => {
			throw new Error("Deletion is unavailable in this preview.");
		},
	}));
	return (
		<QueryClientProvider client={queryClient}>
			<DeclaredSourceCard
				client={client}
				orgId="example-org"
				declared={declaredSource}
			/>
		</QueryClientProvider>
	);
}
export const DeclaredSourceSetUp = {
	render: () => <DeclaredPreview state="setup" />,
};
export const DeclaredSourceConnected = {
	render: () => <DeclaredPreview state="connected" />,
};
export const DeclaredSourceError = {
	render: () => <DeclaredPreview state="error" />,
};
