// @vitest-environment happy-dom
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { Badge, type StatusTone } from "../badge.js";
import { Banner } from "../banner.js";

afterEach(cleanup);

const TONES: StatusTone[] = ["neutral", "success", "warning", "danger", "info"];

function classesOf(text: string): string[] {
	return (screen.getByText(text).closest("[data-slot=badge]")?.className ?? "")
		.split(/\s+/)
		.filter(Boolean);
}

describe("Badge tone", () => {
	// The bug the tone exists to fix: Set up, Connected and Error rendered as
	// outline, secondary and destructive, and secondary read as "nothing here".
	it("paints three statuses three ways", () => {
		render(
			<>
				<Badge tone="neutral">Set up</Badge>
				<Badge tone="success">Connected</Badge>
				<Badge tone="danger">Error</Badge>
			</>,
		);
		expect(classesOf("Set up")).toContain("text-muted-foreground");
		expect(classesOf("Connected")).toContain("text-success");
		expect(classesOf("Error")).toContain(
			"[color:color-mix(in_oklab,var(--destructive)_70%,var(--foreground))]",
		);
	});

	it.each([
		["success", "bg-success/10"],
		["warning", "bg-warning/10"],
		["danger", "bg-destructive/10"],
		["info", "bg-info/10"],
		["neutral", "bg-muted"],
	] as const)("tints %s with its own token", (tone, tint) => {
		render(<Badge tone={tone}>Status</Badge>);
		expect(classesOf("Status")).toContain(tint);
	});

	// Two fills on one badge would resolve by class order, not by intent.
	it("replaces the variant's colours rather than stacking over them", () => {
		render(
			<Badge variant="default" tone="success">
				Healthy
			</Badge>,
		);
		const classes = classesOf("Healthy");
		expect(classes).not.toContain("bg-primary");
		expect(classes).not.toContain("text-primary-foreground");
	});

	it("keeps the variant when no tone is given", () => {
		render(<Badge variant="outline">Pending</Badge>);
		expect(classesOf("Pending")).toContain("border-border");
	});

	it("exposes the tone and size for a skin rule or a test to read", () => {
		render(
			<Badge tone="warning" size="lg">
				Paused
			</Badge>,
		);
		const badge = screen.getByText("Paused");
		expect(badge.getAttribute("data-tone")).toBe("warning");
		expect(badge.getAttribute("data-size")).toBe("lg");
	});
});

describe("Badge dot", () => {
	it("is decorative, so the status is the text and not the colour", () => {
		const { container } = render(
			<Badge tone="success" dot>
				Connected
			</Badge>,
		);
		const dot = container.querySelector("[data-slot=badge-dot]");
		expect(dot?.getAttribute("aria-hidden")).toBe("true");
		expect(dot?.className).toContain("bg-current");
		expect(screen.getByText("Connected").textContent).toBe("Connected");
	});

	it("is absent unless asked for", () => {
		const { container } = render(<Badge tone="success">Connected</Badge>);
		expect(container.querySelector("[data-slot=badge-dot]")).toBeNull();
	});

	it.each([
		["sm", "size-1"],
		["default", "size-1.5"],
		["lg", "size-2"],
	] as const)("scales with a %s badge", (size, dotClass) => {
		const { container } = render(
			<Badge size={size} dot>
				Live
			</Badge>,
		);
		expect(
			container.querySelector("[data-slot=badge-dot]")?.className,
		).toContain(dotClass);
	});
});

describe("Badge size", () => {
	it.each([
		["sm", "h-4"],
		["default", "h-5"],
		["lg", "h-6"],
	] as const)("%s sets the badge's height", (size, height) => {
		render(<Badge size={size}>Label</Badge>);
		expect(classesOf("Label")).toContain(height);
	});

	it("takes the larger type slot only at lg", () => {
		render(
			<>
				<Badge size="lg">Large</Badge>
				<Badge>Regular</Badge>
			</>,
		);
		expect(classesOf("Large")).toContain("type-badge-lg");
		expect(classesOf("Large")).not.toContain("type-badge");
		expect(classesOf("Regular")).toContain("type-badge");
	});
});

describe("Banner tone", () => {
	it("keeps the original look and no glyph when no tone is given", () => {
		const { container } = render(<Banner title="Source updated" />);
		const banner = container.querySelector("[data-slot=banner]");
		expect(banner?.getAttribute("data-tone")).toBe("neutral");
		expect(banner?.className).toContain("bg-primary/5");
		expect(container.querySelector("[data-slot=banner-icon]")).toBeNull();
	});

	it.each(TONES.filter((tone) => tone !== "neutral"))(
		"gives %s a glyph, so it is told apart by shape",
		(tone) => {
			const { container } = render(<Banner tone={tone} title="Heads up" />);
			const icon = container.querySelector("[data-slot=banner-icon]");
			expect(icon?.querySelector("svg")).toBeTruthy();
			expect(icon?.getAttribute("aria-hidden")).toBe("true");
		},
	);

	it("gives each status tone a different glyph", () => {
		const glyphs = TONES.filter((tone) => tone !== "neutral").map((tone) => {
			const { container, unmount } = render(<Banner tone={tone} title="x" />);
			const svg = container.querySelector("[data-slot=banner-icon] svg");
			const shape = svg?.getAttribute("class") ?? "";
			unmount();
			return shape;
		});
		expect(new Set(glyphs).size).toBe(glyphs.length);
	});

	it("interrupts for a fault and waits for anything else", () => {
		render(
			<>
				<Banner tone="danger" title="Nothing reached the host" />
				<Banner tone="warning" title="Local preview" />
			</>,
		);
		expect(screen.getByRole("alert").textContent).toContain(
			"Nothing reached the host",
		);
		expect(screen.getByRole("status").textContent).toContain("Local preview");
	});

	it("lets a caller replace or remove the glyph", () => {
		const { container, rerender } = render(
			<Banner tone="info" icon={<span data-testid="own" />} title="x" />,
		);
		expect(screen.getByTestId("own")).toBeTruthy();
		rerender(<Banner tone="info" icon={null} title="x" />);
		expect(container.querySelector("[data-slot=banner-icon]")).toBeNull();
	});
});
