import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import {
	Grid,
	Layout,
	Page,
	PageHeader,
	Panel,
	Section,
	Stack,
} from "./layout";

describe("layout primitives", () => {
	it("composes Layout > Page > PageHeader", () => {
		render(
			<Layout data-testid="layout">
				<Page data-testid="page">
					<PageHeader
						title="Users"
						description="Manage your team"
						actions={<button type="button">Invite</button>}
					/>
				</Page>
			</Layout>,
		);
		const layout = screen.getByTestId("layout");
		expect(layout.className).toContain("max-w-7xl");
		// Layout must not add its own gutters — the route shell owns padding, and
		// re-applying it here double-pads the documented composition.
		expect(layout.className).not.toContain("px-4");
		expect(screen.getByTestId("page").className).toContain("space-y-6");
		expect(screen.getByRole("heading", { name: "Users" })).toBeTruthy();
		expect(screen.getByText("Manage your team")).toBeTruthy();
		expect(screen.getByRole("button", { name: "Invite" })).toBeTruthy();
	});

	it("omits PageHeader description and actions when not provided", () => {
		render(<PageHeader title="Only title" />);
		expect(screen.getByRole("heading", { name: "Only title" })).toBeTruthy();
		expect(screen.queryByRole("button")).toBeNull();
	});

	it("accepts a ReactNode title in PageHeader and Section", () => {
		render(
			<>
				<PageHeader title={<span data-testid="ph-title">Users</span>} />
				<Section title={<span data-testid="sec-title">Details</span>}>
					<div>body</div>
				</Section>
			</>,
		);
		expect(screen.getByTestId("ph-title").textContent).toBe("Users");
		expect(screen.getByTestId("sec-title").textContent).toBe("Details");
	});

	it("maps Grid cols and gap to responsive classes", () => {
		render(
			<Grid cols={3} gap={6} data-testid="grid">
				<div>a</div>
			</Grid>,
		);
		const grid = screen.getByTestId("grid");
		expect(grid.className).toContain("grid-cols-1");
		expect(grid.className).toContain("sm:grid-cols-2");
		expect(grid.className).toContain("lg:grid-cols-3");
		expect(grid.className).toContain("gap-6");
	});

	it("switches Stack direction and applies alignment", () => {
		render(
			<Stack
				direction="row"
				align="center"
				justify="between"
				data-testid="stack"
			>
				<div>a</div>
			</Stack>,
		);
		const stack = screen.getByTestId("stack");
		expect(stack.className).toContain("flex-row");
		expect(stack.className).toContain("items-center");
		expect(stack.className).toContain("justify-between");
	});

	it("defaults Stack to a column", () => {
		render(<Stack data-testid="stack" />);
		expect(screen.getByTestId("stack").className).toContain("flex-col");
	});

	it("renders Section header only when heading content is present", () => {
		const { rerender } = render(
			<Section data-testid="section">
				<div>body</div>
			</Section>,
		);
		expect(screen.queryByRole("heading")).toBeNull();

		rerender(
			<Section title="Settings" data-testid="section">
				<div>body</div>
			</Section>,
		);
		expect(screen.getByRole("heading", { name: "Settings" })).toBeTruthy();
	});

	it("renders Panel as a bordered surface and preserves custom classes", () => {
		render(
			<Panel className="custom-class" data-testid="panel">
				content
			</Panel>,
		);
		const panel = screen.getByTestId("panel");
		expect(panel.className).toContain("rounded-lg");
		expect(panel.className).toContain("border");
		expect(panel.className).toContain("custom-class");
	});
});
