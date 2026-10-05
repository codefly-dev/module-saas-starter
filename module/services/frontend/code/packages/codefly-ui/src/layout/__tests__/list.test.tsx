// @vitest-environment happy-dom
import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { DescriptionList, List, ListItem } from "../list.js";

afterEach(cleanup);

describe("List", () => {
	it("is a list of its items", () => {
		render(
			<List label="Parsing notes">
				<ListItem>Front matter has no owner</ListItem>
				<ListItem>Two headings share a level</ListItem>
			</List>,
		);
		const list = screen.getByRole("list", { name: "Parsing notes" });
		expect(within(list).getAllByRole("listitem")).toHaveLength(2);
	});

	it("rules lines only when divided", () => {
		const { container, rerender } = render(
			<List>
				<ListItem>One</ListItem>
			</List>,
		);
		const list = () => container.querySelector("[data-slot=list]");
		expect(list()?.className).not.toContain("divide-y");
		rerender(
			<List variant="divided">
				<ListItem>One</ListItem>
			</List>,
		);
		expect(list()?.getAttribute("data-variant")).toBe("divided");
		expect(list()?.className).toContain("divide-y");
	});
});

describe("ListItem", () => {
	it("renders every slot it is given, in reading order", () => {
		const { container } = render(
			<List>
				<ListItem
					icon={<svg />}
					description="Line 12 of the source"
					meta="2 min ago"
					actions={<button type="button">Open</button>}
				>
					Heading level skipped
				</ListItem>
			</List>,
		);
		const slots = [
			...container.querySelectorAll("[data-slot^=list-item-]"),
		].map((element) => element.getAttribute("data-slot"));
		expect(slots).toEqual([
			"list-item-icon",
			"list-item-title",
			"list-item-description",
			"list-item-meta",
			"list-item-actions",
		]);
		expect(
			container
				.querySelector("[data-slot=list-item-icon]")
				?.getAttribute("aria-hidden"),
		).toBe("true");
		expect(screen.getByRole("button", { name: "Open" })).toBeTruthy();
	});

	it("renders only the title when nothing else is given", () => {
		const { container } = render(
			<List>
				<ListItem>Just a row</ListItem>
			</List>,
		);
		expect(
			[...container.querySelectorAll("[data-slot^=list-item-]")].map((e) =>
				e.getAttribute("data-slot"),
			),
		).toEqual(["list-item-title"]);
	});
});

describe("DescriptionList", () => {
	const items = [
		{ term: "Status", value: "Draft" },
		{ term: "Owner", value: "Jane Doe" },
	];

	it("pairs each term with its value", () => {
		const { container } = render(<DescriptionList items={items} />);
		const terms = [...container.querySelectorAll("dt")].map(
			(e) => e.textContent,
		);
		const values = [...container.querySelectorAll("dd")].map(
			(e) => e.textContent,
		);
		expect(terms).toEqual(["Status", "Owner"]);
		expect(values).toEqual(["Draft", "Jane Doe"]);
		// Each dd follows its own dt inside one group.
		for (const group of container.querySelectorAll(
			"[data-slot=description-item]",
		))
			expect(
				[...group.children].map((child) => child.tagName.toLowerCase()),
			).toEqual(["dt", "dd"]);
	});

	it("aligns pairs in two columns inline and stacks them otherwise", () => {
		const { container, rerender } = render(<DescriptionList items={items} />);
		const dl = () => container.querySelector("dl");
		const group = () =>
			container.querySelector("[data-slot=description-item]");
		expect(dl()?.getAttribute("data-layout")).toBe("inline");
		expect(group()?.className).toContain("contents");
		rerender(<DescriptionList items={items} layout="stacked" />);
		expect(dl()?.getAttribute("data-layout")).toBe("stacked");
		expect(group()?.className).not.toContain("contents");
	});

	it("takes nodes as values and an explicit key for a non-string term", () => {
		render(
			<DescriptionList
				items={[
					{ key: "source", term: <span>Source</span>, value: <a href="#source">s</a> },
				]}
			/>,
		);
		expect(screen.getByRole("link", { name: "s" })).toBeTruthy();
	});
});
