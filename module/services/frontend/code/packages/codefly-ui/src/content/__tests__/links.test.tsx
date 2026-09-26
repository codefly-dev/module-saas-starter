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
