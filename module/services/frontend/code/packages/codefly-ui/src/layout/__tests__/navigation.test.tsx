// @vitest-environment happy-dom
import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
	Accordion,
	AccordionContent,
	AccordionHeader,
	AccordionItem,
	AccordionTrigger,
} from "../accordion.js";
import { Breadcrumb } from "../breadcrumb.js";
import { Disclosure } from "../disclosure.js";
import { Label } from "../label.js";
import { Radio, RadioGroup } from "../radio-group.js";
import { Surface } from "../surface.js";
import { Tree, type TreeNode } from "../tree.js";

afterEach(cleanup);
const nodes: TreeNode[] = [
	{
		id: "a",
		label: "Group A",
		textValue: "Group A",
		children: [
			{ id: "a1", label: "Alpha", textValue: "Alpha" },
			{ id: "a2", label: "Beta", textValue: "Beta", disabled: true },
		],
	},
	{ id: "b", label: "Group B", textValue: "Group B", hasChildren: true },
];
const activeText = (tree: HTMLElement) =>
	document
		.getElementById(tree.getAttribute("aria-activedescendant") ?? "")
		?.getAttribute("aria-label");

it("disclosure exposes expansion and lets a controlled owner refuse a change", async () => {
	const change = vi.fn();
	const view = render(
		<Disclosure title="Details" open={false} onOpenChange={change}>
			Evidence
		</Disclosure>,
	);
	fireEvent.click(screen.getByRole("button", { name: "Details" }));
	expect(change).toHaveBeenCalledWith(true, expect.anything());
	expect(screen.getByRole("button").getAttribute("aria-expanded")).toBe(
		"false",
	);
	view.rerender(
		<Disclosure title="Details" open onOpenChange={change}>
			Evidence
		</Disclosure>,
	);
	expect(screen.getByRole("button").getAttribute("aria-expanded")).toBe("true");
	expect(screen.getByText("Evidence")).toBeTruthy();
});

it("an accordion opens one item at a time by default", async () => {
	render(
		<Accordion defaultValue={["one"]}>
			{["one", "two"].map((value) => (
				<AccordionItem key={value} value={value}>
					<AccordionHeader>
						<AccordionTrigger>{value}</AccordionTrigger>
					</AccordionHeader>
					<AccordionContent>{value} body</AccordionContent>
				</AccordionItem>
			))}
		</Accordion>,
	);
	fireEvent.click(screen.getByRole("button", { name: "two" }));
	await waitFor(() =>
		expect(
			screen.getByRole("button", { name: "two" }).getAttribute("aria-expanded"),
		).toBe("true"),
	);
	expect(
		screen.getByRole("button", { name: "one" }).getAttribute("aria-expanded"),
	).toBe("false");
});

it("breadcrumbs navigate ancestors while keeping the current item inert", () => {
	const navigate = vi.fn();
	render(
		<Breadcrumb
			items={[
				{ id: "a", label: "Parent", onNavigate: navigate },
				{ id: "b", label: "Current", href: "/current", onNavigate: navigate },
			]}
		/>,
	);
	fireEvent.click(screen.getByRole("button", { name: "Parent" }));
	expect(navigate).toHaveBeenCalledOnce();
	expect(screen.queryByRole("link", { name: "Current" })).toBeNull();
	expect(screen.getByText("Current").getAttribute("aria-current")).toBe("page");
});

it("radio selection contributes its form value and refuses disabled choices", async () => {
	render(
		<form aria-label="Options">
			<RadioGroup name="choice" defaultValue="a" aria-label="Choice">
				<Label>
					<Radio value="a" />
					Alpha
				</Label>
				<Label>
					<Radio value="b" />
					Beta
				</Label>
				<Label>
					<Radio value="c" disabled />
					Unavailable
				</Label>
			</RadioGroup>
		</form>,
	);
	fireEvent.click(screen.getByRole("radio", { name: "Beta" }));
	await waitFor(() =>
		expect(
			screen.getByRole("radio", { name: "Beta" }).getAttribute("aria-checked"),
		).toBe("true"),
	);
	expect(
		new FormData(screen.getByRole("form") as HTMLFormElement).get("choice"),
	).toBe("b");
	fireEvent.click(screen.getByRole("radio", { name: "Unavailable" }));
	expect(
		new FormData(screen.getByRole("form") as HTMLFormElement).get("choice"),
	).toBe("b");
});

it("a surface imposes no layout or padding on its contents", () => {
	render(<Surface data-testid="surface">Content</Surface>);
	expect(screen.getByTestId("surface").className).not.toMatch(
		/\b(?:flex|grid|p-\d|py-\d|gap-\d|shadow)/,
	);
});

describe("Tree", () => {
	it("navigates hierarchy without selecting until requested, and preserves disabled semantics", () => {
		const select = vi.fn();
		render(<Tree items={nodes} label="Items" onSelect={select} />);
		const tree = screen.getByRole("tree");
		tree.focus();
		fireEvent.keyDown(tree, { key: "ArrowRight" });
		fireEvent.keyDown(tree, { key: "ArrowRight" });
		expect(activeText(tree)).toBe("Alpha");
		expect(select).not.toHaveBeenCalled();
		expect(
			screen
				.getByRole("treeitem", { name: "Alpha" })
				.getAttribute("aria-level"),
		).toBe("2");
		fireEvent.keyDown(tree, { key: "Enter" });
		expect(select).toHaveBeenCalledWith(nodes[0].children?.[0]);
		fireEvent.keyDown(tree, { key: "ArrowDown" });
		fireEvent.keyDown(tree, { key: " " });
		expect(select).toHaveBeenCalledOnce();
		fireEvent.keyDown(tree, { key: "ArrowLeft" });
		expect(activeText(tree)).toBe("Group A");
		fireEvent.keyDown(tree, { key: "ArrowLeft" });
		expect(screen.queryByRole("treeitem", { name: "Alpha" })).toBeNull();
	});
	it("supports home, end and typeahead", () => {
		render(<Tree items={nodes} label="Items" defaultExpandedIds={["a"]} />);
		const tree = screen.getByRole("tree");
		fireEvent.keyDown(tree, { key: "End" });
		expect(activeText(tree)).toBe("Group B");
		fireEvent.keyDown(tree, { key: "Home" });
		expect(activeText(tree)).toBe("Group A");
		fireEvent.keyDown(tree, { key: "b" });
		expect(activeText(tree)).toBe("Beta");
	});
	it("requests lazy children once per expansion and honors controlled expansion", () => {
		const load = vi.fn(),
			change = vi.fn();
		const view = render(
			<Tree
				items={nodes}
				label="Items"
				expandedIds={[]}
				onExpandedChange={change}
				onLoadChildren={load}
			/>,
		);
		fireEvent.click(screen.getByRole("button", { name: "Expand Group B" }));
		expect(load).toHaveBeenCalledWith(nodes[1]);
		expect(change).toHaveBeenCalledWith(["b"]);
		expect(
			screen
				.getByRole("treeitem", { name: "Group B" })
				.getAttribute("aria-expanded"),
		).toBe("false");
		view.rerender(
			<Tree
				items={nodes}
				label="Items"
				expandedIds={["b"]}
				onExpandedChange={change}
				onLoadChildren={load}
			/>,
		);
		fireEvent.keyDown(screen.getByRole("tree"), { key: "ArrowRight" });
		expect(load).toHaveBeenCalledOnce();
	});
	it("returns focus to the ancestor when a controlled branch collapses", () => {
		const view = render(
			<Tree items={nodes} label="Items" expandedIds={["a"]} selectedId="a1" />,
		);
		const tree = screen.getByRole("tree");
		fireEvent.keyDown(tree, { key: "ArrowDown" });
		expect(activeText(tree)).toBe("Beta");
		view.rerender(
			<Tree items={nodes} label="Items" expandedIds={[]} selectedId="a1" />,
		);
		expect(activeText(tree)).toBe("Group A");
	});
	it("keeps the active descendant mounted through keyboard navigation and pointer scrolling", () => {
		const many = Array.from({ length: 100 }, (_, n) => ({
			id: String(n),
			textValue: `Item ${n}`,
			label: `Item ${n}`,
		}));
		const view = render(
			<Tree
				items={many}
				label="Items"
				virtualize={{ height: 100, rowHeight: 20, overscan: 1 }}
			/>,
		);
		const tree = screen.getByRole("tree");
		expect(screen.getAllByRole("treeitem").length).toBeLessThan(10);
		fireEvent.keyDown(tree, { key: "End" });
		expect(activeText(tree)).toBe("Item 99");
		expect(tree.scrollTop).toBe(1900);
		fireEvent.scroll(tree, { target: { scrollTop: 0 } });
		expect(activeText(tree)).toBe("Item 99");
		fireEvent.keyDown(tree, { key: "Home" });
		expect(activeText(tree)).toBe("Item 0");
		fireEvent.scroll(tree, { target: { scrollTop: 1900 } });
		view.rerender(
			<Tree
				items={many.slice(0, 2)}
				label="Items"
				virtualize={{ height: 100, rowHeight: 20 }}
			/>,
		);
		expect(tree.scrollTop).toBe(0);
		expect(screen.getAllByRole("treeitem")).toHaveLength(2);
	});
});

it("an external focus request reveals a distant row without selecting or stealing focus", () => {
	const select = vi.fn();
	const many = Array.from({ length: 100 }, (_, n) => ({
		id: String(n),
		textValue: `Item ${n}`,
		label: `Item ${n}`,
	}));
	const view = render(
		<Tree
			items={many}
			label="Items"
			selectedId="0"
			onSelect={select}
			virtualize={{ height: 100, rowHeight: 20 }}
		/>,
	);
	const before = document.activeElement;
	view.rerender(
		<Tree
			items={many}
			label="Items"
			selectedId="0"
			focusedId="95"
			onSelect={select}
			virtualize={{ height: 100, rowHeight: 20 }}
		/>,
	);
	const tree = screen.getByRole("tree");
	expect(activeText(tree)).toBe("Item 95");
	expect(tree.scrollTop).toBe(1820);
	expect(document.activeElement).toBe(before);
	expect(select).not.toHaveBeenCalled();
	expect(
		screen
			.getByRole("treeitem", { name: "Item 95" })
			.getAttribute("aria-selected"),
	).toBe("false");
});

it("retained disclosure content preserves an uncontrolled draft through collapse", async () => {
	render(
		<Disclosure title="Options" defaultOpen keepMounted>
			<input aria-label="Draft" defaultValue="" />
		</Disclosure>,
	);
	const input = screen.getByRole("textbox", { name: "Draft" });
	fireEvent.change(input, { target: { value: "incomplete draft" } });
	fireEvent.click(screen.getByRole("button", { name: "Options" }));
	expect(input.isConnected).toBe(true);
	fireEvent.click(screen.getByRole("button", { name: "Options" }));
	await waitFor(() =>
		expect(screen.getByRole("textbox", { name: "Draft" })).toBe(input),
	);
	expect((input as HTMLInputElement).value).toBe("incomplete draft");
});
