// @vitest-environment happy-dom
import {
	act,
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Chat } from "../../chat/chat.js";
import { CodeBlock } from "../code-block.js";
import { Content } from "../content.js";
import { detectFormat } from "../detect.js";
import { JsonView } from "../json-view.js";
import { Markdown } from "../markdown.js";
import { toPlainText } from "../plain.js";
import { TONE } from "../tones.js";

afterEach(cleanup);

describe("detectFormat", () => {
	it.each([
		['{"a":1}', "json"],
		["  [1, 2, 3]  ", "json"],
		["{not json}", "text"],
		["42", "text"],
		['"quoted"', "text"],
		["# Heading", "markdown"],
		["- one\n- two", "markdown"],
		["1. first", "markdown"],
		["see [docs](https://example.com)", "markdown"],
		["| a | b |\n| --- | --- |\n| 1 | 2 |", "markdown"],
		["```\ncode\n```", "markdown"],
		["a **strong** claim", "markdown"],
		["use `npm ci`", "markdown"],
		["> quoted", "markdown"],
		["Plain prose, with a comma - and a dash.", "text"],
		["", "text"],
	])("%j → %s", (value, expected) => {
		expect(detectFormat(value)).toBe(expected);
	});

	it("treats structured values as json and nullish as text", () => {
		expect(detectFormat({ a: 1 })).toBe("json");
		expect(detectFormat([1])).toBe("json");
		expect(detectFormat(3)).toBe("json");
		expect(detectFormat(null)).toBe("text");
		expect(detectFormat(undefined)).toBe("text");
	});
});

describe("toPlainText", () => {
	it("keeps the words of markdown and drops its markup", () => {
		expect(
			toPlainText(
				"# Title\n\nSome **bold** and [a link](https://example.com).\n\n- one\n- two",
			),
		).toBe("Title Some bold and a link. one two");
	});

	it("reads a table as its cells", () => {
		expect(toPlainText("| a | b |\n| - | - |\n| 1 | 2 |", "markdown")).toBe(
			"a · b 1 · 2",
		);
	});

	it("drops raw HTML and keeps image alt text", () => {
		expect(
			toPlainText(
				"hi <b onclick=x>there</b> ![alt text](https://example.com/a.png)",
				"markdown",
			),
		).toBe("hi there alt text");
	});

	it("compacts JSON and collapses text whitespace", () => {
		expect(toPlainText('{\n  "a": [1, 2]\n}')).toBe('{"a":[1,2]}');
		expect(toPlainText({ a: 1 })).toBe('{"a":1}');
		expect(toPlainText("a\n\n  b\tc", "text")).toBe("a b c");
	});

	it("bounds the work on a huge input", () => {
		const huge = "word ".repeat(200_000);
		const started = performance.now();
		expect(toPlainText(huge, "markdown").length).toBeLessThanOrEqual(8_000);
		expect(performance.now() - started).toBeLessThan(1_000);
	});
});

describe("<Content>", () => {
	it("renders auto-detected markdown as structure", () => {
		const { container } = render(
			<Content value={"## Result\n\n- [x] done\n- [ ] open"} />,
		);
		expect(container.querySelector("h4")?.textContent).toBe("Result");
		const boxes = container.querySelectorAll('input[type="checkbox"]');
		expect(boxes).toHaveLength(2);
		expect((boxes[0] as HTMLInputElement).checked).toBe(true);
		expect((boxes[0] as HTMLInputElement).disabled).toBe(true);
	});

	it("renders a GFM table and strikethrough", () => {
		const { container } = render(
			<Content
				value={"| a | b |\n| - | - |\n| 1 | ~~2~~ |"}
				format="markdown"
			/>,
		);
		expect(container.querySelectorAll("th")).toHaveLength(2);
		expect(container.querySelector("del")?.textContent).toBe("2");
	});

	it("maps markdown headings under the caller's level", () => {
		const { container } = render(
			<Markdown headingLevel={2}>{"# a\n\n## b\n\n### c"}</Markdown>,
		);
		expect(
			[...container.querySelectorAll("h2, h3, h4")].map((h) => h.tagName),
		).toEqual(["H2", "H3", "H4"]);
	});

	it("preserves whitespace in text", () => {
		const { container } = render(
			<Content value={"line one\n    indented"} format="text" />,
		);
		const block = container.querySelector('[data-slot="content-text"]');
		expect(block?.textContent).toBe("line one\n    indented");
		expect(block?.className).toContain("whitespace-pre-wrap");
	});

	it("renders inline as one truncated line with the full text as its title", () => {
		const { container } = render(
			<Content value={"**Revenue** grew\n\n12%"} variant="inline" />,
		);
		const line = container.querySelector('[data-slot="content-inline"]');
		expect(line?.textContent).toBe("Revenue grew 12%");
		expect(line?.getAttribute("title")).toBe("Revenue grew 12%");
		expect(line?.className).toContain("truncate");
	});

	it("clamps inline to more than one line on request", () => {
		const { container } = render(
			<Content value="a long excerpt" variant="inline" lines={2} />,
		);
		expect(
			container.querySelector('[data-slot="content-inline"]')?.className,
		).toContain("line-clamp-2");
	});

	it("renders structured data as a JSON tree", () => {
		render(<Content value={{ status: "ok" }} label="Payload" />);
		expect(
			screen.getByRole("figure", { name: "Payload" }).textContent,
		).toContain('"status"');
	});

	it("falls back to text when format=json does not parse", () => {
		const { container } = render(<Content value="{broken" format="json" />);
		expect(
			container.querySelector('[data-slot="content-text"]')?.textContent,
		).toBe("{broken");
	});
});

describe("<JsonView>", () => {
	it("opens the root, collapses deeper levels, and toggles with the keyboard", () => {
		render(<JsonView value={{ outer: { inner: { deep: 1 } } }} />);
		const root = screen.getAllByRole("button", { expanded: true });
		expect(root).toHaveLength(1);
		const outer = screen.getByRole("button", { name: /"outer"/ });
		expect(outer.getAttribute("aria-expanded")).toBe("false");
		expect(outer.textContent).toContain("1 key");
		expect(screen.queryByText('"inner"')).toBeNull();
		fireEvent.click(outer);
		expect(outer.getAttribute("aria-expanded")).toBe("true");
		expect(screen.getByText('"inner"')).toBeTruthy();
	});

	it("pages a huge array instead of rendering it", () => {
		const big = Array.from({ length: 50_000 }, (_, index) => ({ index }));
		const started = performance.now();
		const { container } = render(<JsonView value={big} pageSize={100} />);
		expect(performance.now() - started).toBeLessThan(2_000);
		// 100 visible children, each a collapsed object button, plus the root.
		expect(container.querySelectorAll("button[aria-expanded]")).toHaveLength(
			101,
		);
		const more = screen.getByRole("button", {
			name: /Show 100 more \(49,900 hidden\)/,
		});
		fireEvent.click(more);
		expect(container.querySelectorAll("button[aria-expanded]")).toHaveLength(
			201,
		);
	});

	it("previews a long string and expands it on request", () => {
		const long = "x".repeat(1_000);
		const { container } = render(<JsonView value={{ long }} />);
		expect(container.textContent).not.toContain(long);
		fireEvent.click(
			screen.getByRole("button", { name: /Show all 1,000 characters/ }),
		);
		expect(container.textContent).toContain(long);
	});

	it("survives a cycle", () => {
		const cyclic: Record<string, unknown> = { name: "a" };
		cyclic.self = cyclic;
		const { container } = render(<JsonView value={cyclic} expandDepth={3} />);
		expect(container.textContent).toContain("[Circular]");
	});

	it("parses a JSON string and shows unparseable text verbatim", () => {
		const { container, rerender } = render(<JsonView value='{"a": true}' />);
		expect(container.textContent).toContain("true");
		rerender(<JsonView value="not json" />);
		expect(
			container.querySelector('[data-slot="content-text"]')?.textContent,
		).toBe("not json");
	});

	it("copies the whole document through a labelled button", async () => {
		const writeText = vi.fn().mockResolvedValue(undefined);
		Object.defineProperty(navigator, "clipboard", {
			value: { writeText },
			configurable: true,
		});
		render(<JsonView value={{ a: [1, 2] }} label="Payload" />);
		await act(async () => {
			fireEvent.click(screen.getByRole("button", { name: "Copy Payload" }));
		});
		expect(writeText).toHaveBeenCalledWith(
			'{\n  "a": [\n    1,\n    2\n  ]\n}',
		);
		expect(screen.getByRole("status").textContent).toBe("Copied");
	});
});

describe("<CodeBlock>", () => {
	it("renders plain first, then highlights with token classes only", async () => {
		const { container } = render(
			<CodeBlock code={'const a = "b"; // note'} language="ts" />,
		);
		expect(container.querySelector("code")?.textContent).toBe(
			'const a = "b"; // note',
		);
		await waitFor(() =>
			expect(container.querySelector("code span")).not.toBeNull(),
		);
		const classes = [...container.querySelectorAll("code span")].map(
			(span) => span.className,
		);
		expect(classes).toContain(TONE.keyword);
		expect(classes).toContain(TONE.string);
		expect(classes.some((name) => name.includes("text-muted-foreground"))).toBe(
			true,
		);
		expect(classes.join(" ")).not.toMatch(/hljs/);
		expect(container.querySelector("code")?.textContent).toBe(
			'const a = "b"; // note',
		);
	});

	it("stays plain for an unknown language", async () => {
		const { container } = render(
			<CodeBlock code="x := 1" language="brainfuck" />,
		);
		await new Promise((resolve) => setTimeout(resolve, 50));
		expect(container.querySelector("code span")).toBeNull();
	});

	it("never shows a stale highlight after the code changes", async () => {
		const { container, rerender } = render(
			<CodeBlock code="let a = 1" language="js" />,
		);
		await waitFor(() =>
			expect(container.querySelector("code span")).not.toBeNull(),
		);
		rerender(<CodeBlock code="let b = 2" language="js" />);
		expect(container.querySelector("code")?.textContent).toBe("let b = 2");
	});

	it("labels its copy button", () => {
		render(<CodeBlock code="x" />);
		expect(screen.getByRole("button", { name: "Copy code" })).toBeTruthy();
	});
});

describe("<Chat> renders a markdown answer through the content tier", () => {
	it("keeps text messages verbatim and renders markdown ones", () => {
		const { container } = render(
			<Chat
				messages={[
					{ id: "1", role: "user", content: "**not bold**" },
					{
						id: "2",
						role: "assistant",
						content: "**bold** answer",
						format: "markdown",
					},
				]}
			/>,
		);
		expect(container.textContent).toContain("**not bold**");
		expect(container.querySelector("strong")?.textContent).toBe("bold");
	});
});

describe("<Markdown> references and line breaks", () => {
	const render_ = (marker: number) => (
		<button type="button" data-marker={marker}>{`ref ${marker}`}</button>
	);

	it("renders only the cited markers through the caller", () => {
		const { container } = render(
			<Markdown references={{ markers: [1, 2], render: render_ }}>
				{"Revenue grew [1] and costs fell [2]; see [3]."}
			</Markdown>,
		);
		const refs = [...container.querySelectorAll("button[data-marker]")];
		expect(refs.map((b) => b.getAttribute("data-marker"))).toEqual(["1", "2"]);
		expect(container.textContent).toContain("see [3].");
	});

	it("keeps a marker literal inside code and link text, and drops a cited marker's URL", () => {
		const { container } = render(
			<Markdown references={{ markers: [1, 2], render: render_ }}>
				{
					"`[1]` and [see [2]](https://example.com) and [1](https://model.example/src)\n\n[2]: https://evil.example"
				}
			</Markdown>,
		);
		expect(container.querySelector("code")?.textContent).toBe("[1]");
		expect(container.querySelector("a")?.textContent).toBe("see [2]");
		expect(container.querySelectorAll("button[data-marker]")).toHaveLength(1);
		expect(container.innerHTML).not.toContain("model.example");
		expect(container.innerHTML).not.toContain("evil.example");
	});

	it("strips sentinel characters the source smuggled in", () => {
		const { container } = render(
			<Markdown references={{ markers: [1], render: render_ }}>
				{`a ${String.fromCharCode(0xe000)}9${String.fromCharCode(0xe001)} b`}
			</Markdown>,
		);
		expect(container.querySelectorAll("button[data-marker]")).toHaveLength(0);
		expect(container.textContent).toBe("a 9 b");
	});

	it("turns a single newline into a line break only on request", () => {
		const soft = render(<Markdown>{"one\ntwo"}</Markdown>);
		expect(soft.container.querySelectorAll("br")).toHaveLength(0);
		cleanup();
		const hard = render(<Markdown lineBreaks>{"one\ntwo"}</Markdown>);
		expect(hard.container.querySelectorAll("br")).toHaveLength(1);
	});
});
