import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DeclaredSourceCard } from "../declared-source-card.js";
import {
	declaredCollectionLabel,
	type DeclaredSource,
	matchDeclaredSources,
} from "../declared-source.js";
import type { DatasourceClient, DatasourceView } from "../types.js";

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

const declared: DeclaredSource = {
	provider: "github",
	repo: "example-org/handbook",
	paths: ["proposals/"],
	ref: "main",
	label: "Proposals",
};

const connected: DatasourceView = {
	id: "ds-1",
	orgId: "org-1",
	provider: "github",
	repo: "example-org/handbook",
	paths: ["proposals/"],
	branch: "main",
	boundaryNodeId: "11111111-1111-1111-1111-111111111111",
	webhookConfigured: false,
	liveDelivery: "app_webhook",
	reconcileIntervalSeconds: 3600,
	status: "active",
	lastSyncedAt: undefined,
	lastIngestedAt: "2026-10-02T09:30:00.000Z",
	lastIngestedCommit: "abcdef1234567890",
	createdAt: undefined,
};

describe("matchDeclaredSources", () => {
	it("matches a repository however its owner and name are cased", () => {
		// GitHub treats owner/name case-insensitively, so a source connected as
		// Example-Org/Handbook IS the declared repository. Comparing literally
		// would show the declaration as unconnected beside the source serving it.
		expect(
			matchDeclaredSources({ provider: "github", repo: "Example-Org/Handbook" }, [
				connected,
			]),
		).toHaveLength(1);
	});

	it("ignores paths when the declaration names none", () => {
		expect(
			matchDeclaredSources(
				{ provider: "github", repo: "example-org/handbook" },
				[{ ...connected, paths: ["anything/"] }],
			),
		).toHaveLength(1);
	});

	it("requires the same path set when the declaration names paths", () => {
		expect(
			matchDeclaredSources(declared, [{ ...connected, paths: ["docs/"] }]),
		).toHaveLength(0);
		expect(
			matchDeclaredSources({ ...declared, paths: ["a/", "b/"] }, [
				{ ...connected, paths: ["b/", "a/"] },
			]),
		).toHaveLength(1);
	});

	it("lands entries in the declared label, falling back to the repository", () => {
		expect(declaredCollectionLabel(declared)).toBe("Proposals");
		expect(
			declaredCollectionLabel({ provider: "github", repo: "example-org/handbook" }),
		).toBe("example-org/handbook");
	});
});

describe("DeclaredSourceCard", () => {
	it("asks only for the credential when nothing is connected yet", async () => {
		const client = fakeClient();
		renderWithClient(
			<DeclaredSourceCard client={client} orgId="org-1" declared={declared} />,
		);

		expect(await screen.findByText("Set up")).toBeTruthy();
		// The declaration is shown, as text — see "never offers the repository
		// as a field" below for the half that matters.
		expect(screen.getByText("example-org/handbook")).toBeTruthy();
		expect(screen.getByText("proposals/")).toBeTruthy();
		expect(screen.getByLabelText("Authentication")).toBeTruthy();
	});

	it("connects the declared repository, paths and ref, not anything typed", async () => {
		const client = fakeClient();
		renderWithClient(
			<DeclaredSourceCard client={client} orgId="org-1" declared={declared} />,
		);
		fireEvent.change(await screen.findByLabelText("Access token"), {
			target: { value: "ghp_token" },
		});
		fireEvent.click(screen.getByRole("button", { name: "Connect" }));

		await waitFor(() => expect(client.addGitHubSource).toHaveBeenCalled());
		expect(client.addGitHubSource).toHaveBeenCalledWith({
			orgId: "org-1",
			repo: "example-org/handbook",
			paths: ["proposals/"],
			branch: "main",
			targetCollection: "Proposals",
			accessToken: "ghp_token",
			webhookSecret: "",
		});
	});

	it("never offers the repository as a field", async () => {
		const client = fakeClient();
		renderWithClient(
			<DeclaredSourceCard client={client} orgId="org-1" declared={declared} />,
		);

		await screen.findByText("Set up");
		// The whole point of a declaration: the repository is not a question.
		// A control for it — free text or a picker — would let the person
		// connect something other than what the solution is built on.
		expect(screen.queryByLabelText("Repository")).toBeNull();
		expect(screen.queryByRole("textbox")).toBeNull();
		expect(
			screen
				.getAllByRole("combobox")
				.map((element) => (element as HTMLSelectElement).id),
		).toHaveLength(1);
	});

	it("tells a viewer who cannot manage who does, and offers nothing", async () => {
		const client = fakeClient();
		renderWithClient(
			<DeclaredSourceCard
				client={client}
				orgId="org-1"
				declared={declared}
				canManage={false}
			/>,
		);

		expect(await screen.findByText("Set up")).toBeTruthy();
		expect(
			screen.getByText(/An organization administrator connects it/),
		).toBeTruthy();
		expect(screen.queryByLabelText("Authentication")).toBeNull();
		expect(screen.queryByRole("button", { name: "Connect" })).toBeNull();
	});

	it("reports a connected source's provenance and delivery, and syncs it", async () => {
		const client = fakeClient({
			listSources: vi.fn(async () => [connected]),
		});
		const onSyncEnqueued = vi.fn();
		renderWithClient(
			<DeclaredSourceCard
				client={client}
				orgId="org-1"
				declared={declared}
				onSyncEnqueued={onSyncEnqueued}
			/>,
		);

		expect(await screen.findByText("Connected")).toBeTruthy();
		expect(screen.getByText(/last ingest .* · abcdef1/)).toBeTruthy();
		expect(
			screen.getByText(/On push, through the GitHub App/),
		).toBeTruthy();
		expect(screen.getByText(/reconciled every 1 hour/)).toBeTruthy();
		// Connected is not a state that asks for a credential again.
		expect(screen.queryByLabelText("Authentication")).toBeNull();

		fireEvent.click(screen.getByRole("button", { name: "Sync now" }));
		await waitFor(() =>
			expect(client.syncSource).toHaveBeenCalledWith("org-1", "ds-1"),
		);
		expect(onSyncEnqueued).toHaveBeenCalledWith("job-1");
	});

	it("shows a degraded source's reason verbatim and reconnects with a new token", async () => {
		const degraded: DatasourceView = {
			...connected,
			status: "degraded",
			statusReason:
				"the stored credential was rejected by GitHub (401) on the last 3 attempts",
		};
		const reconnectSource = vi.fn(async () => "job-2");
		const client = fakeClient({
			listSources: vi.fn(async () => [degraded]),
			reconnectSource,
		});
		renderWithClient(
			<DeclaredSourceCard client={client} orgId="org-1" declared={declared} />,
		);

		expect(await screen.findByText("Error")).toBeTruthy();
		// The host writes status_reason from a closed set of named reasons, so
		// it is rendered as it arrives rather than reworded into a guess.
		expect(
			screen.getByText(
				"the stored credential was rejected by GitHub (401) on the last 3 attempts",
			),
		).toBeTruthy();

		fireEvent.change(screen.getByLabelText("Access token"), {
			target: { value: "ghp_replacement" },
		});
		fireEvent.click(
			screen.getByRole("button", { name: "Reconnect and sync" }),
		);
		await waitFor(() =>
			expect(reconnectSource).toHaveBeenCalledWith(
				"org-1",
				"ds-1",
				"ghp_replacement",
			),
		);
		// Reconnecting a declared source never re-asks for the repository.
		expect(client.addGitHubSource).not.toHaveBeenCalled();
	});

	it("leaves a degraded source to an administrator when the viewer is not one", async () => {
		const client = fakeClient({
			listSources: vi.fn(async () => [
				{ ...connected, status: "degraded" as const, statusReason: "paused" },
			]),
		});
		renderWithClient(
			<DeclaredSourceCard
				client={client}
				orgId="org-1"
				declared={declared}
				canManage={false}
			/>,
		);

		expect(await screen.findByText("Error")).toBeTruthy();
		expect(
			screen.getByText("An organization administrator reconnects this source."),
		).toBeTruthy();
		expect(screen.queryByLabelText("Access token")).toBeNull();
		expect(screen.queryByRole("button", { name: /Reconnect/ })).toBeNull();
	});

	it("tells the three states apart by shape, and says the repository was not chosen here", async () => {
		// The consuming solution reported that a Connected badge on `secondary`
		// reads neutral beside an outline "Set up", so a working source looks
		// like one nobody has connected. Each state now carries a dot as well as
		// a tone, and Connected no longer borrows `secondary`.
		const client = fakeClient({
			listSources: vi.fn(async () => [connected]),
		});
		const { container } = renderWithClient(
			<DeclaredSourceCard client={client} orgId="org-1" declared={declared} />,
		);

		const badge = await screen.findByText("Connected");
		expect(badge.className).not.toMatch(/bg-secondary/);
		expect(container.querySelector('[data-slot="badge-dot"]')).toBeTruthy();
		// The card's own description slot, not a muted paragraph in the body:
		// the first thing to understand is that the repository below is not a
		// choice being offered.
		const description = container.querySelector(
			'[data-slot="card-description"]',
		);
		expect(description?.textContent).toBe(
			"Declared by this solution, not chosen here.",
		);
	});

	it("does not call a source in error when the host did not report its state", async () => {
		// "unknown" is what an OLDER host sends: the status field decodes to its
		// proto default and the gateway maps it there rather than guessing. The
		// source is connected; treating the absence of a status as DEGRADED
		// would paint it red, with no status_reason to show for it, and offer to
		// Reconnect — asking for a credential again to fix nothing.
		const client = fakeClient({
			listSources: vi.fn(async () => [
				{ ...connected, status: "unknown" as const },
			]),
		});
		renderWithClient(
			<DeclaredSourceCard client={client} orgId="org-1" declared={declared} />,
		);

		expect(await screen.findByText("Connected")).toBeTruthy();
		expect(screen.queryByText("Error")).toBeNull();
		expect(
			screen.getByText(/This host does not report this source's state/),
		).toBeTruthy();
		// Sync is offered — it works on any host — and Reconnect is not.
		expect(screen.getByRole("button", { name: "Sync now" })).toBeTruthy();
		expect(screen.queryByRole("button", { name: /Reconnect/ })).toBeNull();
		expect(screen.queryByLabelText("Access token")).toBeNull();
	});

	it("reports two matching sources instead of silently picking one", async () => {
		// Two sources can legitimately exist for one repository — one through the
		// App and one through a PAT, say — and they can disagree about branch,
		// scope and health. Rendering the first would sync one and leave the
		// other ingesting invisibly.
		const second: DatasourceView = {
			...connected,
			id: "ds-2",
			branch: "release",
			status: "degraded",
		};
		const client = fakeClient({
			listSources: vi.fn(async () => [connected, second]),
		});
		renderWithClient(
			<DeclaredSourceCard client={client} orgId="org-1" declared={declared} />,
		);

		expect(
			await screen.findByText("More than one source matches this declaration"),
		).toBeTruthy();
		expect(screen.getByText(/ds-1/)).toBeTruthy();
		expect(screen.getByText(/2 connected sources read/)).toBeTruthy();
		expect(screen.queryByRole("button", { name: "Sync now" })).toBeNull();
		expect(screen.queryByLabelText("Authentication")).toBeNull();
	});

	describe("the GitHub App return leg", () => {
		function landOn(search: string) {
			window.history.replaceState(null, "", `/s/proposals${search}`);
		}
		afterEach(() => landOn(""));

		function appClient(overrides: Partial<DatasourceClient> = {}) {
			return fakeClient({
				beginGitHubAppSetup: vi.fn(async () => ({
					installUrl: "https://github.com/apps/example/installations/new",
					state: "s1",
					expiresAt: undefined,
				})),
				completeGitHubAppSetup: vi.fn(async () => ({
					installationId: "42",
					repositories: [],
				})),
				...overrides,
			});
		}

		it("connects through the App with no token at all", async () => {
			const client = appClient();
			renderWithClient(
				<DeclaredSourceCard client={client} orgId="org-1" declared={declared} />,
			);

			// The App is the default where it is available, and it sends no
			// credential: the host resolves the installation covering the repo.
			expect(await screen.findByLabelText("Authentication")).toHaveProperty(
				"value",
				"app",
			);
			expect(screen.queryByLabelText("Access token")).toBeNull();
			fireEvent.click(screen.getByRole("button", { name: "Connect" }));

			await waitFor(() => expect(client.addGitHubSource).toHaveBeenCalled());
			const input = vi.mocked(client.addGitHubSource).mock.calls[0]?.[0];
			expect(input?.repo).toBe("example-org/handbook");
			// Absent, not empty: the host reads a missing token as "resolve the
			// installation covering this repository" (AddGitHubSourceRequest).
			expect(input && "accessToken" in input).toBe(false);
		});

		it("redeems the state the redirect echoed back", async () => {
			landOn("?installation_id=42&setup_action=install&state=s1&code=oauth-1");
			const client = appClient();
			renderWithClient(
				<DeclaredSourceCard client={client} orgId="org-1" declared={declared} />,
			);

			await waitFor(() =>
				expect(client.completeGitHubAppSetup).toHaveBeenCalledWith(
					"org-1",
					"s1",
					"42",
					"oauth-1",
				),
			);
			// And burns the single-use parameters out of the address bar.
			expect(window.location.search).toBe("");
		});

		it("says so when the viewer cannot redeem the return it just spent", async () => {
			// The capture and the scrub run regardless of authority, because the
			// credential canManage is read from may not have arrived on the load
			// that follows the redirect. A viewer who really cannot manage has
			// spent a single-use state on an installation standing at GitHub.
			landOn("?installation_id=42&state=s1&code=oauth-1");
			const client = appClient();
			renderWithClient(
				<DeclaredSourceCard
					client={client}
					orgId="org-1"
					declared={declared}
					canManage={false}
				/>,
			);

			expect(
				await screen.findByText(
					"The installation finished, but it is not yours to connect",
				),
			).toBeTruthy();
			expect(client.completeGitHubAppSetup).not.toHaveBeenCalled();
		});
	});

	it("says the state is unknown when the source list could not be read", async () => {
		// An unreadable list is not an unconnected source: offering "Set up"
		// here would invite a second connection of a repository that may well
		// already be connected.
		const client = fakeClient({
			listSources: vi.fn(async () => {
				throw new Error("datasource service unavailable");
			}),
		});
		renderWithClient(
			<DeclaredSourceCard client={client} orgId="org-1" declared={declared} />,
		);

		expect(
			await screen.findByText(/has not been reported as disconnected/),
		).toBeTruthy();
		expect(screen.queryByText("Set up")).toBeNull();
		expect(screen.queryByLabelText("Authentication")).toBeNull();
	});
});
