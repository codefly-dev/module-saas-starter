import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DatasourcesPanel } from "../datasources-panel.js";
import { SourceSyncProgress } from "../sync-progress.js";
import { describeSync } from "../sync-progress-model.js";
import { notifySourceSyncRequested } from "../sync-requests.js";
import type {
	DatasourceClient,
	DatasourceView,
	SourceSyncView,
} from "../types.js";

afterEach(cleanup);

const NOW = Date.parse("2026-09-28T12:00:00.000Z");
const ago = (ms: number) => new Date(NOW - ms).toISOString();

const source: DatasourceView = {
	id: "ds-1",
	orgId: "org-1",
	provider: "github",
	repo: "example-org/example-repo",
	paths: [],
	branch: "main",
	boundaryNodeId: "11111111-1111-1111-1111-111111111111",
	webhookConfigured: true,
	status: "active",
	lastSyncedAt: undefined,
	createdAt: undefined,
};

function sync(overrides: Partial<SourceSyncView> = {}): SourceSyncView {
	return {
		jobId: "22222222-2222-2222-2222-222222222222",
		phase: "queued",
		trigger: "manual",
		queuedAt: ago(1_000),
		attempt: 1,
		maxAttempts: 5,
		...overrides,
	};
}

function renderWithClient(ui: ReactElement) {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>,
	);
}

function fakeClient(
	overrides: Partial<DatasourceClient> = {},
): DatasourceClient {
	return {
		listSources: vi.fn(async () => [source]),
		addGitHubSource: vi.fn(async () => {}),
		syncSource: vi.fn(async () => "job-1"),
		deleteSource: vi.fn(async () => {}),
		...overrides,
	};
}

/** The bar, as the accessibility tree sees it. */
function bar() {
	return screen.getByRole("progressbar");
}

describe("SourceSyncProgress", () => {
	it("renders a queued sync as a named, part-filled bar", () => {
		render(
			<SourceSyncProgress
				source={source}
				report={describeSync(sync(), { now: NOW })}
			/>,
		);

		expect(bar().getAttribute("aria-valuenow")).toBe("25");
		expect(bar().getAttribute("aria-label")).toBe(
			"Sync progress for example-org/example-repo",
		);
		// The words, not the number, are what assistive tech reads out.
		expect(bar().getAttribute("aria-valuetext")).toBe(
			"Accepted, waiting for a worker",
		);
		expect(screen.getByText("Step 1 of 4")).toBeTruthy();
		// The badge names the state, the headline says what the state does not.
		expect(screen.getByText("Queued")).toBeTruthy();
		expect(screen.getByText("Accepted, waiting for a worker")).toBeTruthy();
	});

	it("renders the counts of a compiled change set", () => {
		render(
			<SourceSyncProgress
				source={source}
				report={describeSync(
					sync({
						phase: "compiled",
						compiledAt: ago(1_000),
						changes: {
							files: 162,
							added: 12,
							modified: 150,
							deleted: 0,
							splitKnown: true,
							snapshot: false,
							commit: "abcdef1234567",
						},
					}),
					{ now: NOW },
				)}
			/>,
		);

		expect(screen.getByText("Files:")).toBeTruthy();
		expect(screen.getByText("162")).toBeTruthy();
		expect(screen.getByText("150")).toBeTruthy();
		// The commit is abbreviated, as everywhere else a commit appears here.
		expect(screen.getByText(/abcdef1$/)).toBeTruthy();
	});

	it("announces a failure assertively and shows the host's reason", () => {
		render(
			<SourceSyncProgress
				source={source}
				report={describeSync(
					sync({
						phase: "failed",
						finishedAt: ago(1_000),
						failure: {
							reason: "credential",
							code: "datasource.credential_invalid",
							message: "The stored credential was refused.",
							retrying: false,
						},
					}),
					{ now: NOW },
				)}
			/>,
		);

		const alert = screen.getByRole("alert");
		expect(alert.getAttribute("aria-live")).toBe("assertive");
		expect(screen.getByText("Sync failed")).toBeTruthy();
		expect(screen.getByText("The stored credential was refused.")).toBeTruthy();
		expect(screen.getByText("Failed")).toBeTruthy();
	});

	it("announces a running sync politely, so each phase does not interrupt the reader", () => {
		render(
			<SourceSyncProgress
				source={source}
				report={describeSync(
					sync({ phase: "fetching", fetchingAt: ago(1_000) }),
					{ now: NOW },
				)}
			/>,
		);

		const status = screen.getByRole("status");
		expect(status.getAttribute("aria-live")).toBe("polite");
		expect(screen.queryByRole("alert")).toBeNull();
	});

	it("labels a stalled sync as having made no progress, not as failed", () => {
		render(
			<SourceSyncProgress
				source={source}
				report={describeSync(
					sync({ phase: "fetching", fetchingAt: ago(600_000) }),
					{ now: NOW },
				)}
			/>,
		);

		expect(screen.getByText("No progress")).toBeTruthy();
		expect(screen.getByText(/No progress for 10 minutes/)).toBeTruthy();
		expect(screen.queryByText("Sync failed")).toBeNull();
	});

	it("labels a sync that handed nothing off as having no changes", () => {
		render(
			<SourceSyncProgress
				source={source}
				report={describeSync(sync({ phase: "done", finishedAt: ago(1_000) }), {
					now: NOW,
				})}
			/>,
		);

		expect(screen.getByText("No changes")).toBeTruthy();
		expect(screen.getByText("Up to date")).toBeTruthy();
	});

	it("offers the execution action only when one was handed in", () => {
		const report = describeSync(sync(), { now: NOW });
		const { unmount } = render(
			<SourceSyncProgress source={source} report={report} />,
		);
		expect(screen.queryByRole("button", { name: "Execution" })).toBeNull();
		unmount();

		const onOpenExecution = vi.fn();
		render(
			<SourceSyncProgress
				source={source}
				report={report}
				onOpenExecution={onOpenExecution}
			/>,
		);
		fireEvent.click(screen.getByRole("button", { name: "Execution" }));
		expect(onOpenExecution).toHaveBeenCalledOnce();
	});
});

describe("the panel's live sync progress", () => {
	it("shows a bar for a source whose sync is in flight", async () => {
		const client = fakeClient({
			getSourceSync: vi.fn(async () =>
				sync({ phase: "fetching", fetchingAt: new Date().toISOString() }),
			),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		expect(await screen.findByRole("progressbar")).toBeTruthy();
		expect(screen.getByText("Fetching from the repository")).toBeTruthy();
		expect(client.getSourceSync).toHaveBeenCalledWith("org-1", "ds-1");
	});

	it("shows nothing when the source has never synced", async () => {
		const client = fakeClient({ getSourceSync: vi.fn(async () => undefined) });
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		await screen.findByText("example-org/example-repo");
		await waitFor(() => expect(client.getSourceSync).toHaveBeenCalled());
		expect(screen.queryByRole("progressbar")).toBeNull();
	});

	it("shows nothing for a client that cannot read a sync at all", async () => {
		// An older consumer's adapter implements no `getSourceSync`. It keeps
		// working, without a bar, rather than rendering an empty card.
		const client = fakeClient();
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		await screen.findByText("example-org/example-repo");
		expect(screen.queryByRole("progressbar")).toBeNull();
	});

	it("does not keep a long-finished sync above the table", async () => {
		// History is where an old sync lives. Without this the panel accumulates a
		// card per source for as long as the page stays open.
		const client = fakeClient({
			getSourceSync: vi.fn(async () =>
				sync({
					phase: "done",
					finishedAt: new Date(Date.now() - 3_600_000).toISOString(),
					changes: {
						files: 1,
						added: 1,
						modified: 0,
						deleted: 0,
						splitKnown: true,
						snapshot: false,
						commit: "abcdef1",
					},
				}),
			),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		await screen.findByText("example-org/example-repo");
		await waitFor(() => expect(client.getSourceSync).toHaveBeenCalled());
		expect(screen.queryByRole("progressbar")).toBeNull();
	});

	it("keeps a just-finished sync visible", async () => {
		const client = fakeClient({
			getSourceSync: vi.fn(async () =>
				sync({
					phase: "done",
					finishedAt: new Date().toISOString(),
					changes: {
						files: 4,
						added: 4,
						modified: 0,
						deleted: 0,
						splitKnown: true,
						snapshot: false,
						commit: "abcdef1",
					},
				}),
			),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		expect(await screen.findByRole("progressbar")).toBeTruthy();
		expect(screen.getByText("Handed off the changed files")).toBeTruthy();
	});

	it("re-reads the sync as soon as one is announced, without waiting out the poll", async () => {
		// The client announces every sync it enqueues. Nothing subscribed before
		// this, which is why a tenant who pressed Sync saw no phase until the next
		// slow interval — the complaint in the issue.
		let phase: SourceSyncView["phase"] = "done";
		const client = fakeClient({
			getSourceSync: vi.fn(async () =>
				phase === "done"
					? sync({ phase: "done", finishedAt: new Date().toISOString() })
					: sync({ phase: "queued", queuedAt: new Date().toISOString() }),
			),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);
		await screen.findByText("Up to date");

		phase = "queued";
		notifySourceSyncRequested("ds-1");

		expect(
			await screen.findByText("Accepted, waiting for a worker"),
		).toBeTruthy();
	});

	it("reveals a stall on the clock, with no new data to re-render it", async () => {
		// The bug this guards: a sync that stops advancing answers every poll
		// identically, so nothing about the data changes. Judged against a clock
		// read during render, the view would never re-render and the state that
		// exists to make silence visible would never appear.
		vi.useFakeTimers({ shouldAdvanceTime: true });
		try {
			const fetchingAt = new Date().toISOString();
			const client = fakeClient({
				getSourceSync: vi.fn(async () =>
					sync({ phase: "fetching", fetchingAt }),
				),
			});
			renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);
			expect(
				await screen.findByText("Fetching from the repository"),
			).toBeTruthy();
			expect(screen.queryByText("No progress")).toBeNull();

			await act(async () => {
				await vi.advanceTimersByTimeAsync(180_000);
			});

			expect(screen.getByText("No progress")).toBeTruthy();
		} finally {
			vi.useRealTimers();
		}
	});

	it("ignores an announcement for another source", async () => {
		const client = fakeClient({
			getSourceSync: vi.fn(async () =>
				sync({ phase: "done", finishedAt: new Date().toISOString() }),
			),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);
		await screen.findByText("Up to date");
		const before = (client.getSourceSync as ReturnType<typeof vi.fn>).mock.calls
			.length;

		notifySourceSyncRequested("ds-other");

		await waitFor(() =>
			expect(
				(client.getSourceSync as ReturnType<typeof vi.fn>).mock.calls.length,
			).toBe(before),
		);
	});

	it("renders no bar when the sync read fails, leaving the row's own status to speak", async () => {
		// A transport error is not a failed sync. Painting one red would report a
		// healthy source as broken every time the network blipped.
		const client = fakeClient({
			getSourceSync: vi.fn(async () => {
				throw new Error("transport down");
			}),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		await screen.findByText("example-org/example-repo");
		await waitFor(() => expect(client.getSourceSync).toHaveBeenCalled());
		expect(screen.queryByRole("progressbar")).toBeNull();
		expect(screen.queryByText("Sync failed")).toBeNull();
	});
});

describe("the panel's execution extension point", () => {
	it("offers no execution action when no view was handed in", async () => {
		const client = fakeClient({ listActivity: vi.fn(async () => []) });
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		await screen.findByText("example-org/example-repo");
		expect(screen.queryByText("Execution")).toBeNull();
	});

	it("opens the consumer's view from the row, with the host's sync resolved", async () => {
		const client = fakeClient({
			getSourceSync: vi.fn(async () =>
				sync({ phase: "done", finishedAt: new Date().toISOString() }),
			),
		});
		const renderSourceExecution = vi.fn(
			({
				source: s,
				sync: view,
			}: {
				source: DatasourceView;
				sync?: SourceSyncView;
			}) => (
				<span>
					execution of {s.id} job {view?.jobId ?? "none"}
				</span>
			),
		);
		renderWithClient(
			<DatasourcesPanel
				client={client}
				orgId="org-1"
				renderSourceExecution={renderSourceExecution}
			/>,
		);

		fireEvent.click(
			await screen.findByRole("button", {
				name: "More actions for example-org/example-repo",
			}),
		);
		fireEvent.click(await screen.findByRole("menuitem", { name: "Execution" }));

		expect(
			await screen.findByText(
				"execution of ds-1 job 22222222-2222-2222-2222-222222222222",
			),
		).toBeTruthy();
		// The host owns the frame so a consumer's view arrives looking like the
		// rest of the panel.
		expect(
			screen.getByRole("region", {
				name: "Execution of example-org/example-repo",
			}),
		).toBeTruthy();
	});

	it("offers the execution to a viewer who manages nothing", async () => {
		// The runtime read for a member is its own scope; a reader who may not
		// sync a source may still see what a sync did.
		const client = fakeClient();
		renderWithClient(
			<DatasourcesPanel
				client={client}
				orgId="org-1"
				canManage={false}
				renderSourceExecution={() => <span>member execution</span>}
			/>,
		);

		fireEvent.click(
			await screen.findByRole("button", {
				name: "Execution of example-org/example-repo",
			}),
		);
		expect(await screen.findByText("member execution")).toBeTruthy();
	});

	it("closes the execution view", async () => {
		const client = fakeClient();
		renderWithClient(
			<DatasourcesPanel
				client={client}
				orgId="org-1"
				canManage={false}
				renderSourceExecution={() => <span>member execution</span>}
			/>,
		);
		fireEvent.click(
			await screen.findByRole("button", {
				name: "Execution of example-org/example-repo",
			}),
		);
		fireEvent.click(
			await screen.findByRole("button", { name: "Close execution" }),
		);

		expect(screen.queryByText("member execution")).toBeNull();
	});
});
