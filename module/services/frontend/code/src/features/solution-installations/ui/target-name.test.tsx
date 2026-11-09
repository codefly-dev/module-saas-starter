import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { TargetName } from "./target-name";

afterEach(cleanup);

describe("TargetName", () => {
	// Admin listed route aliases ("expenses") while the sidebar said the
	// title; it says the title, with the alias the teams are named after beside it.
	it("names a target by the title the person's menu gives it, alias beside", () => {
		render(
			<TargetName
				alias="expenses"
				titles={new Map([["expenses", "Expense Reports"]])}
			/>,
		);
		expect(screen.getByText("Expense Reports")).toBeTruthy();
		expect(screen.getByText("expenses")).toBeTruthy();
	});

	it("keeps the alias for a target the person has no menu entry for", () => {
		const { container } = render(
			<TargetName alias="ledger" titles={new Map()} />,
		);
		expect(container.textContent).toBe("ledger");
	});
});
