// @vitest-environment happy-dom
import {
	cleanup,
	fireEvent,
	render,
	screen,
	within,
} from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { EmptySummary, SelectableSummary } from "./semantic-table.stories";

afterEach(cleanup);

it("keeps checkbox selection and the table summary in sync", () => {
	render(<SelectableSummary.render />);
	const table = screen.getByRole("table", {
		name: "Workspace membership — 1 selected",
	});
	const acme = within(table).getByRole("checkbox", { name: "Select Acme" });
	expect(acme.getAttribute("aria-checked")).toBe("true");
	fireEvent.click(acme);
	expect(acme.getAttribute("aria-checked")).toBe("false");
	expect(
		screen.getByRole("table", { name: "Workspace membership — 0 selected" }),
	).toBe(table);
	const example = within(table).getByRole("checkbox", {
		name: "Select ExampleCorp",
	});
	fireEvent.click(example);
	expect(example.getAttribute("aria-checked")).toBe("true");
	expect(
		screen.getByRole("table", { name: "Workspace membership — 1 selected" }),
	).toBe(table);
	expect(
		within(table).getByRole("rowheader", { name: "Total members" }),
	).toBeTruthy();
});

it("keeps column labels and an explicit empty result", () => {
	render(<EmptySummary.render />);
	const table = screen.getByRole("table", { name: "Workspace membership" });
	expect(within(table).getAllByRole("columnheader")).toHaveLength(2);
	expect(
		within(table)
			.getByRole("cell", { name: "No workspaces found." })
			.getAttribute("colspan"),
	).toBe("2");
	expect(within(table).queryByRole("checkbox")).toBeNull();
});
