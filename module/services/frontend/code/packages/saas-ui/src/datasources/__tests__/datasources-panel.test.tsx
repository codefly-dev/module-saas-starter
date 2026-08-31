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
import { ConnectGitHubForm } from "../connect-github-form.js";
import { DatasourcesPanel } from "../datasources-panel.js";
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

const sampleSource: DatasourceView = {
	id: "ds-1",
	orgId: "org-1",
	provider: "github",
	repo: "codefly-dev/module-saas-starter",
	paths: ["docs/"],
	branch: "main",
	targetCollection: "docs",
	webhookConfigured: true,
	status: "active",
	lastSyncedAt: undefined,
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
		fireEvent.click(screen.getByRole("button", { name: /^connect$/i }));

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
		fireEvent.click(screen.getByRole("button", { name: /^connect$/i }));

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
