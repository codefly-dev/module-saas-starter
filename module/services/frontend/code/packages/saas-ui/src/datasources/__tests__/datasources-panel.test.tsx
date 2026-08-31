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
});
