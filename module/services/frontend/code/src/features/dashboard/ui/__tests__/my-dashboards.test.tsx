import { fireEvent, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp } from "@/test/container";
import type { DashboardRecord } from "../../model/record";
import { dashboard } from "../../model/schema";
import { createMemoryDashboardLibrary } from "../../service/dashboard-library";
import { MyDashboards } from "../my-dashboards";

vi.mock("@/lib/auth", () => ({
	useAuth: () => ({ organizationId: "org-1", user: { id: "u-1" } }),
}));

// Stub the editor: this surface owns the collection and the open/back flow;
// the editor has its own coverage. The stub echoes the opened spec's title so a
// test can assert the right dashboard opened.
vi.mock("../dashboard-editor", () => ({
	DashboardEditor: ({ initial }: { initial: { title?: string } }) => (
		<div data-testid="editor">Editing {initial.title}</div>
	),
}));

function record(overrides: Partial<DashboardRecord>): DashboardRecord {
	return {
		id: "d-1",
		name: "Sales",
		spec: dashboard({ title: "Sales", metrics: [] }),
		visibility: "private",
		createdAt: "2026-08-31T00:00:00.000Z",
		updatedAt: "2026-08-31T00:00:00.000Z",
		...overrides,
	};
}

afterEach(() => vi.clearAllMocks());

describe("MyDashboards", () => {
	it("shows an empty state with nothing stored", async () => {
		renderInApp(<MyDashboards library={createMemoryDashboardLibrary()} />);
		expect(await screen.findByText(/No dashboards yet/)).toBeTruthy();
	});

	it("creates a named dashboard and opens it in the editor", async () => {
		renderInApp(<MyDashboards library={createMemoryDashboardLibrary()} />);

		fireEvent.change(screen.getByLabelText("Name"), {
			target: { value: "Weekly activity" },
		});
		fireEvent.click(screen.getByRole("button", { name: /Create/ }));

		expect((await screen.findByTestId("editor")).textContent).toContain(
			"Weekly activity",
		);

		fireEvent.click(screen.getByRole("button", { name: /All dashboards/ }));
		expect(await screen.findByText("Weekly activity")).toBeTruthy();
	});

	it("lists a stored dashboard and opens it", async () => {
		const library = createMemoryDashboardLibrary([
			record({
				name: "Sales",
				spec: dashboard({ title: "Sales", metrics: [] }),
			}),
		]);
		renderInApp(<MyDashboards library={library} />);

		expect(await screen.findByText("Sales")).toBeTruthy();
		fireEvent.click(screen.getByRole("button", { name: "Open" }));
		expect((await screen.findByTestId("editor")).textContent).toContain(
			"Sales",
		);
	});

	it("marks a shared dashboard with a Shared badge", async () => {
		const library = createMemoryDashboardLibrary([
			record({ name: "Org wide", visibility: "org" }),
		]);
		renderInApp(<MyDashboards library={library} />);
		expect(await screen.findByText("Shared")).toBeTruthy();
		expect(screen.queryByText("Private")).toBeNull();
	});
});
