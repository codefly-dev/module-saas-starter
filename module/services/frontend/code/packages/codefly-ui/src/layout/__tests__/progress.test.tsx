// @vitest-environment happy-dom
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { Progress } from "../progress.js";

afterEach(cleanup);

describe("Progress", () => {
	it("exposes the value on the progressbar role, named", () => {
		render(<Progress value={40} label="Sync progress" />);

		const bar = screen.getByRole("progressbar", { name: "Sync progress" });
		expect(bar.getAttribute("aria-valuenow")).toBe("40");
		expect(bar.getAttribute("aria-valuemin")).toBe("0");
		expect(bar.getAttribute("aria-valuemax")).toBe("100");
	});

	it("prefers the caller's words over the bare percentage", () => {
		render(
			<Progress value={50} label="Sync progress" valueText="Fetching files" />,
		);

		expect(screen.getByRole("progressbar").getAttribute("aria-valuetext")).toBe(
			"Fetching files",
		);
	});

	it("omits aria-valuetext entirely when there are no words for it", () => {
		// An empty aria-valuetext would be read in place of the percentage, which
		// leaves a screen-reader user with neither.
		render(<Progress value={50} label="Sync progress" />);

		expect(screen.getByRole("progressbar").hasAttribute("aria-valuetext")).toBe(
			false,
		);
	});

	it("clamps a value outside the track rather than overflowing it", () => {
		const { rerender } = render(<Progress value={140} label="Over" />);
		expect(screen.getByRole("progressbar").getAttribute("aria-valuenow")).toBe(
			"100",
		);

		rerender(<Progress value={-20} label="Under" />);
		expect(screen.getByRole("progressbar").getAttribute("aria-valuenow")).toBe(
			"0",
		);
	});

	it("rounds a fractional value, so the fill and the announcement agree", () => {
		render(<Progress value={33.4} label="Third" />);

		const bar = screen.getByRole("progressbar");
		expect(bar.getAttribute("aria-valuenow")).toBe("33");
		expect(
			bar.querySelector<HTMLElement>("[data-slot='progress-fill']")?.style
				.width,
		).toBe("33%");
	});

	it("divides the track into the steps it was given, decoratively", () => {
		render(<Progress value={50} label="Phases" steps={4} />);

		const steps = screen
			.getByRole("progressbar")
			.querySelector("[data-slot='progress-steps']");
		// One fewer divider than segments, and hidden: the ladder is conveyed by
		// the label and aria-valuetext, so announcing four empty divs repeats
		// nothing useful.
		expect(steps?.childElementCount).toBe(3);
		expect(steps?.getAttribute("aria-hidden")).toBe("true");
	});

	it("draws no dividers for a single step", () => {
		render(<Progress value={50} label="One" steps={1} />);

		expect(
			screen
				.getByRole("progressbar")
				.querySelector("[data-slot='progress-steps']"),
		).toBeNull();
	});

	it("carries a tone class so a stalled bar is not read by colour alone as healthy", () => {
		const { rerender } = render(
			<Progress value={50} label="Warn" tone="warning" />,
		);
		const fill = () =>
			screen
				.getByRole("progressbar")
				.querySelector<HTMLElement>("[data-slot='progress-fill']");
		expect(fill()?.className).toContain("amber");

		rerender(<Progress value={50} label="Bad" tone="danger" />);
		expect(fill()?.className).toContain("destructive");
	});
});
