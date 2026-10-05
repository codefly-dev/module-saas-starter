// @vitest-environment happy-dom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Chip, ChipGroup } from "../chip.js";

afterEach(cleanup);

describe("Chip", () => {
	it("is a link when given an href", () => {
		render(<Chip href="/proposals/p-1">Pricing change</Chip>);
		const link = screen.getByRole("link", { name: "Pricing change" });
		expect(link.getAttribute("href")).toBe("/proposals/p-1");
	});

	it("is a button when given onClick, and says it is one", () => {
		const onClick = vi.fn();
		render(<Chip onClick={onClick}>Filter: open</Chip>);
		const button = screen.getByRole("button", { name: "Filter: open" });
		expect(button.getAttribute("type")).toBe("button");
		fireEvent.click(button);
		expect(onClick).toHaveBeenCalledOnce();
	});

	it("is plain text when neither is given", () => {
		render(<Chip>Read only</Chip>);
		expect(screen.queryByRole("link")).toBeNull();
		expect(screen.queryByRole("button")).toBeNull();
		expect(screen.getByText("Read only")).toBeTruthy();
	});

	it("renders a router link in place of the anchor", () => {
		render(
			<Chip render={<a href="/routed" data-router="yes" />}>Routed</Chip>,
		);
		const link = screen.getByRole("link", { name: "Routed" });
		expect(link.getAttribute("data-router")).toBe("yes");
		expect(link.getAttribute("data-slot")).toBe("chip-action");
	});

	it("puts its meta after a separator a screen reader skips", () => {
		const { container } = render(
			<Chip href="/p/2" meta="unread">
				Pricing change
			</Chip>,
		);
		const meta = container.querySelector("[data-slot=chip-meta]");
		expect(meta?.textContent).toBe("· unread");
		expect(meta?.querySelector("[aria-hidden]")?.textContent).toBe("· ");
		expect(screen.getByRole("link").textContent).toContain("unread");
	});

	it("keeps its leading icon out of the accessible name", () => {
		const { container } = render(
			<Chip icon={<svg data-testid="glyph" />}>Owner</Chip>,
		);
		expect(
			container.querySelector("[data-slot=chip-icon]")?.getAttribute(
				"aria-hidden",
			),
		).toBe("true");
	});

	// A button inside a link is two targets announced as one.
	it("places remove beside the link, never inside it", () => {
		const onRemove = vi.fn();
		render(
			<Chip href="/p/3" onRemove={onRemove} removeLabel="Remove Pricing change">
				Pricing change
			</Chip>,
		);
		const remove = screen.getByRole("button", {
			name: "Remove Pricing change",
		});
		expect(screen.getByRole("link").contains(remove)).toBe(false);
		fireEvent.click(remove);
		expect(onRemove).toHaveBeenCalledOnce();
	});

	// WCAG 2.4.7: the ring is drawn outside the control, so anything between
	// the control and the chip's edge that clips overflow hides it.
	it("draws a keyboard focus ring nothing in the chip crops", () => {
		const { container } = render(
			<Chip href="/p/5" onRemove={() => {}} removeLabel="Remove Owner">
				Owner
			</Chip>,
		);
		const chip = container.querySelector("[data-slot=chip]") as HTMLElement;
		for (const control of [
			screen.getByRole("link"),
			screen.getByRole("button", { name: "Remove Owner" }),
		]) {
			expect(control.className).toContain("focus-visible:ring-[3px]");
			for (
				let element: HTMLElement | null = control;
				element && chip.contains(element);
				element = element.parentElement
			)
				expect(element.className).not.toMatch(/overflow-(hidden|clip)/);
		}
	});

	it("has no remove control unless asked", () => {
		render(<Chip href="/p/4">Pricing change</Chip>);
		expect(screen.queryByRole("button")).toBeNull();
	});

	it.each([
		["neutral", "border-border"],
		["success", "text-success"],
		["warning", "text-warning"],
		["danger", "text-destructive"],
		["info", "text-info"],
	] as const)("carries the %s tone", (tone, cls) => {
		const { container } = render(<Chip tone={tone}>Status</Chip>);
		const chip = container.querySelector("[data-slot=chip]");
		expect(chip?.getAttribute("data-tone")).toBe(tone);
		expect(chip?.className).toContain(cls);
	});

	it("only shows a hover affordance when there is something to press", () => {
		const { container } = render(
			<>
				<Chip>Static</Chip>
				<Chip href="/x">Linked</Chip>
			</>,
		);
		const [still, linked] = container.querySelectorAll(
			"[data-slot=chip-action]",
		);
		expect(still.className).not.toContain("cursor-pointer");
		expect(linked.className).toContain("cursor-pointer");
	});
});

describe("ChipGroup", () => {
	it("is a named list with one item per chip", () => {
		render(
			<ChipGroup label="Affects">
				<Chip href="/a">Billing</Chip>
				<Chip href="/b">Onboarding</Chip>
				{null}
			</ChipGroup>,
		);
		const list = screen.getByRole("list", { name: "Affects" });
		expect(list.querySelectorAll(":scope > li")).toHaveLength(2);
	});
});
