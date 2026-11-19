import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp } from "@/test/container";
import type { DashboardRecord } from "../../model/record";
import { dashboard } from "../../model/schema";
import {
	createMemoryDashboardLibrary,
	type DashboardLibrary,
} from "../../service/dashboard-library";
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

		await waitFor(() =>
			expect((screen.getByLabelText("Name") as HTMLInputElement).disabled).toBe(
				false,
			),
		);
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
	it("does not present a failed read as an empty library", async () => {
		const library: DashboardLibrary = {
			...createMemoryDashboardLibrary(),
			list: () => {
				throw new Error("private-storage-diagnostic");
			},
		};
		renderInApp(<MyDashboards library={library} />);
		expect(
			await screen.findByText(/Couldn't view your dashboards/),
		).toBeTruthy();
		expect(screen.queryByText(/No dashboards yet/)).toBeNull();
		expect(screen.queryByText(/private-storage-diagnostic/)).toBeNull();
	});

	it("shows no empty state while a library read is pending", async () => {
		let resolve!: (records: DashboardRecord[]) => void;
		const library: DashboardLibrary = {
			...createMemoryDashboardLibrary(),
			list: () =>
				new Promise((done) => {
					resolve = done;
				}),
		};
		renderInApp(<MyDashboards library={library} />);
		await waitFor(() => expect(resolve).toBeTypeOf("function"));
		expect(screen.queryByText(/No dashboards yet/)).toBeNull();
		resolve([]);
		expect(await screen.findByText(/No dashboards yet/)).toBeTruthy();
	});

	it("keeps the name after a failed create", async () => {
		const library: DashboardLibrary = {
			...createMemoryDashboardLibrary(),
			create: () => {
				throw new Error("private-failure");
			},
		};
		renderInApp(<MyDashboards library={library} />);
		await screen.findByText(/No dashboards yet/);
		fireEvent.change(screen.getByLabelText("Name"), {
			target: { value: "Weekly activity" },
		});
		fireEvent.click(screen.getByRole("button", { name: "Create" }));
		await screen.findByText(/Couldn't save the dashboard change/);
		expect((screen.getByLabelText("Name") as HTMLInputElement).value).toBe(
			"Weekly activity",
		);
		expect(screen.queryByText(/private-failure/)).toBeNull();
	});

	it("names a dashboard in its confirmation and keeps a failed deletion open", async () => {
		const remove = vi.fn().mockRejectedValue(new Error("private-delete"));
		const library = { ...createMemoryDashboardLibrary([record({})]), remove };
		renderInApp(<MyDashboards library={library} />);
		await screen.findByText("Sales");
		fireEvent.click(
			screen.getByRole("button", { name: 'More actions for "Sales"' }),
		);
		fireEvent.click(await screen.findByRole("menuitem", { name: "Delete" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog.textContent).toContain("Sales");
		expect(remove).not.toHaveBeenCalled();
		fireEvent.click(within(dialog).getByRole("button", { name: "Delete" }));
		await within(dialog).findByText(/This change could not be saved/);
		expect(remove).toHaveBeenCalledOnce();
		expect(dialog.textContent).not.toContain("private-delete");
	});
});
