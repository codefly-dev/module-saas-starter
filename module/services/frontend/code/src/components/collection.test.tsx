import {
	cleanup,
	fireEvent,
	render,
	screen,
	within,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
	Collection,
	type CollectionData,
	type CollectionDocument,
	type CollectionView,
} from "./collection";

afterEach(cleanup);

// One hierarchical collection, rendered three ways by three descriptors.
const documents: CollectionDocument[] = [
	{
		id: "src",
		label: "src",
		facets: { kind: "folder", status: "clean" },
		children: [
			{
				id: "src/app.ts",
				label: "app.ts",
				facets: { kind: "file", status: "modified" },
			},
			{
				id: "src/util.ts",
				label: "util.ts",
				facets: { kind: "file", status: "clean" },
			},
		],
	},
	{
		id: "readme",
		label: "README.md",
		facets: { kind: "file", status: "modified" },
	},
];

function renderCollection(
	view: CollectionView,
	data?: Partial<CollectionData>,
) {
	return render(<Collection data={{ documents, ...data }} view={view} />);
}

describe("Collection renderer", () => {
	it("renders the collection as a tree, revealing children on expand", () => {
		renderCollection({ type: "tree" });
		// Roots show; nested children stay hidden until their parent expands.
		expect(screen.getByText("src")).toBeTruthy();
		expect(screen.getByText("README.md")).toBeTruthy();
		expect(screen.queryByText("app.ts")).toBeNull();

		fireEvent.click(screen.getByRole("button", { name: "Expand" }));
		expect(screen.getByText("app.ts")).toBeTruthy();
		expect(screen.getByText("util.ts")).toBeTruthy();
	});

	it("renders the same data as a table with facets as columns", () => {
		renderCollection({
			type: "table",
			columns: [
				{ facet: "label", header: "Name" },
				{ facet: "status", header: "Status" },
			],
		});
		expect(screen.getByRole("columnheader", { name: "Name" })).toBeTruthy();
		expect(screen.getByRole("columnheader", { name: "Status" })).toBeTruthy();
		// The table is flat, so every node — nested included — is a row.
		expect(screen.getByText("app.ts")).toBeTruthy();
		expect(screen.getAllByText("modified").length).toBe(2);
	});

	it("renders the same data as a gallery grouped by a facet", () => {
		renderCollection({ type: "gallery", groupBy: "status" });
		// A board's heading sits in a header div; the cards are its siblings.
		const modified = screen.getByText("modified").closest("div")?.parentElement;
		const clean = screen.getByText("clean").closest("div")?.parentElement;
		expect(modified).toBeTruthy();
		expect(clean).toBeTruthy();
		// app.ts + README.md are "modified"; util.ts + src are "clean".
		expect(within(modified as HTMLElement).getByText("app.ts")).toBeTruthy();
		expect(within(clean as HTMLElement).getByText("util.ts")).toBeTruthy();
	});

	it("applies descriptor-driven decorations (badge, tooltip, color)", () => {
		const decorate = (doc: CollectionDocument) =>
			doc.facets?.status === "modified"
				? { badge: "M", color: "rgb(200, 0, 0)", tooltip: "Modified" }
				: undefined;
		renderCollection({
			type: "table",
			columns: [{ facet: "label", header: "Name" }],
			decorate,
		});
		// app.ts and README.md are both "modified" → both decorated.
		expect(screen.getAllByText("M").length).toBe(2);
		// The colored label carries its tooltip on the wrapping element.
		expect(screen.getAllByTitle("Modified").length).toBe(2);
		const readme = screen.getByText("README.md");
		expect((readme as HTMLElement).style.color).toBe("rgb(200, 0, 0)");
	});

	it("lazily fetches a subtree when an unfetched node expands", () => {
		const onLoadChildren = vi.fn();
		render(
			<Collection
				data={{ documents: [{ id: "n", label: "node", hasChildren: true }] }}
				view={{ type: "tree", onLoadChildren, loadingIds: ["n"] }}
			/>,
		);
		fireEvent.click(screen.getByRole("button", { name: "Expand" }));
		expect(onLoadChildren).toHaveBeenCalledOnce();
		expect(onLoadChildren.mock.calls[0][0].id).toBe("n");
		// While loadingIds contains the node, its subtree slot spins.
		expect(screen.getByText("Loading…")).toBeTruthy();
	});

	it("shows error, loading, and empty states only when there is no data", () => {
		const view: CollectionView = { type: "tree" };
		const { rerender } = render(
			<Collection
				data={{ documents: [], error: new Error("boom") }}
				view={view}
			/>,
		);
		expect(screen.getByText("Failed to load.")).toBeTruthy();

		rerender(
			<Collection data={{ documents: [], isLoading: true }} view={view} />,
		);
		expect(screen.queryByText("Failed to load.")).toBeNull();

		rerender(
			<Collection
				data={{ documents: [], emptyMessage: "Nothing here." }}
				view={view}
			/>,
		);
		expect(screen.getByText("Nothing here.")).toBeTruthy();
	});

	it("keeps rendering retained documents when a background refetch errors", () => {
		renderCollection({ type: "tree" }, { error: new Error("refetch blip") });
		expect(screen.getByText("src")).toBeTruthy();
		expect(screen.queryByText("Failed to load.")).toBeNull();
	});

	it("windows a large tree, rendering only the visible slice", () => {
		const many: CollectionDocument[] = Array.from({ length: 500 }, (_, i) => ({
			id: `row-${i}`,
			label: `row-${i}`,
		}));
		render(
			<Collection
				data={{ documents: many }}
				view={{
					type: "tree",
					virtualizeThreshold: 50,
					rowHeight: 32,
					height: 320,
				}}
			/>,
		);
		// A 320px viewport at 32px rows shows ~10 rows plus overscan — far
		// fewer than 500. The far-down row is not mounted until we scroll.
		expect(screen.queryByText("row-400")).toBeNull();
		expect(screen.getByText("row-0")).toBeTruthy();

		const scroller = screen.getByRole("tree");
		fireEvent.scroll(scroller, { target: { scrollTop: 400 * 32 } });
		expect(screen.getByText("row-400")).toBeTruthy();
		expect(screen.queryByText("row-0")).toBeNull();
	});
});
