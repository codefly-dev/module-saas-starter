// @vitest-environment happy-dom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
	Banner,
	Breadcrumbs,
	EmptyState,
	ErrorState,
	PageHeader,
	Pagination,
	Spinner,
	StatTile,
} from "../index.js";

afterEach(cleanup);

describe("StatTile", () => {
	it("shows a down arrow for a negative delta even when toned positive", () => {
		const { container } = render(
			<StatTile label="Failed" value="27" delta="-8.0%" deltaTone="positive" />,
		);
		expect(screen.getByText("-8.0%")).toBeTruthy();
		// positive tone → chart-2 color class present; arrow follows the sign (down).
		const badge = screen.getByText("-8.0%");
		expect(badge.className).toContain("chart-2");
		expect(container.querySelector(".lucide-arrow-down")).toBeTruthy();
		expect(container.querySelector(".lucide-arrow-up")).toBeNull();
	});

	it("defaults a plain positive delta to an up arrow", () => {
		const { container } = render(
			<StatTile label="Total" value="1,284" delta="+12%" />,
		);
		expect(container.querySelector(".lucide-arrow-up")).toBeTruthy();
	});
});

describe("Banner", () => {
	it("renders its tone icon, title, and actions", () => {
		render(
			<Banner
				tone="warning"
				title="Heads up"
				actions={<button type="button">Fix</button>}
			>
				body copy
			</Banner>,
		);
		expect(screen.getByText("Heads up")).toBeTruthy();
		expect(screen.getByText("body copy")).toBeTruthy();
		expect(screen.getByRole("button", { name: "Fix" })).toBeTruthy();
	});
});

describe("EmptyState / ErrorState", () => {
	it("EmptyState renders title, description, actions", () => {
		render(
			<EmptyState
				title="No sessions"
				description="Nothing yet"
				actions={<button type="button">Add</button>}
			/>,
		);
		expect(screen.getByText("No sessions")).toBeTruthy();
		expect(screen.getByText("Nothing yet")).toBeTruthy();
	});
	it("ErrorState has an alert role and a default title", () => {
		render(<ErrorState description="boom" />);
		expect(screen.getByRole("alert")).toBeTruthy();
		expect(screen.getByText("Something went wrong")).toBeTruthy();
	});
});

describe("Spinner", () => {
	it("exposes an accessible label", () => {
		render(<Spinner label="Loading sessions" />);
		expect(screen.getByRole("status")).toBeTruthy();
		expect(screen.getByText("Loading sessions")).toBeTruthy();
	});
});

describe("Breadcrumbs", () => {
	it("marks the last crumb as the current page and links the rest", () => {
		render(
			<Breadcrumbs items={[{ label: "Home", href: "/" }, { label: "Now" }]} />,
		);
		expect(screen.getByRole("link", { name: "Home" })).toBeTruthy();
		const current = screen.getByText("Now");
		expect(current.getAttribute("aria-current")).toBe("page");
	});
});

describe("PageHeader", () => {
	it("renders title, breadcrumbs, and actions", () => {
		render(
			<PageHeader
				title="Login activity"
				breadcrumbs={[
					{ label: "Home", href: "/" },
					{ label: "Login activity" },
				]}
				actions={<button type="button">Export</button>}
			/>,
		);
		expect(
			screen.getByRole("heading", { name: "Login activity" }),
		).toBeTruthy();
		expect(screen.getByRole("navigation", { name: "Breadcrumb" })).toBeTruthy();
		expect(screen.getByRole("button", { name: "Export" })).toBeTruthy();
	});
});

describe("Pagination", () => {
	it("renders nothing for a single page", () => {
		const { container } = render(<Pagination page={1} pageCount={1} />);
		expect(container.querySelector("[data-slot=pagination]")).toBeNull();
	});

	it("disables Previous on the first page and pages on click", () => {
		const onPageChange = vi.fn();
		render(<Pagination page={1} pageCount={5} onPageChange={onPageChange} />);
		expect(
			(
				screen.getByRole("button", {
					name: "Previous page",
				}) as HTMLButtonElement
			).disabled,
		).toBe(true);
		fireEvent.click(screen.getByRole("button", { name: "Page 2" }));
		expect(onPageChange).toHaveBeenCalledWith(2);
	});

	it("collapses long ranges with a gap", () => {
		render(<Pagination page={10} pageCount={20} />);
		expect(screen.getByRole("button", { name: "Page 1" })).toBeTruthy();
		expect(screen.getByRole("button", { name: "Page 20" })).toBeTruthy();
		expect(screen.queryByRole("button", { name: "Page 5" })).toBeNull();
	});
});
