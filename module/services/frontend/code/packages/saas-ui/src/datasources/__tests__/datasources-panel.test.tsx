import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { type ReactElement, StrictMode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ConnectGitHubForm } from "../connect-github-form.js";
import { DatasourcesPanel } from "../datasources-panel.js";
import type {
	DatasourceClient,
	DatasourceView,
	SourceReconcileReceipt,
} from "../types.js";

import {
	exampleProvider,
	repositoryProvider,
} from "../../../stories/catalog-fixtures.js";

afterEach(cleanup);

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
		getCatalog: vi.fn(async () => [repositoryProvider]),
		addSource: vi.fn(async () => {}),
		listSources: vi.fn(async () => [] as DatasourceView[]),
		addGitHubSource: vi.fn(async () => {}),
		reconcileSource: vi.fn(async (_orgId, _id, effectId: string) => ({
			effectId,
			status: "committed" as const,
			jobId: "job-1",
		})),
		lookupReconcileSource: vi.fn(async () => undefined),
		reconnectSource: vi.fn(async () => "job-reconnect"),
		deleteSource: vi.fn(async () => {}),
		...overrides,
	};
}

const sampleSource: DatasourceView = {
	id: "ds-1",
	orgId: "org-1",
	provider: "github",
	repo: "codefly-dev/module-saas-starter",
	paths: ["docs/"],
	branch: "main",
	boundaryNodeId: "11111111-1111-1111-1111-111111111111",
	webhookConfigured: true,
	status: "active",
	lastSyncedAt: undefined,
	// Deliberately omits lastIngestedAt/lastIngestedCommit: they are optional, so
	// a consumer's own adapter keeps compiling without them.
	createdAt: undefined,
};

const secondSource: DatasourceView = {
	...sampleSource,
	id: "ds-2",
	repo: "codefly-dev/other-repo",
};

async function openConnectForm(client: DatasourceClient) {
	renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);
	fireEvent.click(await screen.findByRole("radio", { name: "GitHub" }));
	fireEvent.change(screen.getByLabelText("Repository"), {
		target: { value: "codefly-dev/module-saas-starter" },
	});
	fireEvent.change(screen.getByLabelText("Collection"), {
		target: { value: "docs" },
	});
	fireEvent.change(screen.getByLabelText("Access token"), {
		target: { value: "ghp_token" },
	});
}

describe("DatasourcesPanel", () => {
	it("renders the sources the client returns", async () => {
		const client = fakeClient({
			listSources: vi.fn(async () => [sampleSource]),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		expect(
			await screen.findByText("codefly-dev/module-saas-starter"),
		).toBeTruthy();
		expect(client.listSources).toHaveBeenCalledWith("org-1");
	});

	it("renders a consumer's detail under each source it was handed", async () => {
		// The host does not know what an ingesting module has made of a source's
		// files; the consumer composing both renders that module's view here.
		const client = fakeClient({
			listSources: vi.fn(async () => [sampleSource, secondSource]),
		});
		const renderSourceDetail = vi.fn((source: DatasourceView) => (
			<span>ingestion of {source.id}</span>
		));
		renderWithClient(
			<DatasourcesPanel
				client={client}
				orgId="org-1"
				renderSourceDetail={renderSourceDetail}
			/>,
		);

		expect(await screen.findByText("ingestion of ds-1")).toBeTruthy();
		expect(screen.getByText("ingestion of ds-2")).toBeTruthy();
		expect(renderSourceDetail).toHaveBeenCalledWith(sampleSource);
	});

	it("surfaces a degraded source and its reason without being asked", async () => {
		// The host sets this state, never the tenant, so nothing prompts a reader
		// to open History looking for it.
		const degraded: DatasourceView = {
			...sampleSource,
			status: "degraded",
			statusReason:
				"snapshot manifest is 12582912 bytes, over the 8388608-byte ingest limit",
		};
		const client = fakeClient({ listSources: vi.fn(async () => [degraded]) });
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		expect(await screen.findByText("Needs attention")).toBeTruthy();
		expect(screen.getByText(/over the 8388608-byte ingest limit/)).toBeTruthy();
	});

	it("names the action that restarts a degraded source", async () => {
		// Degrading clears the schedule and only a full snapshot lifts it, so an
		// ordinary delivery never will: copy that says syncs stopped without
		// naming Sync now leaves a tenant who fixed the cause waiting on a pull
		// that cannot come. It must also not imply content was withdrawn.
		const degraded: DatasourceView = {
			...sampleSource,
			status: "degraded",
			statusReason: "snapshot manifest is over the ingest limit",
		};
		const client = fakeClient({ listSources: vi.fn(async () => [degraded]) });
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		const explanation = await screen.findByText(/syncs have stopped/i);
		expect(explanation.textContent).toMatch(/Sync now/);
		expect(explanation.textContent).toMatch(/stays readable/i);
	});

	it("says a source of a closed provider keeps syncing, without the host's notes", async () => {
		// The host keeps such a source running but takes no new source of its
		// provider. Why is the host's business: its gap is a developer's note.
		const flagged: DatasourceView = {
			...sampleSource,
			conformant: false,
			conformanceGap: "no cursor and no deletions",
		};
		const client = fakeClient({ listSources: vi.fn(async () => [flagged]) });
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		const note = await screen.findByText(/can no longer be added/i);
		expect(note.textContent).toMatch(/keeps syncing/);
		expect(screen.queryByText(/no cursor and no deletions/)).toBeNull();
	});

	it("does not flag a conformant source, or one whose client cannot tell", async () => {
		const conformant: DatasourceView = { ...sampleSource, conformant: true };
		const client = fakeClient({
			listSources: vi.fn(async () => [conformant, secondSource]),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		expect(await screen.findByText("codefly-dev/other-repo")).toBeTruthy();
		expect(screen.queryByText(/can no longer be added/i)).toBeNull();
	});

	it("renders the reason for a status other than degraded", async () => {
		// status_reason is scoped to "why the source left active", not to one way
		// of leaving it, so gating the render on `degraded` drops a paused
		// source's explanation.
		const paused: DatasourceView = {
			...sampleSource,
			status: "paused",
			statusReason: "paused by an organization administrator",
		};
		const client = fakeClient({ listSources: vi.fn(async () => [paused]) });
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		expect(await screen.findByText("Paused")).toBeTruthy();
		expect(
			screen.getByText("paused by an organization administrator"),
		).toBeTruthy();
	});

	it("renders a degraded source the host published no reason for", async () => {
		// status_reason is a plain string on the wire, so an empty one maps to no
		// reason at all; the state itself still has to reach the reader.
		const degraded: DatasourceView = { ...sampleSource, status: "degraded" };
		const client = fakeClient({ listSources: vi.fn(async () => [degraded]) });
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		expect(await screen.findByText("Needs attention")).toBeTruthy();
		expect(screen.getByText(/syncs have stopped/i)).toBeTruthy();
	});

	// Active is healthy; paused waits on someone; degraded is a fault; unknown
	// is neither.
	it.each([
		["active", "Active", "success"],
		["paused", "Paused", "warning"],
		["degraded", "Needs attention", "danger"],
		["unknown", "Status unknown", "neutral"],
	] as const)(
		"paints a %s source in its status tone",
		async (status, label, tone) => {
			const source: DatasourceView = { ...sampleSource, status };
			const client = fakeClient({ listSources: vi.fn(async () => [source]) });
			renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

			const badge = (await screen.findByText(label)).closest(
				"[data-slot=badge]",
			);
			expect(badge?.getAttribute("data-tone")).toBe(tone);
			expect(badge?.querySelector("[data-slot=badge-dot]")).toBeTruthy();
		},
	);

	it("says when the source last synced, never which commit", async () => {
		// A commit is a repository's internal handle; the reader wants a time.
		const ingested: DatasourceView = {
			...sampleSource,
			lastIngestedAt: "2026-09-08T11:30:00.000Z",
			lastIngestedCommit: "9f2c1ab7d4e5f60718293a4b5c6d7e8f90a1b2c3",
		};
		const client = fakeClient({ listSources: vi.fn(async () => [ingested]) });
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		const line = await screen.findByText(/^Synced/);
		expect(line.textContent).not.toMatch(/webhook/i);
		expect(line.textContent).not.toContain("9f2c1ab");
	});

	it("renders the sync time of day, not just its date", async () => {
		const at = "2026-09-08T11:30:00.000Z";
		const ingested: DatasourceView = { ...sampleSource, lastIngestedAt: at };
		const client = fakeClient({ listSources: vi.fn(async () => [ingested]) });
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		const line = await screen.findByText(/^Synced/);
		// The clock moves on every delivery, so a date cannot separate a source
		// that synced minutes ago from one that stopped shortly after midnight.
		expect(line.textContent).toContain(
			new Intl.DateTimeFormat(undefined, { timeStyle: "short" }).format(
				new Date(at),
			),
		);
	});

	it("distinguishes two syncs a minute apart", async () => {
		const client = fakeClient({
			listSources: vi.fn(async () => [
				{ ...sampleSource, lastIngestedAt: "2026-09-08T11:30:00.000Z" },
				{ ...secondSource, lastIngestedAt: "2026-09-08T11:31:00.000Z" },
			]),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		const lines = await screen.findAllByText(/^Synced/);
		expect(lines).toHaveLength(2);
		expect(lines[0].textContent).not.toBe(lines[1].textContent);
	});

	it('drops "Not synced yet" when there is an ingest to show', async () => {
		// A github source never sets last_synced_at — the compiler advances the
		// ingest clock instead — so the row reads that clock.
		const ingested: DatasourceView = {
			...sampleSource,
			lastSyncedAt: undefined,
			lastIngestedAt: "2026-09-08T11:30:00.000Z",
		};
		const client = fakeClient({ listSources: vi.fn(async () => [ingested]) });
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		await screen.findByText(/^Synced/);
		expect(screen.queryByText(/Not synced yet/)).toBeNull();
	});

	it("reads the sync clock for a provider that pulls", async () => {
		const pulled: DatasourceView = {
			...sampleSource,
			lastSyncedAt: "2026-09-08T11:30:00.000Z",
		};
		const client = fakeClient({ listSources: vi.fn(async () => [pulled]) });
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		expect(await screen.findByText(/^Synced/)).toBeTruthy();
	});

	it("says a source whose first delivery has not landed is not synced yet", async () => {
		const client = fakeClient({
			listSources: vi.fn(async () => [sampleSource]),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		await screen.findByText("codefly-dev/module-saas-starter");
		expect(screen.queryByText(/^Synced/)).toBeNull();
		expect(screen.getByText(/Not synced yet/)).toBeTruthy();
	});

	it("submits a provider's own form through addSource", async () => {
		const client = fakeClient({
			getCatalog: vi.fn(async () => [repositoryProvider, exampleProvider]),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		fireEvent.click(
			await screen.findByRole("radio", { name: "Example archive" }),
		);
		fireEvent.change(screen.getByLabelText("Key prefix"), {
			target: { value: "docs/" },
		});
		fireEvent.change(screen.getByLabelText("Collection"), {
			target: { value: "docs" },
		});
		fireEvent.change(screen.getByLabelText("Secret key"), {
			target: { value: "s3cret" },
		});
		fireEvent.click(
			screen.getByRole("button", { name: "Connect Example archive" }),
		);

		await waitFor(() => expect(client.addSource).toHaveBeenCalledTimes(1));
		expect(client.addSource).toHaveBeenCalledWith({
			orgId: "org-1",
			provider: exampleProvider,
			config: { prefix: "docs/", include_patterns: "", exclude_patterns: "" },
			targetCollection: "docs",
			credential: { kind: "static_secret", secret: "s3cret", webhookSecret: "" },
		});
	});

	it("connects a repository through the GitHub form", async () => {
		const client = fakeClient();
		await openConnectForm(client);
		fireEvent.change(screen.getByLabelText("Folders"), {
			target: { value: "docs/\nsrc/api/" },
		});
		fireEvent.change(screen.getByLabelText("File types"), {
			target: { value: ".md" },
		});
		fireEvent.click(screen.getByRole("button", { name: "Connect repository" }));

		await waitFor(() =>
			expect(client.addGitHubSource).toHaveBeenCalledTimes(1),
		);
		expect(client.addGitHubSource).toHaveBeenCalledWith(
			expect.objectContaining({
				orgId: "org-1",
				repo: "codefly-dev/module-saas-starter",
				paths: ["docs/", "src/api/"],
				fileExtensions: [".md"],
				targetCollection: "docs",
				accessToken: "ghp_token",
			}),
		);
	});

	// The grant belongs where the person is, not in a separate later journey:
	// they have just connected a repository and know exactly who needs to read
	// it. Connecting grants nobody anything — not even the person who connected
	// it — so without this the collection stays unreadable until somebody
	// remembers to come back.
	it("offers the read grant on the collection a connect just landed in", async () => {
		let sources: DatasourceView[] = [];
		const client = fakeClient({
			listSources: vi.fn(async () => sources),
			addGitHubSource: vi.fn(async () => {
				sources = [{ ...sampleSource, repo: "acme/handbook", boundaryNodeId: "node-docs" }];
			}),
			listCollections: vi.fn(async () => [
				{
					nodeId: "node-docs",
					label: "docs",
					scopePath: "org.docs",
					grants: [],
				},
			]),
			listGrantSubjects: vi.fn(async () => [
				{ id: "team", kind: "team" as const, label: "Example Team" },
			]),
			grantCollectionRead: vi.fn(async () => {}),
			revokeCollectionRead: vi.fn(async () => {}),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		fireEvent.click(await screen.findByRole("radio", { name: "GitHub" }));
		fireEvent.change(screen.getByLabelText("Repository"), {
			target: { value: "acme/handbook" },
		});
		fireEvent.change(screen.getByLabelText("Collection"), {
			target: { value: "node-docs" },
		});
		fireEvent.change(screen.getByLabelText("Access token"), {
			target: { value: "ghp_token" },
		});
		fireEvent.click(screen.getByRole("button", { name: "Connect repository" }));

		expect(
			await screen.findByText(/acme\/handbook is connected/),
		).toBeTruthy();
		// And it is an offer, not a gate: it can be put away.
		fireEvent.click(screen.getByRole("button", { name: "Not now" }));
		await waitFor(() =>
			expect(screen.queryByText(/acme\/handbook is connected/)).toBeNull(),
		);
	});

	it("surfaces a connect failure in the form and keeps it open", async () => {
		const client = fakeClient({
			addGitHubSource: vi.fn(async () => {
				throw new Error("refused");
			}),
		});
		await openConnectForm(client);
		fireEvent.click(screen.getByRole("button", { name: "Connect repository" }));

		const alert = await screen.findByRole("alert");
		expect(alert.textContent).toContain("Couldn't connect the repository");
		// The form stays open so the person can correct the input.
		expect(screen.getByLabelText("Repository")).toBeTruthy();
	});

	it("surfaces a sync failure in the panel instead of swallowing it", async () => {
		const client = fakeClient({
			listSources: vi.fn(async () => [sampleSource]),
			reconcileSource: vi.fn(async () => {
				throw new Error("gateway timeout");
			}),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		fireEvent.click(await screen.findByRole("button", { name: /^Sync now / }));

		const alert = await screen.findByRole("alert");
		expect(alert.textContent).toContain("Couldn't sync");
		expect(alert.textContent).toContain("gateway timeout");
		expect(alert.getAttribute("data-tone")).toBe("danger");

		fireEvent.click(screen.getByRole("button", { name: "Dismiss the error" }));
		expect(screen.queryByRole("alert")).toBeNull();
	});

	it("tracks sync state per row and blocks a double-enqueue", async () => {
		// A sync that never settles keeps its row pending, exposing whether a
		// second row's state leaks onto the first (the shared-mutation bug).
		const pending = new Promise<SourceReconcileReceipt>(() => {});
		const reconcileSource = vi.fn(() => pending);
		const client = fakeClient({
			listSources: vi.fn(async () => [sampleSource, secondSource]),
			reconcileSource,
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		await screen.findByText("codefly-dev/module-saas-starter");
		const first = screen.getByRole("button", {
			name: "Sync now codefly-dev/module-saas-starter",
		});
		const second = screen.getByRole("button", {
			name: "Sync now codefly-dev/other-repo",
		});

		fireEvent.click(first);
		await waitFor(() => expect(first.textContent).toBe("Syncing…"));
		// Row 2 is still idle and enabled — row 1's pending state did not leak.
		expect(second.textContent).toBe("Sync now");
		expect((second as HTMLButtonElement).disabled).toBe(false);

		// Clicking row 1 again is a no-op: its button is disabled, so no second job.
		fireEvent.click(first);
		expect(reconcileSource).toHaveBeenCalledTimes(1);

		fireEvent.click(second);
		await waitFor(() => expect(second.textContent).toBe("Syncing…"));
		expect(reconcileSource).toHaveBeenCalledTimes(2);
	});
});

describe("DatasourcesPanel who can read it", () => {
	const docs = {
		nodeId: sampleSource.boundaryNodeId,
		label: "Docs",
		scopePath: "root.docs",
		grants: [
			{
				id: "grant-1",
				subjectId: "user-1",
				subjectKind: "principal" as const,
				scopePath: "root.docs",
				roleId: "role-1",
				subjectLabel: "reader@example.com",
				roleName: "Collection reader",
				actorLabel: "admin@example.com",
			},
		],
	};

	it("names the collection a source lands in and who can read it", async () => {
		const client = fakeClient({
			listSources: vi.fn(async () => [sampleSource]),
			listCollections: vi.fn(async () => [docs]),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		expect(await screen.findByText(/→ Docs/)).toBeTruthy();
		expect(await screen.findByText("reader@example.com")).toBeTruthy();
	});

	it("names the collection from the source when collections cannot be listed, never its id", async () => {
		// The host lists the collection's name with the source for any member.
		const client = fakeClient({
			listSources: vi.fn(async () => [
				{ ...sampleSource, boundaryLabel: "handbook" },
			]),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		expect(await screen.findByText(/→ handbook/)).toBeTruthy();
		expect(screen.queryByText(/11111111/)).toBeNull();
		// Without the collection list there is no reader list to claim.
		expect(screen.queryByText(/Who can read it/)).toBeNull();
	});

	it("lists a collection no source writes into, so its readers can still be chosen", async () => {
		const client = fakeClient({
			listSources: vi.fn(async () => [sampleSource]),
			listCollections: vi.fn(async () => [
				docs,
				{ nodeId: "node-other", label: "Shared notes", scopePath: "x", grants: [] },
			]),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		const others = await screen.findByRole("list", { name: "Other collections" });
		expect(others.textContent).toContain("Shared notes");
		expect(others.textContent).toContain("Nobody yet");
	});
});

describe("ConnectGitHubForm", () => {
	it("gives each instance distinct field ids so two forms don't collide", () => {
		const noop = () => {};
		render(
			<>
				<ConnectGitHubForm onSubmit={noop} onCancel={noop} isPending={false} />
				<ConnectGitHubForm onSubmit={noop} onCancel={noop} isPending={false} />
			</>,
		);

		const repoInputs = screen.getAllByLabelText("Repository");
		expect(repoInputs).toHaveLength(2);
		expect(repoInputs[0].id).not.toBe("");
		expect(repoInputs[0].id).not.toBe(repoInputs[1].id);
	});
});

/** A row's secondary actions live behind its "More actions" menu. */
async function openRowActions() {
	fireEvent.click(
		await screen.findByRole("button", { name: /^More for / }),
	);
	await screen.findByRole("menu");
}

it("acknowledges queued sync without claiming ingestion completed", async () => {
	const client = fakeClient({ listSources: vi.fn(async () => [sampleSource]) });
	renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);
	fireEvent.click(await screen.findByRole("button", { name: /^Sync now / }));
	expect((await screen.findByRole("status")).textContent).toContain(
		"Sync started",
	);
	expect(screen.getByRole("status").textContent).toContain("is syncing");
});

it("shows a rejected credential as an actionable message", async () => {
	const client = fakeClient({
		listSources: vi.fn(async () => [sampleSource]),
		reconcileSource: vi.fn(async () => {
			throw new ConnectError(
				"rpc error: code = FailedPrecondition desc = GitHub rejected the access token (401). Reconnect the source.",
				Code.FailedPrecondition,
			);
		}),
	});
	renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);
	fireEvent.click(await screen.findByRole("button", { name: /^Sync now / }));
	const text = (await screen.findByRole("alert")).textContent;
	expect(text).toContain("GitHub rejected the access token (401)");
	expect(text).not.toContain("rpc error");
	expect(text).not.toContain("[failed_precondition]");
});

it.each(["first", "second"])(
	"settles concurrent syncs when %s finishes first",
	async (finishFirst) => {
		let rejectFirst!: (error: Error) => void;
		let resolveSecond!: (receipt: SourceReconcileReceipt) => void;
		const first = new Promise<SourceReconcileReceipt>((_, reject) => {
			rejectFirst = reject;
		});
		const second = new Promise<SourceReconcileReceipt>((resolve) => {
			resolveSecond = resolve;
		});
		const onSyncEnqueued = vi.fn();
		const client = fakeClient({
			listSources: vi.fn(async () => [sampleSource, secondSource]),
			reconcileSource: vi.fn((_org: string, id: string) =>
				id === sampleSource.id ? first : second,
			),
		});
		renderWithClient(
			<DatasourcesPanel
				client={client}
				orgId="org-1"
				onSyncEnqueued={onSyncEnqueued}
			/>,
		);
		await screen.findByText(sampleSource.repo);
		const buttons = screen.getAllByRole("button", { name: /^Sync now / });
		fireEvent.click(buttons[0]);
		fireEvent.click(buttons[1]);
		const reject = () =>
			rejectFirst(
				new ConnectError(
					"GitHub rejected the access token (401)",
					Code.FailedPrecondition,
				),
			);
		const resolve = () =>
			resolveSecond({
				effectId: "effect-2",
				status: "committed",
				jobId: "job-2",
			});
		await act(async () => {
			(finishFirst === "first" ? reject : resolve)();
		});
		await waitFor(() =>
			expect(screen.getAllByRole("button", { name: /^Syncing/ })).toHaveLength(
				1,
			),
		);
		await act(async () => {
			(finishFirst === "first" ? resolve : reject)();
		});
		await waitFor(() =>
			expect(
				screen.queryAllByRole("button", { name: /^Syncing/ }),
			).toHaveLength(0),
		);
		expect(screen.getByRole("alert").textContent).toContain("401");
		expect(screen.getByRole("status").textContent).toContain(secondSource.repo);
		expect(onSyncEnqueued).toHaveBeenCalledExactlyOnceWith("job-2");
	},
);

it("keeps Sync now, Edit and Remove on the row and the rest behind one menu", async () => {
	const client = fakeClient({
		listSources: vi.fn(async () => [sampleSource]),
		updateSource: vi.fn(async () => undefined),
	});
	renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);
	await screen.findByRole("button", { name: /^Sync now / });
	expect(screen.getByRole("button", { name: /^Edit / })).toBeTruthy();
	expect(screen.queryByRole("button", { name: "Replace token" })).toBeNull();
	const confirm = vi.fn(() => true);
	vi.stubGlobal("confirm", confirm);
	fireEvent.click(screen.getByRole("button", { name: /^Remove / }));
	expect(confirm).toHaveBeenCalledOnce();
	await waitFor(() =>
		expect(client.deleteSource).toHaveBeenCalledWith("org-1", "ds-1"),
	);
	vi.unstubAllGlobals();
	await openRowActions();
	expect(screen.getByRole("menuitem", { name: "Replace token" })).toBeTruthy();
});

it("reconnects the same source without creating or deleting a source", async () => {
	const client = fakeClient({ listSources: vi.fn(async () => [sampleSource]) });
	renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);
	await openRowActions();
	fireEvent.click(await screen.findByRole("menuitem", { name: "Replace token" }));
	const token = screen.getByLabelText("New access token");
	expect(token.getAttribute("type")).toBe("password");
	fireEvent.change(token, { target: { value: "replacement-test-token" } });
	fireEvent.click(screen.getByRole("button", { name: "Save token" }));
	await waitFor(() =>
		expect(client.reconnectSource).toHaveBeenCalledWith(
			"org-1",
			"ds-1",
			"replacement-test-token",
		),
	);
	expect(client.addGitHubSource).not.toHaveBeenCalled();
	expect(client.deleteSource).not.toHaveBeenCalled();
	expect((await screen.findByRole("status")).textContent).toContain(
		"Token replaced",
	);
});

it("reconnects a public source without a credential when the client can", async () => {
	const client = fakeClient({ listSources: vi.fn(async () => [sampleSource]) });
	renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);
	await openRowActions();
	fireEvent.click(await screen.findByRole("menuitem", { name: "Replace token" }));
	const token = screen.getByLabelText("New access token");
	expect(token.hasAttribute("required")).toBe(false);
	fireEvent.click(screen.getByRole("button", { name: "Save token" }));
	await waitFor(() =>
		expect(client.reconnectSource).toHaveBeenCalledWith(
			"org-1",
			"ds-1",
			undefined,
		),
	);
	// A reconnect is not a reconcile: it rebinds whose authority the source's
	// syncs run under, so it must not travel as a pass under an effect identity.
	expect(client.reconcileSource).not.toHaveBeenCalled();
	expect((await screen.findByRole("status")).textContent).toContain(
		"Reconnected",
	);
});

// The fallback that went with `syncSource`: a plain sync carrying a replacement
// credential, which the host reads as a reconnect. A client that cannot
// reconnect is now offered no Reconnect at all, rather than a control that
// quietly does a different operation.
it("offers no Replace token to a client that cannot reconnect", async () => {
	const client = fakeClient({ listSources: vi.fn(async () => [sampleSource]) });
	delete client.reconnectSource;
	renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);
	await screen.findByRole("button", { name: /^Remove / });
	// Nothing else to offer, so no menu at all.
	expect(screen.queryByRole("button", { name: /^More for / })).toBeNull();
});

it("reconnects with a replacement PAT through the reconnect call when the client has one", async () => {
	const client = fakeClient({ listSources: vi.fn(async () => [sampleSource]) });
	renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);
	await openRowActions();
	fireEvent.click(await screen.findByRole("menuitem", { name: "Replace token" }));
	fireEvent.change(screen.getByLabelText("New access token"), {
		target: { value: "replacement-test-token" },
	});
	fireEvent.click(screen.getByRole("button", { name: "Save token" }));
	await waitFor(() =>
		expect(client.reconnectSource).toHaveBeenCalledWith(
			"org-1",
			"ds-1",
			"replacement-test-token",
		),
	);
	expect((await screen.findByRole("status")).textContent).toContain(
		"Token replaced",
	);
});

describe("GitHub App onboarding", () => {
	const appRepositories = [
		{
			repo: "codefly-dev/module-saas-starter",
			defaultBranch: "main",
			alreadyConnected: false,
		},
		{
			repo: "codefly-dev/already-there",
			defaultBranch: "trunk",
			alreadyConnected: true,
		},
	];

	function appClient(overrides: Partial<DatasourceClient> = {}) {
		return fakeClient({
			beginGitHubAppSetup: vi.fn(async () => ({
				installUrl:
					"https://github.com/apps/codefly/installations/new?state=s1",
				state: "s1",
				expiresAt: undefined,
			})),
			completeGitHubAppSetup: vi.fn(async () => ({
				installationId: "42",
				repositories: appRepositories,
			})),
			migrateGitHubSourceToApp: vi.fn(async () => {}),
			...overrides,
		});
	}

	function landOn(search: string) {
		window.history.replaceState(null, "", `/admin/datasources${search}`);
	}

	afterEach(() => landOn(""));

	it("offers the three ways to connect as equal choices, the token first", async () => {
		render(
			<ConnectGitHubForm
				onSubmit={vi.fn()}
				onCancel={vi.fn()}
				isPending={false}
				onBeginAppSetup={async () => {}}
			/>,
		);

		const method = screen.getByRole("radiogroup", { name: "How to connect" });
		expect(
			[...method.querySelectorAll("label")].map(
				(option) => option.querySelector(".type-label")?.textContent,
			),
		).toEqual(["Access token", "GitHub app", "Public repository"]);
		expect(
			screen.getByRole("radio", { name: /Access token/ }).getAttribute(
				"aria-checked",
			),
		).toBe("true");
		expect(screen.getByLabelText("Access token")).toBeTruthy();
	});

	it("offers no GitHub app when the client cannot drive its setup", async () => {
		// A consumer adapting its own client need not implement the App RPCs; the
		// surface must not offer a dead button.
		render(
			<ConnectGitHubForm
				onSubmit={vi.fn()}
				onCancel={vi.fn()}
				isPending={false}
			/>,
		);

		expect(screen.getAllByRole("radio")).toHaveLength(2);
		expect(screen.queryByRole("radio", { name: /GitHub app/ })).toBeNull();
		expect(screen.getByLabelText("Access token")).toBeTruthy();
	});

	it("connects a public repository with no credential", async () => {
		const client = fakeClient();
		await openConnectForm(client);
		fireEvent.click(screen.getByRole("radio", { name: /Public repository/ }));
		expect(screen.queryByLabelText("Access token")).toBeNull();
		expect(screen.queryByLabelText("Webhook secret (optional)")).toBeNull();
		fireEvent.click(screen.getByRole("button", { name: "Connect repository" }));
		await waitFor(() =>
			expect(client.addGitHubSource).toHaveBeenCalledTimes(1),
		);
		expect(client.addGitHubSource).toHaveBeenCalledWith(
			expect.objectContaining({ accessToken: undefined, webhookSecret: "" }),
		);
	});

	it("does not echo operation failures that may contain a credential", async () => {
		const client = fakeClient({
			addGitHubSource: vi.fn(async () => {
				throw new Error("example-secret is invalid");
			}),
		});
		await openConnectForm(client);
		fireEvent.click(screen.getByRole("button", { name: "Connect repository" }));
		expect((await screen.findByRole("alert")).textContent).toContain(
			"Couldn't connect",
		);
		expect(document.body.textContent).not.toContain("example-secret");
	});

	it("sends the browser to the install URL the host minted", async () => {
		const assign = vi.fn();
		vi.spyOn(window.location, "assign").mockImplementation(assign);
		const client = appClient();
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);
		fireEvent.click(await screen.findByRole("radio", { name: "GitHub" }));
		fireEvent.click(screen.getByRole("radio", { name: /GitHub app/ }));
		fireEvent.click(
			screen.getByRole("button", { name: "Install the GitHub app" }),
		);

		await waitFor(() =>
			expect(client.beginGitHubAppSetup).toHaveBeenCalledWith("org-1"),
		);
		expect(assign).toHaveBeenCalledWith(
			"https://github.com/apps/codefly/installations/new?state=s1",
		);
	});

	it("redeems the state and installation the redirect echoed back", async () => {
		landOn("?installation_id=42&setup_action=install&state=s1&code=oauth-1");
		const client = appClient();
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		await waitFor(() =>
			expect(client.completeGitHubAppSetup).toHaveBeenCalledWith(
				"org-1",
				"s1",
				"42",
				"oauth-1",
			),
		);
	});

	it("redeems a setup_action=update return too", async () => {
		// An existing installation gaining repositories comes back as `update`, not
		// `install`; treating only the latter as a return would strand that tenant.
		landOn("?installation_id=42&setup_action=update&state=s1&code=oauth-1");
		const client = appClient();
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		await waitFor(() =>
			expect(client.completeGitHubAppSetup).toHaveBeenCalled(),
		);
	});

	it("burns the state out of the URL so a reload cannot replay it", async () => {
		landOn(
			"?tab=sources&installation_id=42&setup_action=install&state=s1&code=oauth-1",
		);
		const client = appClient();
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		await waitFor(() =>
			expect(client.completeGitHubAppSetup).toHaveBeenCalled(),
		);
		// The state is single-use, so leaving it in the URL would make a refresh
		// report a rejection for a setup that in fact succeeded.
		expect(window.location.search).toBe("?tab=sources");
		expect(client.completeGitHubAppSetup).toHaveBeenCalledTimes(1);
	});

	it("renders the returned repositories with their default branch", async () => {
		landOn("?installation_id=42&state=s1&code=oauth-1");
		renderWithClient(<DatasourcesPanel client={appClient()} orgId="org-1" />);

		const picker = (await screen.findByLabelText(
			"Repository",
		)) as HTMLSelectElement;
		const offered = [...picker.options].map((option) => option.textContent);
		expect(offered).toContain("codefly-dev/module-saas-starter · main");
	});

	it("does not offer a repository this organization already connects", async () => {
		landOn("?installation_id=42&state=s1&code=oauth-1");
		renderWithClient(<DatasourcesPanel client={appClient()} orgId="org-1" />);

		const picker = (await screen.findByLabelText(
			"Repository",
		)) as HTMLSelectElement;
		const connected = [...picker.options].find((option) =>
			option.value.includes("already-there"),
		);
		expect(connected?.disabled).toBe(true);
		expect(connected?.textContent).toContain("already connected");
	});

	it("connects the selected repository with no access token", async () => {
		landOn("?installation_id=42&state=s1&code=oauth-1");
		const client = appClient();
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		fireEvent.change(await screen.findByLabelText("Repository"), {
			target: { value: "codefly-dev/module-saas-starter" },
		});
		fireEvent.change(screen.getByLabelText("Collection"), {
			target: { value: "docs" },
		});
		fireEvent.click(
			screen.getByRole("button", { name: "Connect repository" }),
		);

		await waitFor(() => expect(client.addGitHubSource).toHaveBeenCalled());
		const input = vi.mocked(client.addGitHubSource).mock.calls[0][0];
		expect(input.accessToken).toBeUndefined();
		expect(input.repo).toBe("codefly-dev/module-saas-starter");
		// Picking a repository adopts the branch GitHub reported for it.
		expect(input.branch).toBe("main");
	});

	it("surfaces a rejected return without opening the repository picker", async () => {
		landOn("?installation_id=42&state=stale&code=oauth-1");
		const client = appClient({
			completeGitHubAppSetup: vi.fn(async () => {
				throw new ConnectError(
					"The GitHub App setup link has already been used or has expired.",
					Code.PermissionDenied,
				);
			}),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		const alert = await screen.findByRole("alert");
		expect(alert.textContent).toContain("already been used");
		expect(screen.queryByRole("option", { name: /already-there/ })).toBeNull();
	});

	it("migrates an existing source onto the App in place", async () => {
		const client = appClient({
			listSources: vi.fn(async () => [sampleSource]),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);
		await openRowActions();
		fireEvent.click(
			await screen.findByRole("menuitem", { name: "Use the GitHub app" }),
		);

		await waitFor(() =>
			expect(client.migrateGitHubSourceToApp).toHaveBeenCalledWith(
				"org-1",
				"ds-1",
			),
		);
		// In place: no source is created or removed to change how one authenticates.
		expect(client.addGitHubSource).not.toHaveBeenCalled();
		expect(client.deleteSource).not.toHaveBeenCalled();
		expect((await screen.findByRole("status")).textContent).toContain(
			"GitHub app",
		);
	});

	it("hides the migration action when the client cannot perform it", async () => {
		const client = fakeClient({
			listSources: vi.fn(async () => [sampleSource]),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		await openRowActions();
		await screen.findByRole("menuitem", { name: "Replace token" });
		expect(
			screen.queryByRole("menuitem", { name: "Use the GitHub app" }),
		).toBeNull();
	});

	it("reports a failed migration against the source it names", async () => {
		const client = appClient({
			listSources: vi.fn(async () => [sampleSource]),
			migrateGitHubSourceToApp: vi.fn(async () => {
				throw new ConnectError(
					"The GitHub App installation covering that repository is not connected to this organization.",
					Code.PermissionDenied,
				);
			}),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);
		await openRowActions();
		fireEvent.click(
			await screen.findByRole("menuitem", { name: "Use the GitHub app" }),
		);

		const alert = await screen.findByRole("alert");
		expect(alert.textContent).toContain(sampleSource.repo);
		expect(alert.textContent).toContain("not connected to this organization");
	});

	it("burns the return leg and names who must finish it when the viewer may not", async () => {
		// The viewer who follows the provider's redirect need not be the one who
		// began the install: the App's setup URL is one address, and whoever's
		// session is live in that browser lands here. Skipping the capture for them
		// would drop a finished installation without a trace AND leave the
		// single-use state and the authorization code in the address bar, in
		// history and in same-origin referrers — the very thing the scrub exists
		// to prevent.
		landOn("?installation_id=42&setup_action=install&state=s1&code=oauth-1");
		const client = appClient();
		renderWithClient(
			<DatasourcesPanel client={client} orgId="org-1" canManage={false} />,
		);

		expect(
			await screen.findByText(
				/only an organization administrator can connect/i,
			),
		).toBeTruthy();
		expect(window.location.search).toBe("");
		// Redeeming is the host's to refuse, so the panel does not ask on behalf of
		// a viewer it can see would be refused.
		expect(client.completeGitHubAppSetup).not.toHaveBeenCalled();
		expect(screen.queryByRole("form", { name: "Connect GitHub" })).toBeNull();
	});

	it("redeems the return leg when the viewer's authority arrives after the first render", async () => {
		// `canManage` is read from a credential the panel only observes, and the
		// redirect always lands on a cold page load, so an administrator's own
		// authority can be unknown on the first paint. Frozen at that moment, the
		// installation would be stranded even once the credential arrives.
		landOn("?installation_id=42&setup_action=install&state=s1&code=oauth-1");
		const client = appClient();
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		const panel = (canManage: boolean) => (
			<QueryClientProvider client={queryClient}>
				<DatasourcesPanel client={client} orgId="org-1" canManage={canManage} />
			</QueryClientProvider>
		);
		const { rerender } = render(panel(false));
		await screen.findByText(/only an organization administrator can connect/i);
		expect(client.completeGitHubAppSetup).not.toHaveBeenCalled();

		rerender(panel(true));

		await waitFor(() =>
			expect(client.completeGitHubAppSetup).toHaveBeenCalledTimes(1),
		);
		expect(
			screen.queryByText(/only an organization administrator can connect/i),
		).toBeNull();
	});

	it("leaves the redirect alone when the client cannot redeem it", async () => {
		// The App calls are optional on DatasourceClient, so a consumer may adapt
		// its own client and handle the redirect itself. Consuming the parameters
		// — or the address bar — on its behalf silently breaks that handler.
		landOn("?installation_id=42&setup_action=install&state=s1&code=oauth-1");
		renderWithClient(<DatasourcesPanel client={fakeClient()} orgId="org-1" />);

		await screen.findByText("No sources yet");
		expect(window.location.search).toBe(
			"?installation_id=42&setup_action=install&state=s1&code=oauth-1",
		);
		expect(screen.queryByRole("form", { name: "Connect GitHub" })).toBeNull();
	});

	it("does not re-redeem a burned state against a different organization", async () => {
		// The state is bound to the org that began it, so re-firing it after an org
		// switch reports a rejection for a setup that in fact succeeded. The kit
		// must hold this itself rather than rely on the host keying it by org.
		landOn("?installation_id=42&state=s1&code=oauth-1");
		const client = appClient();
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		const panel = (orgId: string) => (
			<QueryClientProvider client={queryClient}>
				<DatasourcesPanel client={client} orgId={orgId} />
			</QueryClientProvider>
		);
		const { rerender } = render(panel("org-A"));
		await waitFor(() =>
			expect(client.completeGitHubAppSetup).toHaveBeenCalledWith(
				"org-A",
				"s1",
				"42",
				"oauth-1",
			),
		);

		rerender(panel("org-B"));

		await waitFor(() =>
			expect(screen.queryByLabelText("Repository")).toBeNull(),
		);
		expect(client.completeGitHubAppSetup).toHaveBeenCalledTimes(1);
	});

	it("redeems the single-use state exactly once under StrictMode", async () => {
		// A double-mount re-submitting a single-use credential is how the refresh
		// self-reuse bug (#506) reached production.
		landOn("?installation_id=42&state=s1&code=oauth-1");
		const client = appClient();
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		render(
			<StrictMode>
				<QueryClientProvider client={queryClient}>
					<DatasourcesPanel client={client} orgId="org-1" />
				</QueryClientProvider>
			</StrictMode>,
		);

		await waitFor(() =>
			expect(client.completeGitHubAppSetup).toHaveBeenCalled(),
		);
		await screen.findByLabelText("Repository");
		expect(client.completeGitHubAppSetup).toHaveBeenCalledTimes(1);
	});

	it("says it is completing setup, not opening GitHub, on the return leg", async () => {
		landOn("?installation_id=42&state=s1&code=oauth-1");
		let release: (value: {
			installationId: string;
			repositories: typeof appRepositories;
		}) => void = () => {};
		const client = appClient({
			completeGitHubAppSetup: vi.fn(
				() =>
					new Promise<{
						installationId: string;
						repositories: typeof appRepositories;
					}>((resolve) => {
						release = resolve;
					}),
			),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		expect(
			(await screen.findByRole("button", { name: /completing setup/i }))
				.textContent,
		).not.toMatch(/opening github/i);
		await act(async () => {
			release({ installationId: "42", repositories: appRepositories });
		});
	});

	it("explains a picker with nothing left to connect", async () => {
		landOn("?installation_id=42&state=s1&code=oauth-1");
		const client = appClient({
			completeGitHubAppSetup: vi.fn(async () => ({
				installationId: "42",
				repositories: appRepositories.map((repository) => ({
					...repository,
					alreadyConnected: true,
				})),
			})),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		// Every option disabled with no explanation is a dead end the reader
		// cannot act on.
		const status = await screen.findByText(
			/Every repository the app can see is already connected/i,
		);
		expect(status).toBeTruthy();
	});

	it("names an installation that grants no repository at all", async () => {
		landOn("?installation_id=42&state=s1&code=oauth-1");
		const client = appClient({
			completeGitHubAppSetup: vi.fn(async () => ({
				installationId: "42",
				repositories: [],
			})),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		// "already connects every repository" is vacuously true of an empty
		// installation, and sends the reader looking for connections it has none of.
		expect(
			await screen.findByText(/can't see any repository yet/i),
		).toBeTruthy();
		expect(screen.queryByText(/Every repository the app can see is already connected/i)).toBeNull();
	});

	it("stops offering a repository connected earlier in the same session", async () => {
		// `alreadyConnected` is answered once, when the setup completes, and the
		// state behind it is spent — so nothing can re-ask, and a repository
		// connected since would otherwise stay on offer and be connected twice.
		landOn("?installation_id=42&state=s1&code=oauth-1");
		const client = appClient();
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		fireEvent.change(await screen.findByLabelText("Repository"), {
			target: { value: "codefly-dev/module-saas-starter" },
		});
		fireEvent.change(screen.getByLabelText("Collection"), {
			target: { value: "docs" },
		});
		fireEvent.click(
			screen.getByRole("button", { name: "Connect repository" }),
		);
		await waitFor(() =>
			expect(client.addGitHubSource).toHaveBeenCalledTimes(1),
		);

		// Reopening to connect a second repository the same installation grants.
		fireEvent.click(await screen.findByRole("radio", { name: "GitHub" }));

		const option = (await screen.findByRole("option", {
			name: /module-saas-starter/,
		})) as HTMLOptionElement;
		expect(option.disabled).toBe(true);
		expect(option.textContent).toContain("already connected");
		// Folded in locally: re-redeeming the burned state is not the way to learn it.
		expect(client.completeGitHubAppSetup).toHaveBeenCalledTimes(1);
	});

	it("drops a branch belonging to a repository the method switch cleared", async () => {
		landOn("?installation_id=42&state=s1&code=oauth-1");
		renderWithClient(<DatasourcesPanel client={appClient()} orgId="org-1" />);

		// Picking on the App path fills the branch in from the repository.
		fireEvent.change(await screen.findByLabelText("Repository"), {
			target: { value: "codefly-dev/module-saas-starter" },
		});
		expect((screen.getByLabelText(/^Branch/) as HTMLInputElement).value).toBe(
			"main",
		);

		fireEvent.click(screen.getByRole("radio", { name: /Access token/ }));
		fireEvent.change(screen.getByLabelText("Repository"), {
			target: { value: "other/elsewhere" },
		});
		fireEvent.click(screen.getByRole("radio", { name: /GitHub app/ }));

		// The repository is cleared because the picker cannot show it; a branch
		// describing it must not survive to be submitted for a different one.
		expect((screen.getByLabelText(/^Branch/) as HTMLInputElement).value).toBe(
			"",
		);
	});

	it("hides the App path when the client cannot complete the return leg", async () => {
		// begin and complete are independently optional. Offering the install with
		// no way to redeem what comes back strands the tenant on a completed
		// GitHub install with nothing to show for it.

		render(
			<ConnectGitHubForm
				onSubmit={vi.fn()}
				onCancel={vi.fn()}
				isPending={false}
			/>,
		);

		expect(screen.queryByRole("radio", { name: /GitHub app/ })).toBeNull();
		expect(
			screen.queryByRole("button", { name: /Install the GitHub app/ }),
		).toBeNull();
		expect(screen.getByLabelText("Access token")).toBeTruthy();
	});

	it("does not connect a repository the App picker shows as unselected", async () => {
		// Typing a repository on the PAT path and switching back leaves the picker
		// blank — the installation does not grant it — so submitting must not send
		// a repository the reader cannot see chosen.
		landOn("?installation_id=42&state=s1&code=oauth-1");
		const client = appClient();
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);
		await screen.findByLabelText("Repository");

		fireEvent.click(screen.getByRole("radio", { name: /Access token/ }));
		fireEvent.change(screen.getByLabelText("Repository"), {
			target: { value: "acme/not-granted" },
		});
		fireEvent.click(screen.getByRole("radio", { name: /GitHub app/ }));
		fireEvent.change(screen.getByLabelText("Collection"), {
			target: { value: "docs" },
		});
		fireEvent.click(
			screen.getByRole("button", { name: "Connect repository" }),
		);

		await waitFor(() =>
			expect(
				(screen.getByLabelText("Repository") as HTMLSelectElement).value,
			).toBe(""),
		);
		expect(client.addGitHubSource).not.toHaveBeenCalled();
	});

	it("still redeems a return that carried no authorization code", async () => {
		// GitHub omits `code` when the App was registered without user
		// authorization during installation. Treating that as "not a return" would
		// strand the tenant in silence; the host's error names the setting.
		landOn("?installation_id=42&setup_action=install&state=s1");
		const client = appClient();
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		await waitFor(() =>
			expect(client.completeGitHubAppSetup).toHaveBeenCalledWith(
				"org-1",
				"s1",
				"42",
				"",
			),
		);
	});
});

describe("DatasourcesPanel for a viewer who manages nothing", () => {
	it("offers no control the host would refuse, and keeps the history", async () => {
		const client = fakeClient({
			listSources: vi.fn(async () => [sampleSource]),
			listAccessibleScopes: vi.fn(async () => []),
			listActivity: vi.fn(async () => []),
		});
		renderWithClient(
			<DatasourcesPanel client={client} orgId="org-1" canManage={false} />,
		);
		await screen.findByText("codefly-dev/module-saas-starter");
		expect(screen.queryByRole("region", { name: "Connect a source" })).toBeNull();
		expect(screen.queryByRole("button", { name: /^Sync now / })).toBeNull();
		expect(screen.queryByRole("button", { name: /^Remove / })).toBeNull();
		await openRowActions();
		expect(screen.getByRole("menuitem", { name: "History" })).toBeTruthy();
		expect(screen.queryByRole("menuitem", { name: "Replace token" })).toBeNull();
		// What to do instead: whom to ask.
		expect(
			await screen.findByText(
				/Ask an organization administrator to grant you read access/,
			),
		).toBeTruthy();
		expect(
			screen.queryByRole("link", { name: /Grant read access/ }),
		).toBeNull();
	});

	it("offers no menu when there is not even a history to read", async () => {
		const client = fakeClient({
			listSources: vi.fn(async () => [sampleSource]),
		});
		renderWithClient(
			<DatasourcesPanel client={client} orgId="org-1" canManage={false} />,
		);
		await screen.findByText("codefly-dev/module-saas-starter");
		expect(screen.queryByRole("button", { name: /^More for / })).toBeNull();
	});

	it("does not offer to connect a source to an empty organization", async () => {
		renderWithClient(
			<DatasourcesPanel
				client={fakeClient()}
				orgId="org-1"
				canManage={false}
			/>,
		);
		expect(
			await screen.findByText(/An organization administrator connects/),
		).toBeTruthy();
		expect(screen.queryByRole("region", { name: "Connect a source" })).toBeNull();
	});
});
