import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Tabs } from "../tabs.js";

afterEach(cleanup);

const tabs = [
	{ id: "one", label: "One", content: <p>First panel</p> },
	{ id: "two", label: "Two", content: <p>Second panel</p> },
	{ id: "three", label: "Three", content: <p>Third panel</p> },
];

describe("Tabs", () => {
	it("renders the ARIA tablist/tab/tabpanel roles", () => {
		render(<Tabs tabs={tabs} />);
		expect(screen.getByRole("tablist")).toBeTruthy();
		expect(screen.getAllByRole("tab")).toHaveLength(3);
		expect(screen.getByRole("tabpanel")).toBeTruthy();
	});

	it("selects the first tab by default and wires aria-selected", () => {
		render(<Tabs tabs={tabs} />);
		const [first, second] = screen.getAllByRole("tab");
		expect(first.getAttribute("aria-selected")).toBe("true");
		expect(second.getAttribute("aria-selected")).toBe("false");
		expect(screen.getByRole("tabpanel").textContent).toBe("First panel");
	});

	it("honors `initial` for the uncontrolled starting tab", () => {
		render(<Tabs tabs={tabs} initial="two" />);
		expect(screen.getByRole("tabpanel").textContent).toBe("Second panel");
	});

	it("switches panels on click and reports the change", () => {
		const onChange = vi.fn();
		render(<Tabs tabs={tabs} onChange={onChange} />);
		fireEvent.click(screen.getByRole("tab", { name: "Two" }));
		expect(onChange).toHaveBeenCalledWith("two");
		expect(screen.getByRole("tabpanel").textContent).toBe("Second panel");
	});

	it("stays fixed when controlled and only reports intent", () => {
		const onChange = vi.fn();
		render(<Tabs tabs={tabs} active="one" onChange={onChange} />);
		fireEvent.click(screen.getByRole("tab", { name: "Three" }));
		expect(onChange).toHaveBeenCalledWith("three");
		// Controlled: selection does not move until the parent updates `active`.
		expect(screen.getByRole("tabpanel").textContent).toBe("First panel");
	});

	it("moves selection with Arrow/Home/End keys", () => {
		render(<Tabs tabs={tabs} />);
		const tablist = screen.getByRole("tablist");
		const [first] = screen.getAllByRole("tab");
		first.focus();

		fireEvent.keyDown(first, { key: "ArrowRight" });
		expect(screen.getByRole("tabpanel").textContent).toBe("Second panel");

		fireEvent.keyDown(document.activeElement ?? tablist, { key: "End" });
		expect(screen.getByRole("tabpanel").textContent).toBe("Third panel");

		fireEvent.keyDown(document.activeElement ?? tablist, { key: "ArrowRight" });
		// Wraps back to the first tab.
		expect(screen.getByRole("tabpanel").textContent).toBe("First panel");

		fireEvent.keyDown(document.activeElement ?? tablist, { key: "Home" });
		expect(screen.getByRole("tabpanel").textContent).toBe("First panel");

		fireEvent.keyDown(document.activeElement ?? tablist, { key: "ArrowLeft" });
		// Wraps to the last tab.
		expect(screen.getByRole("tabpanel").textContent).toBe("Third panel");
	});

	it("keeps only the selected tab in the tab order (roving tabindex)", () => {
		render(<Tabs tabs={tabs} initial="two" />);
		const [first, second, third] = screen.getAllByRole("tab");
		expect(first.getAttribute("tabindex")).toBe("-1");
		expect(second.getAttribute("tabindex")).toBe("0");
		expect(third.getAttribute("tabindex")).toBe("-1");
	});
});
