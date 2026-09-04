// @vitest-environment happy-dom
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { EmptyState } from "../empty-state.js";
import { ErrorState } from "../error-state.js";

afterEach(cleanup);

describe("EmptyState", () => {
	it("renders heading, description, and an action slot", () => {
		render(
			<EmptyState heading="No documents yet" description="Add one to begin">
				<button type="button">Add document</button>
			</EmptyState>,
		);
		expect(screen.getByRole("heading", { name: "No documents yet" })).toBeTruthy();
		expect(screen.getByText("Add one to begin")).toBeTruthy();
		expect(screen.getByRole("button", { name: "Add document" })).toBeTruthy();
	});

	it("paints a default icon so the block always has one", () => {
		const { container } = render(<EmptyState heading="Empty" />);
		expect(
			container.querySelector('[data-slot="empty-state-icon"] svg'),
		).toBeTruthy();
	});

	it("uses a caller-supplied icon over the default", () => {
		render(<EmptyState icon={<span data-testid="glyph" />} heading="Empty" />);
		expect(screen.getByTestId("glyph")).toBeTruthy();
	});

	it("omits the description paragraph when none is given", () => {
		const { container } = render(<EmptyState heading="Empty" />);
		expect(container.querySelector("p")).toBeNull();
	});
});

describe("ErrorState", () => {
	it("exposes the title through the alert role with a muted detail", () => {
		render(
			<ErrorState title="Request failed" detail="gateway timed out" />,
		);
		const alert = screen.getByRole("alert");
		expect(alert.textContent).toContain("Request failed");
		expect(alert.textContent).toContain("gateway timed out");
	});

	it("renders the title in destructive and the detail in muted", () => {
		render(<ErrorState title="Request failed" detail="gateway timed out" />);
		expect(screen.getByText("Request failed").className).toContain(
			"text-destructive",
		);
		expect(screen.getByText("gateway timed out").className).toContain(
			"text-muted-foreground",
		);
	});

	it("omits the detail paragraph when none is given", () => {
		render(<ErrorState title="Request failed" />);
		expect(screen.queryByText("gateway timed out")).toBeNull();
		expect(screen.getByText("Request failed")).toBeTruthy();
	});
});
