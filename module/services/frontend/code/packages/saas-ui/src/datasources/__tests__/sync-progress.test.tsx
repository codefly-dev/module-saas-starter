import {
	focusManager,
	QueryClient,
	QueryClientProvider,
} from "@tanstack/react-query";
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
import { SourceExecutionRestricted } from "../source-execution-access.js";
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
						fetchingAt: ago(60_000),
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
		// The bar is deliberately full for a failed sync, so the step has to say
		// the sync STOPPED there. "Step 2 of 4" under a full bar reads as a
		// rendering fault rather than as a sync that got two phases in.
		expect(screen.getByText("Stopped at step 2 of 4")).toBeTruthy();
		expect(bar().getAttribute("aria-valuenow")).toBe("100");
	});

	it("says plain Step for a sync that is still going", () => {
		render(
			<SourceSyncProgress
				source={source}
				report={describeSync(
					sync({ phase: "fetching", fetchingAt: ago(1_000) }),
					{ now: NOW },
				)}
			/>,
		);

		expect(screen.getByText("Step 2 of 4")).toBeTruthy();
		expect(screen.queryByText(/Stopped at/)).toBeNull();
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

	it("follows a sync to its end while the page is not focused", async () => {
		// The bug this guards: the card's poll ran only while the document had
		// focus, so a sync watched from a page without focus froze on its
		// in-flight phase — "Queued · Step 1 of 4" beside a row that already said
		// it had finished — until a reload re-read it.
		vi.useFakeTimers({ shouldAdvanceTime: true });
		focusManager.setFocused(false);
		try {
			let phase: SourceSyncView["phase"] = "queued";
			const client = fakeClient({
				getSourceSync: vi.fn(async () =>
					phase === "queued"
						? sync({ phase: "queued", queuedAt: new Date().toISOString() })
						: sync({ phase: "done", finishedAt: new Date().toISOString() }),
				),
			});
			renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);
			expect(
				await screen.findByText("Accepted, waiting for a worker"),
			).toBeTruthy();

			phase = "done";
			await act(async () => {
				await vi.advanceTimersByTimeAsync(5_000);
			});

			expect(screen.getByText("Up to date")).toBeTruthy();
		} finally {
			focusManager.setFocused(undefined);
			vi.useRealTimers();
		}
	});

	it("does not poll a settled sync while the page is not focused, and re-reads it on focus", async () => {
		// Background polling exists only to keep an in-flight sync moving. A
		// settled card in an idle tab must stay silent, or every source's card
		// polls on its settled cadence for as long as the tab stays open.
		vi.useFakeTimers({ shouldAdvanceTime: true });
		try {
			const getSourceSync = vi.fn(async () =>
				sync({ phase: "done", finishedAt: new Date().toISOString() }),
			);
			renderWithClient(
				<DatasourcesPanel
					client={fakeClient({ getSourceSync })}
					orgId="org-1"
				/>,
			);
			await waitFor(() => expect(getSourceSync).toHaveBeenCalled());
			await act(async () => {
				await vi.advanceTimersByTimeAsync(1_000);
			});

			focusManager.setFocused(false);
			// Let one already-scheduled settled tick land; after it, nothing more.
			await act(async () => {
				await vi.advanceTimersByTimeAsync(31_000);
			});
			const settledReads = getSourceSync.mock.calls.length;
			await act(async () => {
				await vi.advanceTimersByTimeAsync(180_000);
			});
			expect(getSourceSync.mock.calls.length).toBe(settledReads);

			await act(async () => {
				focusManager.setFocused(true);
			});
			await waitFor(() =>
				expect(getSourceSync.mock.calls.length).toBeGreaterThan(settledReads),
			);
		} finally {
			focusManager.setFocused(undefined);
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

describe("the panel does not flash a loading indicator", () => {
	it("never shows the list's loading line for a wait shorter than the delay", async () => {
		// The product rule: an indicator appears only if the wait passes the
		// delay. A warm cache is the common case, and it must render the table
		// with no intervening state at all.
		//
		// Asserting only after the table arrives would prove nothing — the line is
		// gone by then whether or not it ever appeared. So hold the answer, check
		// inside the window, and release.
		//
		// An exact clock, deliberately: `shouldAdvanceTime` would add the real
		// milliseconds a loaded machine spends between render and the advance,
		// and an assertion that 150ms has not reached the 200ms delay then fires
		// the timer instead — intermittently, only under load. This test polls
		// nothing, so it has no use for real time.
		vi.useFakeTimers();
		try {
			let release: (value: DatasourceView[]) => void = () => {};
			const client = fakeClient({
				listSources: vi.fn(
					() =>
						new Promise<DatasourceView[]>((resolve) => {
							release = resolve;
						}),
				),
			});
			renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

			await act(async () => {
				await vi.advanceTimersByTimeAsync(150);
			});
			expect(screen.queryByText("Loading data sources…")).toBeNull();

			await act(async () => {
				release([source]);
			});
			await act(async () => {
				await vi.advanceTimersByTimeAsync(500);
			});
			// It resolved inside the window, so the line must never have appeared.
			expect(screen.queryByText("Loading data sources…")).toBeNull();
			expect(screen.getByText("example-org/example-repo")).toBeTruthy();
		} finally {
			vi.useRealTimers();
		}
	});

	it("shows the list's loading line once the wait passes the delay", async () => {
		// The other half: a slow list must still say something, or the panel is
		// simply blank while it waits. Exact clock, same reason as above.
		vi.useFakeTimers();
		try {
			let release: (value: DatasourceView[]) => void = () => {};
			const client = fakeClient({
				listSources: vi.fn(
					() =>
						new Promise<DatasourceView[]>((resolve) => {
							release = resolve;
						}),
				),
			});
			renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

			expect(screen.queryByText("Loading data sources…")).toBeNull();
			await act(async () => {
				await vi.advanceTimersByTimeAsync(250);
			});
			expect(screen.getByText("Loading data sources…")).toBeTruthy();

			await act(async () => {
				release([source]);
			});
		} finally {
			vi.useRealTimers();
		}
	});

	it("keeps a line it has shown up long enough to read", async () => {
		// The half the delay alone does not give you. This panel used to mount
		// `<DelayedLoading active>` inside a branch gated on `list.isLoading`, so
		// the answer landing just past the delay unmounted the line with its own
		// floor still in state — the indicator appeared for a few milliseconds,
		// which is the blink the floor exists to prevent, moved later rather than
		// removed. Exact clock, same reason as above.
		vi.useFakeTimers();
		try {
			let release: (value: DatasourceView[]) => void = () => {};
			const client = fakeClient({
				listSources: vi.fn(
					() =>
						new Promise<DatasourceView[]>((resolve) => {
							release = resolve;
						}),
				),
			});
			renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

			await act(async () => {
				await vi.advanceTimersByTimeAsync(200);
			});
			expect(screen.getByText("Loading data sources…")).toBeTruthy();

			// The answer arrives 10ms after the line went up.
			await act(async () => {
				await vi.advanceTimersByTimeAsync(10);
			});
			await act(async () => {
				release([source]);
			});
			expect(screen.getByText("Loading data sources…")).toBeTruthy();

			// 290ms of the 300ms floor remain from when the line appeared.
			await act(async () => {
				await vi.advanceTimersByTimeAsync(289);
			});
			expect(screen.getByText("Loading data sources…")).toBeTruthy();
			await act(async () => {
				await vi.advanceTimersByTimeAsync(1);
			});
			expect(screen.queryByText("Loading data sources…")).toBeNull();
			expect(screen.getByText("example-org/example-repo")).toBeTruthy();
		} finally {
			vi.useRealTimers();
		}
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

	it("never leaves an empty card that reads as 'never synced'", async () => {
		// The trap this guards: the runs behind a sync belong to the module that
		// started them, so a viewer whose permission covers only their OWN runs
		// gets a successful EMPTY answer — and an empty card sits directly under a
		// panel whose empty state says the source has never synced. Two correct
		// components, one false statement. The host cannot tell "none" from "not
		// yours", so it must not let silence pick one.
		const client = fakeClient();
		renderWithClient(
			<DatasourcesPanel
				client={client}
				orgId="org-1"
				canManage={false}
				renderSourceExecution={() => null}
			/>,
		);
		fireEvent.click(
			await screen.findByRole("button", {
				name: "Execution of example-org/example-repo",
			}),
		);

		expect(
			await screen.findByText(/you may not have permission to see them/i),
		).toBeTruthy();
	});

	it("treats every empty render the same way", async () => {
		// `null`, `undefined`, `false` and `[]` all mean "rendered nothing" from a
		// render prop; a consumer returning any of them meant the same thing.
		for (const empty of [undefined, false, []] as const) {
			const client = fakeClient();
			const view = renderWithClient(
				<DatasourcesPanel
					client={client}
					orgId="org-1"
					canManage={false}
					renderSourceExecution={() => empty}
				/>,
			);
			fireEvent.click(
				await screen.findByRole("button", {
					name: "Execution of example-org/example-repo",
				}),
			);
			expect(
				await screen.findByText(/you may not have permission to see them/i),
			).toBeTruthy();
			view.unmount();
		}
	});

	it("says so explicitly when the consumer reports the viewer may not read the runs", async () => {
		// A consumer that CAN tell "not yours" from "none" says which, and the kit
		// carries the sentence so it reads the same wherever it appears.
		const client = fakeClient();
		renderWithClient(
			<DatasourcesPanel
				client={client}
				orgId="org-1"
				canManage={false}
				renderSourceExecution={() => <SourceExecutionRestricted />}
			/>,
		);
		fireEvent.click(
			await screen.findByRole("button", {
				name: "Execution of example-org/example-repo",
			}),
		);

		expect(
			await screen.findByText(
				"Sync runs are visible to organization administrators.",
			),
		).toBeTruthy();
		// And it must not also claim there is nothing to show.
		expect(screen.queryByText(/you may not have permission/i)).toBeNull();
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

describe("the panel's polling cadence", () => {
	// The configured interval each list query is polled at, as TanStack holds it
	// on the query's observer — what the browser will actually be asked to do.
	function cadence(queryClient: QueryClient, queryKey: readonly unknown[]) {
		const query = queryClient.getQueryCache().find({ queryKey, exact: true });
		const observer = query?.observers[0];
		const interval = observer?.options.refetchInterval;
		return {
			interval:
				typeof interval === "function" && query ? interval(query) : interval,
			inBackground: observer?.options.refetchIntervalInBackground ?? false,
		};
	}

	function renderPanel(client: DatasourceClient) {
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		render(
			<QueryClientProvider client={queryClient}>
				<DatasourcesPanel client={client} orgId="org-1" />
			</QueryClientProvider>,
		);
		return queryClient;
	}

	// Observed in a consumer: ~100 ListSources / ListMyAccessibleScopes /
	// ListCollections calls in a few idle minutes, from fixed 5 s polls that also
	// kept running in background tabs.
	it("polls slowly and never in a background tab when nothing is syncing", async () => {
		const client = fakeClient({
			getSourceSync: vi.fn(async () =>
				sync({ phase: "done", finishedAt: ago(3_600_000) }),
			),
			listAccessibleScopes: vi.fn(async () => []),
			listCollections: vi.fn(async () => []),
		});
		const queryClient = renderPanel(client);
		await screen.findByText("example-org/example-repo");
		await waitFor(() => expect(client.getSourceSync).toHaveBeenCalled());

		for (const key of [
			["datasources", "org-1"],
			["datasource-boundaries", "org-1"],
			["collection-access", "org-1"],
		]) {
			expect(cadence(queryClient, key), String(key)).toEqual({
				interval: 60_000,
				inBackground: false,
			});
		}
	});

	it("polls the source list fast while a sync runs, and re-reads it once it settles", async () => {
		let current = sync({
			phase: "fetching",
			fetchingAt: new Date().toISOString(),
		});
		const client = fakeClient({ getSourceSync: vi.fn(async () => current) });
		const queryClient = renderPanel(client);
		expect(await screen.findByRole("progressbar")).toBeTruthy();
		await waitFor(() =>
			expect(cadence(queryClient, ["datasources", "org-1"]).interval).toBe(
				5_000,
			),
		);

		const listedBefore = (client.listSources as ReturnType<typeof vi.fn>).mock
			.calls.length;
		current = sync({ phase: "done", finishedAt: new Date().toISOString() });
		await act(async () => {
			await queryClient.invalidateQueries({
				queryKey: ["source-sync", "org-1"],
			});
		});
		await waitFor(() =>
			expect(cadence(queryClient, ["datasources", "org-1"]).interval).toBe(
				60_000,
			),
		);
		// The last-sync column must not wait out the idle poll.
		await waitFor(() =>
			expect(
				(client.listSources as ReturnType<typeof vi.fn>).mock.calls.length,
			).toBeGreaterThan(listedBefore),
		);
	});
});
