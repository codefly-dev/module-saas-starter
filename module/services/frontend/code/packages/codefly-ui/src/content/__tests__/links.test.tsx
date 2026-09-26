// @vitest-environment happy-dom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Content } from "../content.js";
import {
	findHeading,
	headingSlug,
	type LinkResolver,
	resolveRelativeLink,
} from "../links.js";
import { Markdown } from "../markdown.js";

afterEach(cleanup);

describe("resolveRelativeLink", () => {
	const doc = "docs/guide/intro.md";
	it.each([
		["setup.md", "docs/guide/setup.md", ""],
		["./setup.md", "docs/guide/setup.md", ""],
		["../reference/api.md", "docs/reference/api.md", ""],
		["setup.md#install-the-cli", "docs/guide/setup.md", "install-the-cli"],
		["/README.md", "README.md", ""],
		["../../README.md", "README.md", ""],
		["#usage", "docs/guide/intro.md", "usage"],
		["setup.md?plain=1#x", "docs/guide/setup.md", "x"],
		["my%20notes.md#caf%C3%A9", "docs/guide/my notes.md", "café"],
		["a//b/./c.md", "docs/guide/a/b/c.md", ""],
	])("%s from %s", (href, path, fragment) => {
		expect(resolveRelativeLink(doc, href)).toEqual({
			path,
			fragment,
			directory: false,
		});
	});

	it("marks a folder link", () => {
		expect(resolveRelativeLink(doc, "../reference/")).toEqual({
			path: "docs/reference",
			fragment: "",
			directory: true,
		});
		expect(resolveRelativeLink(doc, "..")).toEqual({
			path: "docs",
			fragment: "",
			directory: true,
		});
	});

	it("refuses an escaped separator instead of resolving through it", () => {
		// `..%2f..%2f..%2f..%2f` used to decode AFTER the split, so the `..`
		// segments were pushed whole and rejoined: the caller was handed
		// "docs/guide/../../../../etc/passwd", which normalizes to /etc/passwd —
		// out of the repository the plain spelling is refused for.
		expect(
			resolveRelativeLink(doc, "..%2f..%2f..%2f..%2fetc%2fpasswd"),
		).toBeNull();
		// A percent-escape of an ordinary character still resolves.
		expect(resolveRelativeLink(doc, "my%20notes.md")?.path).toBe(
			"docs/guide/my notes.md",
		);
	});

	it("resolves from a document at the repository root", () => {
		expect(resolveRelativeLink("README.md", "docs/a.md")?.path).toBe(
			"docs/a.md",
		);
	});

	it.each([
		"https://example.com/a.md",
		"mailto:user@example.com",
		"javascript:alert(1)",
		"JavaScript:alert(1)",
		"data:text/html,x",
		"//evil.example/a.md",
		"",
		"   ",
		"../../../escape.md",
		// A percent-escaped separator must not smuggle a `..` past the climb
		// check: each of these is the refused spelling above, re-encoded.
		"..%2f..%2f..%2fescape.md",
		"..%2F..%2F..%2Fescape.md",
		"%2e%2e%2f%2e%2e%2f%2e%2e%2fescape.md",
		"a%2F..%2F..%2F..%2F..%2Fescape.md",
		"%2F%2Fevil.example/a.md",
		"%E0%A4%A.md",
		"a.md#%E0%A4%A",
	])("leaves %j alone", (href) => {
		expect(resolveRelativeLink(doc, href)).toBeNull();
	});
});

describe("headingSlug", () => {
	it.each([
		["Install the CLI", "install-the-cli"],
		["Set up: step 2", "set-up-step-2"],
		["  Café & crème ", "café--crème"],
		["snake_case_name", "snake_case_name"],
	])("%j → %j", (text, slug) => {
		expect(headingSlug(text)).toBe(slug);
	});
});

describe("a caller's link resolver", () => {
	it("opens an in-product target without navigating", () => {
		const open = vi.fn();
		const resolve: LinkResolver = (href) =>
			href === "setup.md" ? { open, title: "Open setup.md" } : undefined;
		const { container } = render(
			<Markdown resolveLink={resolve}>{"See [setup](setup.md)."}</Markdown>,
		);
		expect(container.querySelector("a")).toBeNull();
		const link = screen.getByRole("button", { name: "setup" });
		expect(link.getAttribute("title")).toBe("Open setup.md");
		fireEvent.click(link);
		expect(open).toHaveBeenCalledTimes(1);
	});

	it("receives the href exactly as written, before any base", () => {
		const seen: string[] = [];
		render(
			<Markdown
				linkBase="https://example.com/repo/blob/main/docs/guide.md"
				resolveLink={(href) => {
					seen.push(href);
					return undefined;
				}}
			>
				{"[a](../a.md#x) [b](https://example.com/b)"}
			</Markdown>,
		);
		expect(seen).toEqual(["../a.md#x", "https://example.com/b"]);
	});

	it("says an unavailable target in words and keeps it inert", () => {
		const { container } = render(
			<Markdown resolveLink={() => ({ unavailable: "Not in this collection" })}>
				{"[gone](gone.md)"}
			</Markdown>,
		);
		expect(container.querySelector("a, button")).toBeNull();
		const text = container.querySelector(
			'[data-slot="content-link-unavailable"]',
		);
		expect(text?.getAttribute("title")).toBe("Not in this collection");
		expect(text?.textContent).toBe("gone (Not in this collection)");
	});

	it("falls back to the default rule, base and allowlist included", () => {
		const { container } = render(
			<Markdown
				linkBase="https://example.com/repo/blob/main/docs/guide.md"
				resolveLink={() => undefined}
			>
				{"[s](./other.md) [x](javascript:alert(1)) [e](https://example.com/e)"}
			</Markdown>,
		);
		expect(
			[...container.querySelectorAll("a")].map((a) => a.getAttribute("href")),
		).toEqual([
			"https://example.com/repo/blob/main/docs/other.md",
			"https://example.com/e",
		]);
		for (const a of container.querySelectorAll("a"))
			expect(a.getAttribute("rel")).toBe("noopener noreferrer");
	});

	it("reaches markdown through Content", () => {
		const open = vi.fn();
		render(
			<Content
				value={"[next](next.md)"}
				format="markdown"
				resolveLink={() => ({ open })}
			/>,
		);
		fireEvent.click(screen.getByRole("button", { name: "next" }));
		expect(open).toHaveBeenCalled();
	});
});

describe("findHeading", () => {
	it("finds the heading a fragment names in rendered content", () => {
		const { container } = render(
			<Markdown>{"# Intro\n\n## Install the CLI\n\ntext"}</Markdown>,
		);
		expect(findHeading(container, "install-the-cli")?.textContent).toBe(
			"Install the CLI",
		);
		expect(findHeading(container, "missing")).toBeNull();
		expect(findHeading(container, "")).toBeNull();
	});

	it("writes no ids into the page", () => {
		const { container } = render(<Markdown>{"## Install the CLI"}</Markdown>);
		expect(container.querySelector("[id]")).toBeNull();
	});
});

describe("GFM footnotes", () => {
	const SOURCE = "Claim.[^a]\n\n[^a]: The source.\n";

	// Both of a footnote's links are `#fragment` hrefs the kit generates itself.
	// They used to reach `safeLinkUrl`, which refuses a relative URL, so the
	// marker and its `↩` back-reference both rendered as inert text: a
	// superscript that did nothing above a section you could not get back from.
	it("makes its marker and back-reference live, and targets them", () => {
		const { container } = render(<Markdown>{SOURCE}</Markdown>);
		const anchors = [...container.querySelectorAll("a")];
		expect(anchors).toHaveLength(2);
		const [marker, backref] = anchors;

		// Each href resolves to an element that exists in this block.
		for (const anchor of anchors) {
			const href = anchor.getAttribute("href") ?? "";
			expect(href.startsWith("#")).toBe(true);
			expect(container.querySelector(`[id="${href.slice(1)}"]`)).not.toBeNull();
		}
		// A same-page anchor is not opened in a new browsing context.
		expect(marker?.getAttribute("target")).toBeNull();
		expect(backref?.getAttribute("target")).toBeNull();
	});

	// The ids to-hast derives from the document used to land on the page under
	// the well-known, shared `user-content-` prefix, and the footnote heading's
	// `footnote-label` was hard-coded and unprefixed — so two blocks on one page
	// emitted the same ids.
	it("namespaces every id it emits, per rendered block", () => {
		const { container } = render(
			<div>
				<Markdown>{SOURCE}</Markdown>
				<Markdown>{SOURCE}</Markdown>
			</div>,
		);
		const ids = [...container.querySelectorAll("[id]")].map((e) => e.id);
		expect(ids.length).toBeGreaterThan(0);
		expect(new Set(ids).size).toBe(ids.length);
		for (const id of ids) {
			expect(id).not.toContain("user-content-");
			expect(id).not.toBe("footnote-label");
			expect(id).toMatch(/^content-[a-zA-Z0-9]+-/);
		}
	});

	it("refuses a fragment the content wrote itself", () => {
		// Only an anchor carrying THIS block's minted prefix is live; a document
		// cannot guess it, and an in-page jump it wrote stays inert.
		const { container } = render(
			<Markdown>{"[jump](#content-r0-fn-a) [top](#top)"}</Markdown>,
		);
		expect(container.querySelectorAll("a")).toHaveLength(0);
		expect(
			container.querySelectorAll('[data-slot="content-link-inert"]'),
		).toHaveLength(2);
	});
});
