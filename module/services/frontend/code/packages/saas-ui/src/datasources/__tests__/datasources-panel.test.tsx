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
import { StrictMode, type ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ConnectGitHubForm } from "../connect-github-form.js";
import { DatasourcesPanel } from "../datasources-panel.js";
import type {
	AccessibleScopeView,
	DatasourceClient,
	DatasourceView,
} from "../types.js";

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
		listSources: vi.fn(async () => [] as DatasourceView[]),
		addGitHubSource: vi.fn(async () => {}),
		syncSource: vi.fn(async () => "job-1"),
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
	fireEvent.click(
		await screen.findByRole("button", { name: /connect github/i }),
	);
	fireEvent.change(screen.getByLabelText("Repository"), {
		target: { value: "codefly-dev/module-saas-starter" },
	});
	fireEvent.change(screen.getByLabelText("Target collection"), {
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

		expect(await screen.findByText("Degraded")).toBeTruthy();
		expect(screen.getByText(/over the 8388608-byte ingest limit/)).toBeTruthy();
	});

	it("names the action that restarts a degraded source", async () => {
		// Degrading clears the reconcile schedule and only a full snapshot lifts
		// it, so an ordinary delivery never will: copy that says pulls stopped
		// without naming Sync leaves a tenant who fixed the cause waiting on a
		// pull that cannot come. It must also not imply content was withdrawn.
		const degraded: DatasourceView = {
			...sampleSource,
			status: "degraded",
			statusReason: "snapshot manifest is over the ingest limit",
		};
		const client = fakeClient({ listSources: vi.fn(async () => [degraded]) });
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		const explanation = await screen.findByText(/pulls have stopped/i);
		expect(explanation.textContent).toMatch(/sync/i);
		expect(explanation.textContent).toMatch(/stays readable/i);
	});

	it("renders the reason for a status other than degraded", async () => {
		// status_reason is scoped to "why the source left active", not to one way
		// of leaving it, so gating the render on `degraded` drops a paused
		// source's explanation — the same dropped reason this column exists for.
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

		expect(await screen.findByText("Degraded")).toBeTruthy();
		expect(screen.getByText(/pulls have stopped/i)).toBeTruthy();
	});

	it("leaves the status cell quiet for an active source", async () => {
		// Nearly every row is active, so badging it too buries the states that
		// need a reader — and it would sit one label away from `unknown`.
		const client = fakeClient({
			listSources: vi.fn(async () => [sampleSource]),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		await screen.findByText(sampleSource.repo);
		expect(screen.queryByText("Active")).toBeNull();
		expect(screen.queryByText(/pulls have stopped/i)).toBeNull();
	});

	it("labels the ingest by what moved the clock, not by one of its triggers", async () => {
		// A tenant pressing "Sync now" on a github source dispatches a forced
		// reconcile, which advances this same clock — so a label naming the
		// webhook would attribute the user's own manual pull to a delivery.
		const ingested: DatasourceView = {
			...sampleSource,
			lastIngestedAt: "2026-09-08T11:30:00.000Z",
			lastIngestedCommit: "9f2c1ab7d4e5f60718293a4b5c6d7e8f90a1b2c3",
		};
		const client = fakeClient({ listSources: vi.fn(async () => [ingested]) });
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		const line = await screen.findByText(/last ingest/i);
		expect(line.textContent).not.toMatch(/webhook/i);
		// The short commit git itself would print, not the full 40-char sha.
		expect(line.textContent).toContain("9f2c1ab");
		expect(line.textContent).not.toContain(
			"9f2c1ab7d4e5f60718293a4b5c6d7e8f90a1b2c3",
		);
	});

	it("renders the ingest time of day, not just its date", async () => {
		const at = "2026-09-08T11:30:00.000Z";
		const ingested: DatasourceView = { ...sampleSource, lastIngestedAt: at };
		const client = fakeClient({ listSources: vi.fn(async () => [ingested]) });
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		const line = await screen.findByText(/last ingest/i);
		// Exactly what a date-only render produces. The clock moves on every
		// delivery, so a date cannot separate a source that ingested minutes ago
		// from one whose ingest stopped shortly after midnight.
		expect(line.textContent).not.toBe(
			`last ingest ${new Date(at).toLocaleString()}`,
		);
		expect(line.textContent).toContain(
			new Intl.DateTimeFormat(undefined, { timeStyle: "short" }).format(
				new Date(at),
			),
		);
	});

	it("distinguishes two ingests a minute apart", async () => {
		// One minute keeps both on the same local date in every real timezone, so
		// this fails for a date-only render and for nothing else.
		const client = fakeClient({
			listSources: vi.fn(async () => [
				{ ...sampleSource, lastIngestedAt: "2026-09-08T11:30:00.000Z" },
				{ ...secondSource, lastIngestedAt: "2026-09-08T11:31:00.000Z" },
			]),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		const lines = await screen.findAllByText(/last ingest/i);
		expect(lines).toHaveLength(2);
		expect(lines[0].textContent).not.toBe(lines[1].textContent);
	});

	it('drops the sync clock\'s "Never" when there is an ingest to show', async () => {
		// A github source never sets last_synced_at — the compiler advances the
		// ingest clock instead — so keeping "Never" above live provenance would
		// tell the reader a healthy source has never synced.
		const ingested: DatasourceView = {
			...sampleSource,
			lastSyncedAt: undefined,
			lastIngestedAt: "2026-09-08T11:30:00.000Z",
			lastIngestedCommit: "9f2c1ab7d4e5f60718293a4b5c6d7e8f90a1b2c3",
		};
		const client = fakeClient({ listSources: vi.fn(async () => [ingested]) });
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		await screen.findByText(/last ingest/i);
		expect(screen.queryByText("Never")).toBeNull();
	});

	it("keeps the sync clock for a provider that pulls", async () => {
		// An api/crawler/upload source advances last_synced_at and never ingests,
		// so its cell keeps the column's own value — including "Never".
		const pulled: DatasourceView = {
			...sampleSource,
			lastSyncedAt: "2026-09-08T11:30:00.000Z",
		};
		const client = fakeClient({ listSources: vi.fn(async () => [pulled]) });
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		expect(
			await screen.findByText(
				new Date("2026-09-08T11:30:00.000Z").toLocaleString(),
			),
		).toBeTruthy();
		expect(screen.queryByText(/last ingest/i)).toBeNull();
	});

	it("shows no ingest line for a source whose first delivery has not landed", async () => {
		const client = fakeClient({
			listSources: vi.fn(async () => [sampleSource]),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		await screen.findByText("codefly-dev/module-saas-starter");
		expect(screen.queryByText(/last ingest/i)).toBeNull();
		expect(screen.getByText("Never")).toBeTruthy();
	});

	it("submits the connect form through addGitHubSource", async () => {
		const client = fakeClient();
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		fireEvent.click(
			await screen.findByRole("button", { name: /connect github/i }),
		);
		fireEvent.change(screen.getByLabelText("Repository"), {
			target: { value: "codefly-dev/module-saas-starter" },
		});
		fireEvent.change(screen.getByLabelText("Paths (optional)"), {
			target: { value: "docs/\nsrc/api/" },
		});
		fireEvent.change(screen.getByLabelText("Target collection"), {
			target: { value: "docs" },
		});
		fireEvent.change(screen.getByLabelText("Access token"), {
			target: { value: "ghp_token" },
		});
		fireEvent.click(
			screen.getByRole("button", { name: /^validate and connect$/i }),
		);

		await waitFor(() =>
			expect(client.addGitHubSource).toHaveBeenCalledTimes(1),
		);
		expect(client.addGitHubSource).toHaveBeenCalledWith({
			orgId: "org-1",
			repo: "codefly-dev/module-saas-starter",
			paths: ["docs/", "src/api/"],
			branch: "",
			targetCollection: "docs",
			accessToken: "ghp_token",
			webhookSecret: "",
		});
	});

	it("surfaces a connect failure in the form and keeps the dialog open", async () => {
		const client = fakeClient({
			addGitHubSource: vi.fn(async () => {
				throw new Error("invalid access token");
			}),
		});
		await openConnectForm(client);
		fireEvent.click(
			screen.getByRole("button", { name: /^validate and connect$/i }),
		);

		const alert = await screen.findByRole("alert");
		expect(alert.textContent).toContain("invalid access token");
		// Dialog stays open so the user can correct the input, not silently vanish.
		expect(screen.getByLabelText("Repository")).toBeTruthy();
	});

	it("surfaces a sync failure in the panel instead of swallowing it", async () => {
		const client = fakeClient({
			listSources: vi.fn(async () => [sampleSource]),
			syncSource: vi.fn(async () => {
				throw new Error("gateway timeout");
			}),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		fireEvent.click(await screen.findByRole("button", { name: /^Sync$/ }));

		const alert = await screen.findByRole("alert");
		expect(alert.textContent).toContain("Couldn't sync");
		expect(alert.textContent).toContain("gateway timeout");
	});

	it("tracks sync state per row and blocks a double-enqueue", async () => {
		// A sync that never settles keeps its row pending, exposing whether a
		// second row's state leaks onto the first (the shared-mutation bug).
		const pending = new Promise<string>(() => {});
		const syncSource = vi.fn(() => pending);
		const client = fakeClient({
			listSources: vi.fn(async () => [sampleSource, secondSource]),
			syncSource,
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		await screen.findByText("codefly-dev/module-saas-starter");
		const syncButtons = screen.getAllByRole("button", { name: /^Sync$/ });
		expect(syncButtons).toHaveLength(2);

		fireEvent.click(syncButtons[0]);
		await waitFor(() =>
			expect(
				screen.getAllByRole("button", { name: /^Syncing…$/ }),
			).toHaveLength(1),
		);
		// Row 2 is still idle and enabled — row 1's pending state did not leak.
		const stillIdle = screen.getByRole("button", { name: /^Sync$/ });
		expect((stillIdle as HTMLButtonElement).disabled).toBe(false);

		// Clicking row 1 again is a no-op: its button is disabled, so no second job.
		fireEvent.click(screen.getByRole("button", { name: /^Syncing…$/ }));
		expect(syncSource).toHaveBeenCalledTimes(1);

		fireEvent.click(stillIdle);
		await waitFor(() =>
			expect(
				screen.getAllByRole("button", { name: /^Syncing…$/ }),
			).toHaveLength(2),
		);
		expect(syncSource).toHaveBeenCalledTimes(2);
	});
});

describe("DatasourcesPanel boundary column", () => {
	const boundaryId = sampleSource.boundaryNodeId;

	it("names the boundary and summarizes the caller's grants on it", async () => {
		const client = fakeClient({
			listSources: vi.fn(async () => [sampleSource]),
			listAccessibleScopes: vi.fn(async () => [
				{
					nodeId: boundaryId,
					label: "Docs",
					kind: "collection",
					actions: ["write", "read"],
				},
			]),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		expect(await screen.findByText("Docs")).toBeTruthy();
		// Ordered read-then-write regardless of the order the lookup reported.
		expect(screen.getByText("Read · Write")).toBeTruthy();
		expect(client.listAccessibleScopes).toHaveBeenCalledWith("org-1");
	});

	it("distinguishes no readable collection from empty indexed content", async () => {
        const client = fakeClient({listSources: vi.fn(async () => [sampleSource]), listAccessibleScopes: vi.fn(async () => [])});
        renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);
        expect(await screen.findByText(/No readable collection/)).toBeTruthy();
    });

	it("refetches boundaries after a source is connected", async () => {
		// Connecting resolves the target collection to a boundary node, so a
		// boundary answer held from before the add is stale. Without invalidating
		// it the new row renders an opaque id for a boundary the caller holds.
		let scopes: AccessibleScopeView[] = [];
		const client = fakeClient({
			listSources: vi.fn(async () => [sampleSource]),
			listAccessibleScopes: vi.fn(async () => scopes),
			addGitHubSource: vi.fn(async () => {
				scopes = [
					{
						nodeId: boundaryId,
						label: "Docs",
						kind: "collection",
						actions: ["read"],
					},
				];
			}),
		});
		await openConnectForm(client);
		expect(await screen.findByText("11111111")).toBeTruthy();

		fireEvent.click(screen.getByRole("button", { name: /^validate and connect$/i }));

		await waitFor(() => expect(screen.getByText("Docs")).toBeTruthy());
	});

	it("never claims no access when the boundary could not be looked up", async () => {
		// A client with no listAccessibleScopes — a gateway-bound remote, whose SDK
		// does not carry the accessible-scopes RPC. Absence of a grant is unknown
		// here, so asserting "No access" would be a false statement about authority.
		const client = fakeClient({
			listSources: vi.fn(async () => [sampleSource]),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		expect(await screen.findByText("11111111")).toBeTruthy();
		expect(screen.queryByText("No access")).toBeNull();
	});

	it("degrades to the boundary id when the lookup fails", async () => {
		const client = fakeClient({
			listSources: vi.fn(async () => [sampleSource]),
			listAccessibleScopes: vi.fn(async () => {
				throw new Error("accessible-scopes unserved");
			}),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		// The panel still lists its sources: an unresolved boundary is a degraded
		// cell, not a failed panel.
		expect(
			await screen.findByText("codefly-dev/module-saas-starter"),
		).toBeTruthy();
		expect(screen.getByText("11111111")).toBeTruthy();
		expect(screen.queryByText("No access")).toBeNull();
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

it("acknowledges queued sync without claiming ingestion completed", async () => {
	const client = fakeClient({ listSources: vi.fn(async () => [sampleSource]) });
	renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);
	fireEvent.click(await screen.findByRole("button", { name: /^Sync$/ }));
	expect((await screen.findByRole("status")).textContent).toContain(
		"Sync queued",
	);
	expect(screen.getByRole("status").textContent).toContain("background");
});

it("shows a rejected credential as an actionable message", async () => {
	const client = fakeClient({
		listSources: vi.fn(async () => [sampleSource]),
		syncSource: vi.fn(async () => {
			throw new ConnectError(
				"rpc error: code = FailedPrecondition desc = GitHub rejected the access token (401). Reconnect the source.",
				Code.FailedPrecondition,
			);
		}),
	});
	renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);
	fireEvent.click(await screen.findByRole("button", { name: /^Sync$/ }));
	const text = (await screen.findByRole("alert")).textContent;
	expect(text).toContain("GitHub rejected the access token (401)");
	expect(text).not.toContain("rpc error");
	expect(text).not.toContain("[failed_precondition]");
});

it.each(["first", "second"])(
	"settles concurrent syncs when %s finishes first",
	async (finishFirst) => {
		let rejectFirst!: (error: Error) => void;
		let resolveSecond!: (id: string) => void;
		const first = new Promise<string>((_, reject) => {
			rejectFirst = reject;
		});
		const second = new Promise<string>((resolve) => {
			resolveSecond = resolve;
		});
		const onSyncEnqueued = vi.fn();
		const client = fakeClient({
			listSources: vi.fn(async () => [sampleSource, secondSource]),
			syncSource: vi.fn((_, id) => (id === sampleSource.id ? first : second)),
		});
		renderWithClient(
			<DatasourcesPanel
				client={client}
				orgId="org-1"
				onSyncEnqueued={onSyncEnqueued}
			/>,
		);
		await screen.findByText(sampleSource.repo);
		const buttons = screen.getAllByRole("button", { name: /^Sync$/ });
		fireEvent.click(buttons[0]);
		fireEvent.click(buttons[1]);
		const reject = () =>
			rejectFirst(
				new ConnectError(
					"GitHub rejected the access token (401)",
					Code.FailedPrecondition,
				),
			);
		const resolve = () => resolveSecond("job-2");
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

 it("reconnects the same source without creating or deleting a source", async () => {
  const client = fakeClient({listSources: vi.fn(async () => [sampleSource])});
  renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);
  fireEvent.click(await screen.findByRole("button", {name: "Reconnect"}));
  const token = screen.getByLabelText("New GitHub PAT");
  expect(token.getAttribute("type")).toBe("password");
  fireEvent.change(token, {target:{value:"replacement-test-token"}});
  fireEvent.click(screen.getByRole("button", {name:"Reconnect and sync"}));
  await waitFor(() => expect(client.syncSource).toHaveBeenCalledWith("org-1", "ds-1", "replacement-test-token"));
  expect(client.addGitHubSource).not.toHaveBeenCalled();
  expect(client.deleteSource).not.toHaveBeenCalled();
  expect((await screen.findByRole("status")).textContent).toContain("Credential replaced");
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
				installUrl: "https://github.com/apps/codefly/installations/new?state=s1",
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

	it("offers the App as the default and keeps the PAT as a named alternative", async () => {
		renderWithClient(<DatasourcesPanel client={appClient()} orgId="org-1" />);
		fireEvent.click(
			await screen.findByRole("button", { name: /connect github/i }),
		);

		const method = screen.getByLabelText("Authentication") as HTMLSelectElement;
		expect(method.value).toBe("app");
		// The App path asks for no token at all — that is the whole point of it.
		expect(screen.queryByLabelText("Access token")).toBeNull();
		expect(
			[...method.options].map((option) => option.textContent),
		).toEqual([
			"GitHub App (recommended)",
			"Fine-grained personal access token",
		]);
	});

	it("falls back to the PAT path when the client cannot drive App setup", async () => {
		// A consumer adapting its own client need not implement the App RPCs; the
		// surface must degrade to the token form rather than offer a dead button.
		renderWithClient(<DatasourcesPanel client={fakeClient()} orgId="org-1" />);
		fireEvent.click(
			await screen.findByRole("button", { name: /connect github/i }),
		);

		expect(screen.queryByLabelText("Authentication")).toBeNull();
		expect(screen.getByLabelText("Access token")).toBeTruthy();
	});

	it("sends the browser to the install URL the host minted", async () => {
		const assign = vi.fn();
		vi.spyOn(window.location, "assign").mockImplementation(assign);
		const client = appClient();
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);
		fireEvent.click(
			await screen.findByRole("button", { name: /connect github/i }),
		);
		fireEvent.click(
			screen.getByRole("button", { name: /install or select repositories/i }),
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

		await waitFor(() => expect(client.completeGitHubAppSetup).toHaveBeenCalled());
	});

	it("burns the state out of the URL so a reload cannot replay it", async () => {
		landOn("?tab=sources&installation_id=42&setup_action=install&state=s1&code=oauth-1");
		const client = appClient();
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		await waitFor(() => expect(client.completeGitHubAppSetup).toHaveBeenCalled());
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
		fireEvent.change(screen.getByLabelText("Target collection"), {
			target: { value: "docs" },
		});
		fireEvent.click(
			screen.getByRole("button", { name: "Connect through the GitHub App" }),
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
		fireEvent.click(await screen.findByRole("button", { name: "Use GitHub App" }));

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
			"GitHub App",
		);
	});

	it("hides the migration action when the client cannot perform it", async () => {
		const client = fakeClient({ listSources: vi.fn(async () => [sampleSource]) });
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);

		await screen.findByRole("button", { name: "Reconnect" });
		expect(screen.queryByRole("button", { name: "Use GitHub App" })).toBeNull();
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
		fireEvent.click(await screen.findByRole("button", { name: "Use GitHub App" }));

		const alert = await screen.findByRole("alert");
		expect(alert.textContent).toContain(sampleSource.repo);
		expect(alert.textContent).toContain("not connected to this organization");
	});

	it("leaves the redirect alone when the client cannot redeem it", async () => {
		// The App calls are optional on DatasourceClient, so a consumer may adapt
		// its own client and handle the redirect itself. Consuming the parameters
		// — or the address bar — on its behalf silently breaks that handler.
		landOn("?installation_id=42&setup_action=install&state=s1&code=oauth-1");
		renderWithClient(<DatasourcesPanel client={fakeClient()} orgId="org-1" />);

		await screen.findByText(/No data sources connected/i);
		expect(window.location.search).toBe(
			"?installation_id=42&setup_action=install&state=s1&code=oauth-1",
		);
		expect(screen.queryByRole("dialog", { name: "Connect GitHub" })).toBeNull();
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

		await waitFor(() => expect(screen.queryByLabelText("Repository")).toBeNull());
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

		await waitFor(() => expect(client.completeGitHubAppSetup).toHaveBeenCalled());
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
		const status = await screen.findByText(/already connects every repository/i);
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
			await screen.findByText(/grants access to no repository/i),
		).toBeTruthy();
		expect(screen.queryByText(/already connects every repository/i)).toBeNull();
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
		fireEvent.change(screen.getByLabelText("Target collection"), {
			target: { value: "docs" },
		});
		fireEvent.click(
			screen.getByRole("button", { name: "Connect through the GitHub App" }),
		);
		await waitFor(() => expect(client.addGitHubSource).toHaveBeenCalledTimes(1));

		// Reopening to connect a second repository the same installation grants.
		fireEvent.click(
			await screen.findByRole("button", { name: /connect github/i }),
		);

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
		expect(
			(screen.getByLabelText(/^Branch/) as HTMLInputElement).value,
		).toBe("main");

		const method = screen.getByLabelText("Authentication");
		fireEvent.change(method, { target: { value: "pat" } });
		fireEvent.change(screen.getByLabelText("Repository"), {
			target: { value: "other/elsewhere" },
		});
		fireEvent.change(method, { target: { value: "app" } });

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
		const client = fakeClient({
			beginGitHubAppSetup: vi.fn(async () => ({
				installUrl: "https://github.com/apps/codefly/installations/new?state=s1",
				state: "s1",
				expiresAt: undefined,
			})),
		});
		renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);
		fireEvent.click(
			await screen.findByRole("button", { name: /connect github/i }),
		);

		expect(screen.queryByLabelText("Authentication")).toBeNull();
		expect(
			screen.queryByRole("button", {
				name: /install or select repositories/i,
			}),
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

		fireEvent.change(screen.getByLabelText("Authentication"), {
			target: { value: "pat" },
		});
		fireEvent.change(screen.getByLabelText("Repository"), {
			target: { value: "acme/not-granted" },
		});
		fireEvent.change(screen.getByLabelText("Authentication"), {
			target: { value: "app" },
		});
		fireEvent.change(screen.getByLabelText("Target collection"), {
			target: { value: "docs" },
		});
		fireEvent.click(
			screen.getByRole("button", { name: "Connect through the GitHub App" }),
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
