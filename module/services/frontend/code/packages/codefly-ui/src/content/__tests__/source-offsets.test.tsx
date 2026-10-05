// @vitest-environment happy-dom
import {
	act,
	cleanup,
	fireEvent,
	render,
	screen,
} from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { Markdown } from "../markdown.js";
import { byteOffsets } from "../source-offsets.js";

afterEach(cleanup);

const encoder = new TextEncoder();
const decoder = new TextDecoder();
const bytes = (text: string) => encoder.encode(text);
const at = (source: string, start: number, end: number) =>
	decoder.decode(bytes(source).subarray(start, end));

// Front matter before the body and accents inside it, so a character offset and
// a byte offset disagree everywhere: this fixture fails against a renderer that
// emits string indices, and an ASCII one would not. The same shape as the
// stopgap a consuming solution wrote for itself and this replaces.
const VERSION =
	"---\ntitle: Ça\n---\n\n# Té\n\nUn paragraphe **gras** et [lien](../x.md).\n\n- Première\n- Deuxième\n\n| Clé | Valeur |\n| --- | --- |\n| à | é |\n\n> Une citation éclairée\n\n```ts\nconst é = 1;\n```\n";
const BODY_AT = VERSION.indexOf("# Té");
const BODY = VERSION.slice(BODY_AT);
const BODY_BASE = bytes(VERSION.slice(0, BODY_AT)).length;

function renderVersion() {
	return render(
		<Markdown headingLevel={1} sourceOffsets sourceStart={BODY_BASE}>
			{BODY}
		</Markdown>,
	);
}

/**
 * What the annotations kit's reading layer does with the attributes
 * (`annotations-ui/src/anchors/source-map.ts`, `textRuns`): each text node is
 * owned by its nearest marked ancestor; inside an exact owner the runs are
 * consecutive, and inside any other the whole owner's range stands for all of
 * them. Reimplemented here rather than imported, because the host depends on no
 * module — if these runs read back the right bytes, that reader maps a selection
 * correctly.
 */
interface Run {
	text: string;
	start: number;
	end: number;
	exact: boolean;
}

function runs(root: Element, content: Uint8Array): Run[] {
	const out: Run[] = [];
	const consumed = new Map<Element, number>();
	const walker = root.ownerDocument.createTreeWalker(root, 4 /* TEXT */);
	for (let node = walker.nextNode(); node; node = walker.nextNode()) {
		const text = node as Text;
		if (text.parentElement?.closest("[data-source-ignore]")) continue;
		let owner: Element | null = text.parentElement;
		while (
			owner &&
			owner !== root.parentElement &&
			!owner.hasAttribute("data-source-start")
		)
			owner = owner.parentElement;
		if (!owner || owner === root.parentElement) continue;
		const start = Number(owner.getAttribute("data-source-start"));
		const end = Number(owner.getAttribute("data-source-end"));
		const exact =
			owner.getAttribute("data-source-exact") !== "false" &&
			decoder.decode(content.subarray(start, end)) ===
				(owner.textContent ?? "");
		const length = bytes(text.data).length;
		const before = consumed.get(owner) ?? 0;
		consumed.set(owner, before + length);
		out.push(
			exact
				? {
						text: text.data,
						start: start + before,
						end: start + before + length,
						exact,
					}
				: { text: text.data, start, end, exact },
		);
	}
	return out;
}

const marked = (container: Element) =>
	Array.from(container.querySelectorAll("[data-source-start]"));

describe("byteOffsets maps a string index to the UTF-8 byte it starts at", () => {
	it.each([
		"plain ascii",
		"accents: Ça é à",
		"an emoji 🎉 after it",
		"mixed 中文 and 🇫🇷 flags",
	])("agrees with TextEncoder on every index of %s", (source) => {
		const map = byteOffsets(source);
		for (let index = 0; index <= source.length; index++) {
			// An index between the halves of a surrogate pair names no byte; it reads
			// as the character's end, which TextEncoder cannot be asked for.
			const code = source.charCodeAt(index);
			if (code >= 0xdc00 && code <= 0xdfff) continue;
			expect(map(index), `index ${index}`).toBe(
				bytes(source.slice(0, index)).length,
			);
		}
	});

	it("counts a lone surrogate as the replacement character the encoder writes", () => {
		const lone = `a\ud800b`;
		expect(byteOffsets(lone)(3)).toBe(bytes(lone).length);
	});

	it("clamps an index outside the source rather than reading past the array", () => {
		const map = byteOffsets("Ça");
		expect(map(-5)).toBe(0);
		expect(map(99)).toBe(3);
	});
});

describe("Markdown emits the bytes it rendered, in the version's byte space", () => {
	it("marks every block an annotation can be written on", () => {
		const { container } = renderVersion();
		for (const selector of ["h1", "p", "li", "th", "td", "blockquote", "pre"]) {
			const elements = Array.from(container.querySelectorAll(selector));
			expect(elements.length, selector).toBeGreaterThan(0);
			for (const element of elements)
				expect(
					element.hasAttribute("data-source-start"),
					`${selector} carries no source range`,
				).toBe(true);
		}
	});

	it("gives every range a valid, character-aligned slice of the version", () => {
		const { container } = renderVersion();
		const content = bytes(VERSION);
		const elements = marked(container);
		expect(elements.length).toBeGreaterThan(10);
		for (const element of elements) {
			const start = Number(element.getAttribute("data-source-start"));
			const end = Number(element.getAttribute("data-source-end"));
			expect(Number.isInteger(start) && Number.isInteger(end)).toBe(true);
			expect(start).toBeGreaterThanOrEqual(BODY_BASE);
			expect(end).toBeLessThanOrEqual(content.length);
			expect(end).toBeGreaterThan(start);
			// A continuation byte at either end would cut a character in half, and
			// the annotations owner refuses such a locator outright.
			expect(
				(content[start] as number) & 0xc0,
				"start cuts a character",
			).not.toBe(0x80);
			if (end < content.length)
				expect(
					(content[end] as number) & 0xc0,
					"end cuts a character",
				).not.toBe(0x80);
		}
	});

	it("says a run is exact only when the text IS those bytes", () => {
		const { container } = renderVersion();
		for (const element of marked(container)) {
			if (element.getAttribute("data-source-exact") === "false") continue;
			const start = Number(element.getAttribute("data-source-start"));
			const end = Number(element.getAttribute("data-source-end"));
			expect(at(VERSION, start, end)).toBe(element.textContent);
		}
	});

	it("reads every rendered run back to the text it shows", () => {
		const { container } = renderVersion();
		const root = container.querySelector("[data-slot=content-markdown]");
		const found = runs(root as Element, bytes(VERSION));
		expect(found.length).toBeGreaterThan(8);
		for (const run of found) {
			if (run.exact) expect(at(VERSION, run.start, run.end)).toBe(run.text);
			// An inexact run maps as a whole: its text is somewhere in those bytes.
			else
				expect(at(VERSION, run.start, run.end)).toContain(
					run.text.trim().slice(0, 4),
				);
		}
	});

	it("keeps a plain run byte-exact and marks what markdown rewrote", () => {
		const { container } = renderVersion();
		const exact = Array.from(
			container.querySelectorAll(
				"span[data-source-start]:not([data-source-exact])",
			),
		).find((span) => span.textContent === "Un paragraphe ");
		expect(
			exact,
			"the plain run before the emphasis is not an exact span",
		).toBeDefined();
		expect(
			at(
				VERSION,
				Number(exact?.getAttribute("data-source-start")),
				Number(exact?.getAttribute("data-source-end")),
			),
		).toBe("Un paragraphe ");

		// The heading dropped its `#`, the emphasis its `**`, the item its bullet,
		// the link its target: each is a block whose text is not its bytes.
		for (const selector of [
			"h1",
			"strong",
			"li",
			"[data-slot=content-link-inert]",
			"pre",
		]) {
			const element = container.querySelector(selector);
			expect(element?.getAttribute("data-source-exact"), selector).toBe(
				"false",
			);
		}
	});

	it("maps a fenced block as a whole and its code character for character", () => {
		const { container } = renderVersion();
		const pre = container.querySelector("pre") as HTMLElement;
		const code = pre.querySelector("code") as HTMLElement;
		expect(pre.getAttribute("data-source-exact")).toBe("false");
		expect(
			at(
				VERSION,
				Number(pre.getAttribute("data-source-start")),
				Number(pre.getAttribute("data-source-end")),
			),
		).toContain("```ts");
		expect(code.getAttribute("data-source-exact")).toBe(null);
		expect(
			at(
				VERSION,
				Number(code.getAttribute("data-source-start")),
				Number(code.getAttribute("data-source-end")),
			),
		).toBe(code.textContent);
	});

	it("falls back to the whole block when the body is not in the source verbatim", () => {
		// An indented code block, and a fence inside a list item whose body runs to
		// a second line: the parser strips the indent from each line, so those
		// bytes are nowhere in the source as one run. The block still carries a
		// range — a reader can comment on it — and says it is not exact rather than
		// claiming bytes it cannot stand behind.
		const source =
			"- item:\n\n  ```ts\n  const a = 1;\n  const b = 2;\n  ```\n\nand then\n\n    four spaces\n    and more\n";
		const { container } = render(<Markdown sourceOffsets>{source}</Markdown>);
		const blocks = Array.from(container.querySelectorAll("pre"));
		expect(blocks).toHaveLength(2);
		for (const pre of blocks) {
			const code = pre.querySelector("code") as HTMLElement;
			expect(code.getAttribute("data-source-exact")).toBe("false");
			const start = Number(code.getAttribute("data-source-start"));
			const end = Number(code.getAttribute("data-source-end"));
			expect(end).toBeGreaterThan(start);
			// Still a range a reader can write a comment on: it holds the text, even
			// though it is not only the text.
			expect(at(source, start, end)).toContain(
				(code.textContent ?? "").split("\n")[0]?.trim(),
			);
		}
	});

	// A text comparison cannot catch this one: the body's text can occur inside
	// the opening line, so a range pointing into the info string reads back as the
	// right text while naming the wrong bytes. Only the offset tells them apart.
	it.each([
		{ source: "```python\nn\n```\n", start: 10, end: 11 },
		{ source: "~~~\n~\n~~~\n", start: 4, end: 5 },
		{ source: "```ts\nconst a = 1;\n```\n", start: 6, end: 18 },
	])(
		"puts a fence's body after its opening line, not inside it ($source)",
		({ source, start, end }) => {
			const { container } = render(<Markdown sourceOffsets>{source}</Markdown>);
			const code = container.querySelector("code") as HTMLElement;
			expect(Number(code.getAttribute("data-source-start"))).toBe(start);
			expect(Number(code.getAttribute("data-source-end"))).toBe(end);
			expect(code.getAttribute("data-source-exact")).toBe(null);
			expect(at(source, start, end)).toBe(code.textContent);
			// Belt and braces: whatever the text says, the body cannot begin before
			// the newline that closed the opening fence.
			expect(start).toBeGreaterThan(source.indexOf("\n"));
		},
	);

	it("counts bytes rather than characters, so the accents do not shift it", () => {
		const { container } = renderVersion();
		// `# Té` is four characters and five bytes. A renderer emitting string
		// indices would end this heading one byte early, and every block after the
		// front matter's `Ça` two bytes early — which is why the fixture has both.
		const heading = container.querySelector("h1") as HTMLElement;
		expect(Number(heading.getAttribute("data-source-start"))).toBe(BODY_BASE);
		expect(Number(heading.getAttribute("data-source-end"))).toBe(BODY_BASE + 5);
		// The front matter's own accent already makes the byte offset differ from
		// the string index the parser counts in.
		expect(BODY_BASE).toBe(BODY_AT + 1);

		// Two accents in one byte-exact paragraph: 21 characters, 23 bytes.
		const quoted = container.querySelector("blockquote p") as HTMLElement;
		const start = Number(quoted.getAttribute("data-source-start"));
		const end = Number(quoted.getAttribute("data-source-end"));
		expect(quoted.textContent).toBe("Une citation éclairée");
		expect(end - start).toBe(23);
		expect(at(VERSION, start, end)).toBe(quoted.textContent);
	});

	it("offsets name the version, not the slice, under sourceStart", () => {
		const { container } = render(
			<Markdown headingLevel={1} sourceOffsets>
				{BODY}
			</Markdown>,
		);
		const first = Number(
			container.querySelector("h1")?.getAttribute("data-source-start"),
		);
		expect(first).toBe(0);
		const { container: shifted } = renderVersion();
		expect(
			Number(shifted.querySelector("h1")?.getAttribute("data-source-start")),
		).toBe(BODY_BASE);
	});

	it("emits nothing when the caller did not ask", () => {
		const { container } = render(<Markdown>{BODY}</Markdown>);
		expect(container.querySelectorAll("[data-source-start]")).toHaveLength(0);
	});

	it("keeps a run's bytes when a single newline is a line break", () => {
		const source = "Première ligne\nseconde ligne\n";
		const { container } = render(
			<Markdown sourceOffsets lineBreaks>
				{source}
			</Markdown>,
		);
		expect(container.querySelectorAll("br")).toHaveLength(1);
		const spans = Array.from(
			container.querySelectorAll("span[data-source-start]"),
		);
		expect(spans).toHaveLength(2);
		for (const span of spans)
			expect(
				at(
					source,
					Number(span.getAttribute("data-source-start")),
					Number(span.getAttribute("data-source-end")),
				),
			).toBe(span.textContent);
	});

	// A text node's value is NOT its source: the parser drops a line's
	// indentation and a soft break's trailing space, and decodes `&amp;` to one
	// character. Counting each piece along the value therefore puts every piece
	// after the first difference on bytes that are not its text — and marking it
	// inexact does not save it, because an inexact run's own range is what a
	// comment is written against. Each piece is located in the source instead.
	describe.each([
		{
			name: "a list item's continuation line, whose indent the parser dropped",
			source: "- first line\n  second line\n",
			exact: ["first line", "second line"],
			fallback: undefined,
		},
		{
			name: "a soft break whose trailing space the parser dropped",
			source: "foo \nbar\n",
			exact: ["foo", "bar"],
			fallback: undefined,
		},
		{
			// `&amp;` is five bytes rendered as one character, so the first piece is
			// nowhere in the source as written. Nothing after it can be located
			// either — a search from a cursor that never advanced past it could match
			// BEFORE the piece's real place — so every piece falls back to the whole
			// text node, which the reader widens to anyway.
			name: "a decoded entity, which no run can be located past",
			source: "a &amp; b\nc d\n",
			exact: [],
			fallback: [0, 13] as [number, number],
		},
	])("a line break in $name", ({ source, exact, fallback }) => {
		it("gives every run bytes that are its text, or one honest coarse range", () => {
			const { container } = render(
				<Markdown sourceOffsets lineBreaks>
					{source}
				</Markdown>,
			);
			const spans = Array.from(
				container.querySelectorAll("span[data-source-start]"),
			);
			expect(spans.length).toBeGreaterThan(1);
			const exactly: string[] = [];
			const coarse = new Set<string>();
			for (const span of spans) {
				const from = Number(span.getAttribute("data-source-start"));
				const to = Number(span.getAttribute("data-source-end"));
				if (span.getAttribute("data-source-exact") === "false") {
					coarse.add(`${from},${to}`);
					continue;
				}
				expect(at(source, from, to)).toBe(span.textContent);
				exactly.push(span.textContent ?? "");
			}
			expect(exactly).toEqual(exact);
			// The bug this replaced gave each piece its own WRONG range. The fallback
			// gives them all the same one, so a comment is coarse rather than
			// misplaced: one range, and it is the whole node.
			expect([...coarse]).toEqual(
				fallback ? [`${fallback[0]},${fallback[1]}`] : [],
			);
		});
	});

	// A text comparison cannot catch this one: the body's text can occur inside
	// the opening line, so a range pointing into the info string reads back as the
	// right text while naming the wrong bytes. Only the offset tells them apart.
	it.each([
		{ source: "```python\nn\n```\n", start: 10, end: 11 },
		{ source: "~~~\n~\n~~~\n", start: 4, end: 5 },
		{ source: "```ts\nconst a = 1;\n```\n", start: 6, end: 18 },
	])(
		"puts a fence's body after its opening line, not inside it ($source)",
		({ source, start, end }) => {
			const { container } = render(<Markdown sourceOffsets>{source}</Markdown>);
			const code = container.querySelector("code") as HTMLElement;
			expect(Number(code.getAttribute("data-source-start"))).toBe(start);
			expect(Number(code.getAttribute("data-source-end"))).toBe(end);
			expect(code.getAttribute("data-source-exact")).toBe(null);
			expect(at(source, start, end)).toBe(code.textContent);
			// Belt and braces: whatever the text says, the body cannot begin before
			// the newline that closed the opening fence.
			expect(start).toBeGreaterThan(source.indexOf("\n"));
		},
	);

	it("counts bytes rather than characters, so the accents do not shift it", () => {
		const { container } = renderVersion();
		// `# Té` is four characters and five bytes. A renderer emitting string
		// indices would end this heading one byte early, and every block after the
		// front matter's `Ça` two bytes early — which is why the fixture has both.
		const heading = container.querySelector("h1") as HTMLElement;
		expect(Number(heading.getAttribute("data-source-start"))).toBe(BODY_BASE);
		expect(Number(heading.getAttribute("data-source-end"))).toBe(BODY_BASE + 5);
		// The front matter's own accent already makes the byte offset differ from
		// the string index the parser counts in.
		expect(BODY_BASE).toBe(BODY_AT + 1);

		// Two accents in one byte-exact paragraph: 21 characters, 23 bytes.
		const quoted = container.querySelector("blockquote p") as HTMLElement;
		const start = Number(quoted.getAttribute("data-source-start"));
		const end = Number(quoted.getAttribute("data-source-end"));
		expect(quoted.textContent).toBe("Une citation éclairée");
		expect(end - start).toBe(23);
		expect(at(VERSION, start, end)).toBe(quoted.textContent);
	});

	it("offsets name the version, not the slice, under sourceStart", () => {
		const { container } = render(
			<Markdown headingLevel={1} sourceOffsets>
				{BODY}
			</Markdown>,
		);
		const first = Number(
			container.querySelector("h1")?.getAttribute("data-source-start"),
		);
		expect(first).toBe(0);
		const { container: shifted } = renderVersion();
		expect(
			Number(shifted.querySelector("h1")?.getAttribute("data-source-start")),
		).toBe(BODY_BASE);
	});

	it("emits nothing when the caller did not ask", () => {
		const { container } = render(<Markdown>{BODY}</Markdown>);
		expect(container.querySelectorAll("[data-source-start]")).toHaveLength(0);
	});

	it("keeps a run's bytes when a single newline is a line break", () => {
		const source = "Première ligne\nseconde ligne\n";
		const { container } = render(
			<Markdown sourceOffsets lineBreaks>
				{source}
			</Markdown>,
		);
		expect(container.querySelectorAll("br")).toHaveLength(1);
		const spans = Array.from(
			container.querySelectorAll("span[data-source-start]"),
		);
		expect(spans).toHaveLength(2);
		for (const span of spans)
			expect(
				at(
					source,
					Number(span.getAttribute("data-source-start")),
					Number(span.getAttribute("data-source-end")),
				),
			).toBe(span.textContent);
	});

	it("does not rewrite the source for references, which would move every offset", () => {
		const source = "Voir [1] et [2] après.\n";
		const { container } = render(
			<Markdown
				sourceOffsets
				references={{ markers: [1, 2], render: (n) => `<${n}>` }}
			>
				{source}
			</Markdown>,
		);
		expect(container.textContent).toContain("[1]");
		expect(container.textContent).not.toContain("<1>");
		expect(container.querySelectorAll("[data-source-exact]")).toHaveLength(0);
		for (const span of container.querySelectorAll("span[data-source-start]")) {
			if (span.getAttribute("data-source-exact") === "false") continue;
			expect(
				at(
					source,
					Number(span.getAttribute("data-source-start")),
					Number(span.getAttribute("data-source-end")),
				),
			).toBe(span.textContent);
		}
	});
});

describe("a resolved link is the caller's answer, held to the kit's allowlist", () => {
	it("opens an external target in a new context and an internal one in place", () => {
		render(
			<Markdown
				resolveLink={(href) =>
					href.startsWith("../")
						? {
								href: `https://example.com/repo/${href.slice(3)}`,
								external: true,
							}
						: { href: `https://product.example/pages/${href}`, external: false }
				}
			>
				{"[away](../decisions/x.md#why) and [here](guide.md)\n"}
			</Markdown>,
		);
		const away = screen.getByRole("link", { name: "away" });
		expect(away.getAttribute("href")).toBe(
			"https://example.com/repo/decisions/x.md#why",
		);
		expect(away.getAttribute("target")).toBe("_blank");
		expect(away.getAttribute("rel")).toBe("noopener noreferrer");
		const here = screen.getByRole("link", { name: "here" });
		expect(here.getAttribute("href")).toBe(
			"https://product.example/pages/guide.md",
		);
		expect(here.getAttribute("target")).toBe(null);
		expect(here.getAttribute("rel")).toBe(null);
	});

	it.each([
		"javascript:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		"vbscript:msgbox(1)",
		"https://bank.example@evil.example/",
		"relative/not/a/url",
	])("refuses %s however the resolver spells it", (resolved) => {
		const { container } = render(
			<Markdown resolveLink={() => ({ href: resolved, external: true })}>
				{"[text](../x.md)\n"}
			</Markdown>,
		);
		expect(container.querySelectorAll("a")).toHaveLength(0);
		expect(
			container.querySelector("[data-slot=content-link-inert]")?.textContent,
		).toBe("text");
	});

	it("leaves the kit's own rule in place when the resolver declines", () => {
		render(
			<Markdown resolveLink={() => undefined}>
				{"[up](https://example.com/a)\n"}
			</Markdown>,
		);
		expect(screen.getByRole("link", { name: "up" }).getAttribute("rel")).toBe(
			"noopener noreferrer",
		);
	});

	it("carries the source range on a resolved link, so it can be annotated", () => {
		const source = "Voir [lien](../x.md).\n";
		const { container } = render(
			<Markdown
				sourceOffsets
				resolveLink={() => ({ href: "https://example.com/x", external: true })}
			>
				{source}
			</Markdown>,
		);
		const link = container.querySelector("a") as HTMLElement;
		expect(
			at(
				source,
				Number(link.getAttribute("data-source-start")),
				Number(link.getAttribute("data-source-end")),
			),
		).toBe("[lien](../x.md)");
		expect(link.getAttribute("data-source-exact")).toBe("false");
	});
});

describe("source offsets change nothing about the untrusted-content rules", () => {
	it("still drops raw HTML and still refuses a javascript: link in the source", () => {
		const { container } = render(
			<Markdown sourceOffsets>
				{
					'<img src=x onerror="alert(1)">\n\n[x](javascript:alert(1))\n\n<b>bold</b>\n'
				}
			</Markdown>,
		);
		expect(container.querySelectorAll("img")).toHaveLength(0);
		expect(container.querySelectorAll("b")).toHaveLength(0);
		expect(container.querySelectorAll("a")).toHaveLength(0);
		expect(container.innerHTML).not.toContain("onerror");
	});
});

describe("chrome the renderer added belongs to no source", () => {
	it("leaves a code block's copy control outside every range the reader reads", async () => {
		// Inside a list item the control DOES have a marked ancestor, so without
		// `data-source-ignore` its announcement would be read as text of the source
		// and lend the item's bytes to a word the document never wrote.
		const source = "- the code:\n\n  ```ts\n  const a = 1;\n  ```\n";
		const { container } = render(<Markdown sourceOffsets>{source}</Markdown>);
		const control = container.querySelector(
			"[data-slot=content-code] [data-source-ignore]",
		) as HTMLElement;
		expect(control).not.toBeNull();
		expect(control.closest("li")).not.toBeNull();

		// Make it say something, so there is text to exclude rather than an empty
		// node a passing test could not tell from an excluded one.
		await act(async () => {
			fireEvent.click(
				control.querySelector("[data-slot=content-copy]") as HTMLElement,
			);
		});
		expect(control.textContent).toMatch(/Cop/);

		const root = container.querySelector(
			"[data-slot=content-markdown]",
		) as Element;
		for (const run of runs(root, bytes(source)))
			expect(run.text).not.toMatch(/Cop/);
	});

	it("leaves an unavailable link's reason outside the link's range", () => {
		const source = "See [the guide](../guide.md).\n";
		const { container } = render(
			<Markdown sourceOffsets resolveLink={() => ({ unavailable: "not here" })}>
				{source}
			</Markdown>,
		);
		const link = container.querySelector(
			"[data-slot=content-link-unavailable]",
		) as HTMLElement;
		expect(link.getAttribute("data-source-start")).not.toBeNull();
		expect(link.textContent).toContain("not here");
		const root = container.querySelector(
			"[data-slot=content-markdown]",
		) as Element;
		for (const run of runs(root, bytes(source)))
			expect(run.text).not.toContain("not here");
	});
});
