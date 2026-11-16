import "@testing-library/jest-dom/vitest";
import type { DatasourceClient, DatasourceView } from "@codefly-dev/saas-ui";
import { cleanup, fireEvent, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp } from "@/test/container";
import { DatasourcesAdmin } from "../datasources-admin";

// The host's Data sources page around the kit's panel: no organization ID
// under the title, and everything most organizations never touch folded into
// one collapsed Advanced section, each part said in one plain line.

const ORG = "01a16fbc-1595-78c6-aa83-ed2c909c3e1d";

vi.mock("@/lib/auth", () => ({
	useAuth: () => ({ organizationId: ORG, orgRole: "admin" }),
}));
vi.mock("@/components/org-selector", () => ({ OrgSelector: () => null }));
vi.mock("@/lib/public-runtime-config-provider", () => ({
	usePublicRuntimeConfig: () => ({ collectionContentResource: "documents" }),
}));

const handbook: DatasourceView = {
	id: "0196a1b2-7c3d-7e4f-8a5b-000000000001",
	orgId: ORG,
	provider: "github",
	repo: "acme/handbook",
	paths: [],
	branch: "",
	boundaryNodeId: "0196a1b2-7c3d-7e4f-8a5b-6c7d8e9f0a1b",
	boundaryLabel: "Handbook",
	webhookConfigured: false,
	reconcileIntervalSeconds: 21600,
	status: "active",
	lastSyncedAt: undefined,
	createdAt: undefined,
};

const fake: DatasourceClient = {
	getCatalog: async () => [],
	listSources: async () => [handbook],
	addGitHubSource: async () => {},
	reconcileSource: async () => ({ effectId: "e", status: "committed", jobId: "" }),
	lookupReconcileSource: async () => undefined,
	deleteSource: async () => {},
	listMyAccountLinks: async () => [],
	beginAccountLink: async () => ({ authorizeUrl: "https://example.com", state: "dl.s" }),
	getDirectory: async () => ({ links: [], bindings: [], domains: [], teams: [] }),
	bindGroup: async () => {
		throw new Error("unused");
	},
	claimDomain: async () => {
		throw new Error("unused");
	},
};

vi.mock("../datasource-client", () => ({
	createDatasourceClient: () => fake,
}));

afterEach(cleanup);

describe("DatasourcesAdmin", () => {
	it("keeps the settings most organizations never change in one collapsed section", async () => {
		renderInApp(<DatasourcesAdmin />);
		const advanced = await screen.findByRole("button", { name: "Advanced" });
		expect(advanced).toHaveAttribute("aria-expanded", "false");
		expect(screen.queryByText("Sync schedule")).toBeNull();
		expect(screen.queryByText(/Reconcile schedules/)).toBeNull();

		fireEvent.click(advanced);
		for (const heading of [
			"Sync schedule",
			"Your GitHub account",
			"GitHub teams",
			"Verified domains",
		])
			expect(
				await screen.findByRole("heading", { name: heading }),
			).toBeVisible();
		const schedule = screen
			.getByRole("heading", { name: "Sync schedule" })
			.closest("section") as HTMLElement;
		expect(
			await within(schedule).findByText(
				"Syncs every 6 hours; you can also sync now.",
			),
		).toBeVisible();
	});

	it("never shows the organization's ID or how the host fires a schedule", async () => {
		renderInApp(<DatasourcesAdmin />);
		fireEvent.click(await screen.findByRole("button", { name: "Advanced" }));
		await screen.findByRole("heading", { name: "Sync schedule" });
		const text = document.body.textContent ?? "";
		expect(text).not.toContain(ORG);
		expect(text).not.toMatch(/stopgap|effect identity|reconcil/i);
	});
});
