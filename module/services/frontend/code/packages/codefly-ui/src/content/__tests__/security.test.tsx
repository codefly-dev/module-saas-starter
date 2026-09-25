// @vitest-environment happy-dom
import { cleanup, render } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { Content } from "../content.js";
import { Markdown } from "../markdown.js";
import { safeImageUrl, safeLinkUrl } from "../url.js";

afterEach(cleanup);

// Content reaching these components is ingested documents and model output, so
// every vector here is one an attacker controls end to end. The assertions are
// about the DOM that results, not about what the parser happens to do today.

function assertInert(root: HTMLElement) {
	expect(
		root.querySelectorAll(
			"script, iframe, object, embed, style, form, link, meta, base, svg foreignObject",
		),
	).toHaveLength(0);
	for (const element of root.querySelectorAll("*")) {
		for (const attribute of element.getAttributeNames()) {
			expect(
				attribute,
				`<${element.tagName.toLowerCase()} ${attribute}>`,
			).not.toMatch(/^on/i);
			expect(attribute).not.toBe("srcdoc");
		}
	}
	for (const anchor of root.querySelectorAll("a")) {
		const href = anchor.getAttribute("href") ?? "";
		expect(href).toMatch(/^(https?:|mailto:)/);
		expect(anchor.getAttribute("rel")).toBe("noopener noreferrer");
	}
	expect(root.querySelectorAll("img")).toHaveLength(0);
}

describe("markdown renders no raw HTML", () => {
	it.each([
		["script", "<script>alert(1)</script>"],
		["img onerror", '<img src="x" onerror="alert(1)">'],
		["inline img onerror", "before <img src=x onerror=alert(1)> after"],
		["iframe", '<iframe src="https://evil.example"></iframe>'],
		["svg onload", "<svg onload=alert(1)></svg>"],
		["raw anchor", '<a href="javascript:alert(1)">click</a>'],
		["style", "<style>body{display:none}</style>"],
		["details ontoggle", "<details open ontoggle=alert(1)>x</details>"],
		["form", '<form action="https://evil.example"><input name=a></form>'],
		[
			"meta refresh",
			'<meta http-equiv="refresh" content="0;url=https://evil.example">',
		],
		["base", '<base href="https://evil.example/">'],
	])("%s", (_name, source) => {
		const { container } = render(
			<Markdown>{`# Title\n\n${source}\n\ntext`}</Markdown>,
		);
		assertInert(container);
		expect(container.textContent).not.toContain("alert(1)");
	});

	it("shows HTML inside code as text, not markup", () => {
		const { container } = render(
			<Markdown>{"`<script>alert(1)</script>`"}</Markdown>,
		);
		assertInert(container);
		expect(container.querySelector("code")?.textContent).toBe(
			"<script>alert(1)</script>",
		);
	});

	it("shows HTML in a fenced block as text", () => {
		const { container } = render(
			<Markdown>{"```html\n<img src=x onerror=alert(1)>\n```"}</Markdown>,
		);
		assertInert(container);
		expect(container.querySelector("pre")?.textContent).toContain(
			"<img src=x onerror=alert(1)>",
		);
	});
});

describe("links are live only for http, https and mailto", () => {
	it.each([
		["javascript", "[x](javascript:alert(1))"],
		["uppercase", "[x](JAVASCRIPT:alert(1))"],
		["tab in scheme", "[x](java\tscript:alert(1))"],
		["entity-encoded", "[x](&#106;avascript:alert(1))"],
		["percent-encoded colon", "[x](javascript%3Aalert(1))"],
		["data", "[x](data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==)"],
		["vbscript", "[x](vbscript:msgbox(1))"],
		["file", "[x](file:///etc/passwd)"],
		["autolink", "<javascript:alert(1)>"],
		["reference", "[x][r]\n\n[r]: javascript:alert(1)"],
		["protocol-relative", "[x](//evil.example/path)"],
		["relative", "[x](/account/delete)"],
		["fragment", "[x](#top)"],
		["credentials", "[x](https://bank.example@evil.example/)"],
	])("%s is inert", (_name, source) => {
		const { container } = render(<Markdown>{source}</Markdown>);
		assertInert(container);
		expect(container.querySelectorAll("a")).toHaveLength(0);
		expect(container.textContent).not.toContain("alert(1)(");
	});

	it.each([
		["https", "[x](https://example.com/a?b=1)", "https://example.com/a?b=1"],
		["http", "[x](http://example.com/)", "http://example.com/"],
		["mailto", "[x](mailto:user@example.com)", "mailto:user@example.com"],
		[
			"gfm autolink literal",
			"see www.example.com today",
			"http://www.example.com/",
		],
	])(
		"%s is live, opens a new context, and leaks no referrer",
		(_name, source, href) => {
			const { container } = render(<Markdown>{source}</Markdown>);
			assertInert(container);
			const anchor = container.querySelector("a");
			expect(anchor?.getAttribute("href")).toBe(href);
			expect(anchor?.getAttribute("target")).toBe("_blank");
			expect(anchor?.getAttribute("rel")).toBe("noopener noreferrer");
		},
	);
});

describe("images are off unless opted in", () => {
	it("renders an image as its alt text and fetches nothing by default", () => {
		const { container } = render(
			<Markdown>{"![a chart](https://example.com/chart.png)"}</Markdown>,
		);
		expect(container.querySelectorAll("img")).toHaveLength(0);
		expect(container.textContent).toContain("a chart");
	});

	it("renders an https image without a referrer when opted in", () => {
		const { container } = render(
			<Markdown allowImages>
				{"![a chart](https://example.com/chart.png)"}
			</Markdown>,
		);
		const image = container.querySelector("img");
		expect(image?.getAttribute("src")).toBe("https://example.com/chart.png");
		expect(image?.getAttribute("referrerpolicy")).toBe("no-referrer");
		expect(image?.getAttribute("loading")).toBe("lazy");
	});

	it.each([
		["http", "![a](http://example.com/a.png)"],
		["data", "![a](data:image/svg+xml;base64,PHN2ZyBvbmxvYWQ9YWxlcnQoMSk+)"],
		["javascript", "![a](javascript:alert(1))"],
		["relative", "![a](/avatar.png)"],
	])("refuses a %s image even when opted in", (_name, source) => {
		const { container } = render(<Markdown allowImages>{source}</Markdown>);
		expect(container.querySelectorAll("img")).toHaveLength(0);
	});
});

describe("every format escapes what it shows", () => {
	const hostile = "<img src=x onerror=alert(1)><script>alert(2)</script>";

	it.each(["text", "code", "json", "auto"] as const)("%s", (format) => {
		const value =
			format === "json" ? JSON.stringify({ note: hostile }) : hostile;
		const { container } = render(<Content value={value} format={format} />);
		assertInert(container);
		expect(container.textContent).toContain("onerror");
	});

	it("inline strips to text", () => {
		const { container } = render(
			<Content
				value={`**bold** ${hostile} [x](javascript:alert(1))`}
				variant="inline"
			/>,
		);
		assertInert(container);
		expect(container.querySelectorAll("strong, a")).toHaveLength(0);
	});
});

describe("url allowlist", () => {
	it("normalizes an allowed link and refuses the rest", () => {
		expect(safeLinkUrl(" https://example.com ")).toBe("https://example.com/");
		expect(safeLinkUrl("MAILTO:user@example.com")).toBe(
			"mailto:user@example.com",
		);
		expect(safeLinkUrl("javascript:alert(1)")).toBeUndefined();
		expect(safeLinkUrl("\u0000javascript:alert(1)")).toBeUndefined();
		expect(safeLinkUrl(undefined)).toBeUndefined();
		expect(safeLinkUrl(42)).toBeUndefined();
	});

	it("holds images to https", () => {
		expect(safeImageUrl("https://example.com/a.png")).toBe(
			"https://example.com/a.png",
		);
		expect(safeImageUrl("http://example.com/a.png")).toBeUndefined();
		expect(safeImageUrl("mailto:user@example.com")).toBeUndefined();
	});
});

describe("a caller's link base", () => {
	it("resolves a relative link against the caller's base, then holds it to the allowlist", () => {
		const { container } = render(
			<Markdown linkBase="https://example.com/repo/blob/main/docs/guide.md">
				{
					"[sibling](./other.md) [up](../README.md) [js](javascript:alert(1)) [data](data:text/html,x)"
				}
			</Markdown>,
		);
		assertInert(container);
		expect(
			[...container.querySelectorAll("a")].map((a) => a.getAttribute("href")),
		).toEqual([
			"https://example.com/repo/blob/main/docs/other.md",
			"https://example.com/repo/blob/main/README.md",
		]);
	});

	it("never lets a protocol-relative link escape to another host silently", () => {
		const { container } = render(
			<Markdown linkBase="https://example.com/docs/">
				{"[x](//evil.example/p)"}
			</Markdown>,
		);
		// It resolves to https://evil.example/p: an absolute https link, which is
		// allowed, and shown as exactly that host.
		expect(container.querySelector("a")?.getAttribute("href")).toBe(
			"https://evil.example/p",
		);
	});

	it("ignores a javascript: base", () => {
		const { container } = render(
			<Markdown linkBase="javascript:alert(1)">{"[x](./a)"}</Markdown>,
		);
		expect(container.querySelectorAll("a")).toHaveLength(0);
	});
});
