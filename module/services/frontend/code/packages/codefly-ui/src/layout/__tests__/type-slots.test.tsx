// @vitest-environment happy-dom
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { Button } from "../button.js";
import { CardDescription, CardRoot, CardTitle } from "../card-root.js";
import { Input } from "../input.js";
import { Label } from "../label.js";
import { PageHeader, Section } from "../page.js";

afterEach(cleanup);

// The guard next door proves kit SOURCE names no raw type primitive. This proves
// the other half: the slot class survives `cn` and actually reaches the element.
// tailwind-merge resolves conflicts, so a component whose slot class sits beside
// a utility it conflicts with would silently lose one of the two, and the source
// would still look migrated.

describe("a migrated component renders its slot class", () => {
	it.each([
		[
			"card title",
			<CardTitle key="t">Title</CardTitle>,
			"Title",
			"type-card-title",
		],
		[
			"card description",
			<CardDescription key="d">Body</CardDescription>,
			"Body",
			"type-card-description",
		],
		["label", <Label key="l">Name</Label>, "Name", "type-label"],
	])("%s", (_name, element, text, expected) => {
		render(element);
		expect(screen.getByText(text).className).toContain(expected);
	});

	it("keeps the colour utility beside the slot", () => {
		render(<CardDescription>Body</CardDescription>);
		const className = screen.getByText("Body").className;
		expect(className).toContain("type-card-description");
		expect(className).toContain("text-muted-foreground");
	});

	it("gives the page and section headings their own slots", () => {
		render(
			<>
				<PageHeader title="Users" />
				<Section title="Members" description="Who can sign in" />
			</>,
		);
		expect(screen.getByText("Users").className).toContain("type-page-title");
		expect(screen.getByText("Members").className).toContain(
			"type-section-title",
		);
		expect(screen.getByText("Who can sign in").className).toContain(
			"type-section-description",
		);
	});

	it("switches the card title's slot with the card's size", () => {
		render(
			<CardRoot size="sm">
				<CardTitle>Compact</CardTitle>
			</CardRoot>,
		);
		expect(screen.getByText("Compact").className).toContain(
			"group-data-[size=sm]/card:type-card-title-sm",
		);
	});
});

describe("a control renders its rung, not a literal size", () => {
	it.each([
		["default", undefined, "control-default"],
		["sm", "sm" as const, "control-sm"],
		["xs", "xs" as const, "control-xs"],
		["lg", "lg" as const, "control-lg"],
	])("size %s", (_name, size, expected) => {
		render(<Button size={size}>Go</Button>);
		const className = screen.getByRole("button").className;
		expect(className).toContain(expected);
		expect(className).not.toMatch(/\bh-\d/);
		expect(className).not.toMatch(/\btext-(?:xs|sm|base)\b/);
	});

	it("renders an icon-only button as a square rung", () => {
		render(<Button size="icon" aria-label="Open" />);
		expect(screen.getByRole("button").className).toContain(
			"control-icon-default",
		);
	});

	// The input keeps its touch size below `md` and its smaller size above, which
	// is two slots rather than a breakpoint hidden inside one role.
	it("carries both input slots across the breakpoint", () => {
		render(<Input aria-label="Email" />);
		const className = screen.getByLabelText("Email").className;
		expect(className).toContain("type-input-touch");
		expect(className).toContain("md:type-input");
		expect(className).toContain("control-height-default");
	});

	// A caller override must still win. This is the regression tailwind-merge
	// would otherwise cause: it cannot know a custom utility sets font size.
	it("lets a caller override the rung's type", () => {
		render(
			<Button size="sm" className="text-lg">
				Go
			</Button>,
		);
		const className = screen.getByRole("button").className;
		expect(className).toContain("text-lg");
		expect(className).not.toContain("control-sm");
	});
});
