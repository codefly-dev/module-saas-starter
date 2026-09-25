import { describe, expect, it } from "vitest";
import { markdownFragmentToText } from "../fragment.js";
import { toPlainText } from "../plain.js";

// A slice of a document, read as the words it renders to. These cases are the
// union of what two consumers pinned before the kit owned this: a search result
// card (chunks sized for retrieval) and a citation passage (a window around a
// quote).

describe("markdownFragmentToText", () => {
	it("reads a slice that opens mid-emphasis and mid-table-row", () => {
		expect(
			markdownFragmentToText("Solution** | The product a customer buys |"),
		).toBe("Solution · The product a customer buys");
	});

	it("drops block markers, links and code markers", () => {
		expect(
			markdownFragmentToText(
				"## Composition\n> Use `codefly run` to [start](https://x.example) it.",
			),
		).toBe("Composition Use codefly run to start it.");
		expect(markdownFragmentToText("- one\n- *two*\n1. three")).toBe(
			"one two three",
		);
		expect(
			markdownFragmentToText(
				"## Records\n\n- **kept** in `store`\n> a _quoted_ line",
			),
		).toBe("Records kept in store a quoted line");
	});

	it("reads a table as cells and skips its alignment row", () => {
		expect(markdownFragmentToText("| a | b |\n|---|:---:|\n| c | d |")).toBe(
			"a · b c · d",
		);
		expect(
			markdownFragmentToText(
				"| Record | Disposition |\n| --- | :-- |\n| #232 | merged |",
			),
		).toBe("Record · Disposition #232 · merged");
	});

	it("keeps underscores that are part of a name", () => {
		expect(
			markdownFragmentToText("set EXAMPLE_LOCAL_STORE and _emphasis_ here"),
		).toBe("set EXAMPLE_LOCAL_STORE and emphasis here");
		expect(markdownFragmentToText("snake_case_name stays whole")).toBe(
			"snake_case_name stays whole",
		);
	});

	it("drops frontmatter, closed or cut short, but keeps a ruled opening", () => {
		expect(
			markdownFragmentToText(
				"---\ntitle: Records\ndate: 2026-09-01\n---\n\nThe body.",
			),
		).toBe("The body.");
		expect(
			markdownFragmentToText(
				"---\ntitle: Records\nrefs:\n  - a\n...\nThe body.",
			),
		).toBe("The body.");
		expect(
			markdownFragmentToText("---\ntitle: Records\nformerly: Ledger\n"),
		).toBe("");
		expect(
			markdownFragmentToText("---\nA ruled opening.\n---\nAnd more."),
		).toBe("A ruled opening. And more.");
	});

	it("keeps a fence's code and an autolink's URL", () => {
		expect(markdownFragmentToText("```go\nfunc main() {}\n```")).toBe(
			"func main() {}",
		);
		expect(
			markdownFragmentToText(
				"See [#232](https://example.com/pull/232) and <https://example.com>.",
			),
		).toBe("See #232 and https://example.com.");
	});

	it("keeps an escaped character literal", () => {
		expect(markdownFragmentToText("2 \\* 3 and \\_x\\_")).toBe("2 * 3 and _x_");
	});

	it("is reachable through toPlainText", () => {
		expect(
			toPlainText("Solution** | a |", "markdown", { fragment: true }),
		).toBe("Solution · a");
	});
});
