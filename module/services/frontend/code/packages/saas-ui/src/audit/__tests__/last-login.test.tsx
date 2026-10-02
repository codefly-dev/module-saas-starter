import { act, cleanup, render, screen } from "@testing-library/react";
import { renderToString } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LastLogin, relativeTime } from "../last-login.js";

afterEach(() => {
	cleanup();
	vi.useRealTimers();
});

const at = new Date("2026-10-02T10:00:00Z");
const after = (seconds: number) => new Date(at.getTime() + seconds * 1000);

describe("relativeTime", () => {
	it("says how long ago, in the largest unit that fits", () => {
		expect(relativeTime(at, after(12), "en")).toBe("12 seconds ago");
		expect(relativeTime(at, after(90), "en")).toBe("1 minute ago");
		expect(relativeTime(at, after(120), "en")).toBe("2 minutes ago");
		expect(relativeTime(at, after(3 * 3600), "en")).toBe("3 hours ago");
		expect(relativeTime(at, after(26 * 3600), "en")).toBe("yesterday");
		expect(relativeTime(at, after(10 * 24 * 3600), "en")).toBe("last week");
	});

	it("counts whole units elapsed, never rounding up to the next", () => {
		const almost = (seconds: number) => new Date(at.getTime() + seconds * 1000 - 1);
		expect(relativeTime(at, almost(60), "en")).toBe("59 seconds ago");
		expect(relativeTime(at, after(60), "en")).toBe("1 minute ago");
		expect(relativeTime(at, almost(3600), "en")).toBe("59 minutes ago");
		expect(relativeTime(at, after(3600), "en")).toBe("1 hour ago");
	});

	it("says now for this second, and for a clock that runs ahead", () => {
		expect(relativeTime(at, at, "en")).toBe("now");
		expect(relativeTime(after(5), at, "en")).toBe("now");
	});
});

describe("<LastLogin>", () => {
	it("says when, with the exact time on the element and beside it", () => {
		render(<LastLogin at={at.toISOString()} subject="ana@example.com" now={() => after(12)} />);
		expect(screen.getByText("Last login")).toBeTruthy();
		const time = screen.getByText(/12 seconds ago/);
		expect(time.tagName).toBe("TIME");
		expect(time.getAttribute("datetime")).toBe(at.toISOString());
		expect(screen.getByText(/ana@example\.com/)).toBeTruthy();
	});

	it("stays true as time passes", () => {
		vi.useFakeTimers();
		vi.setSystemTime(after(10));
		render(<LastLogin at={at} />);
		expect(screen.getByText(/10 seconds ago/)).toBeTruthy();
		act(() => {
			vi.advanceTimersByTime(5_000);
		});
		expect(screen.getByText(/15 seconds ago/)).toBeTruthy();
	});

	// The server and the browser's hydration render must say the same thing,
	// whatever either one's clock or timezone: the time in UTC. The relative
	// words and the viewer's local time follow once mounted.
	it("renders the same first text on any clock, so hydration matches", () => {
		const html = renderToString(<LastLogin at={at} now={() => after(12)} />);
		expect(html).toContain("2026-10-02 10:00 UTC");
		expect(html).not.toContain("seconds ago");
		expect(renderToString(<LastLogin at={at} now={() => after(999)} />)).toBe(html);
	});

	// A parent that re-renders more often than the words change, handing a new
	// clock function each time, must not hold them still.
	it("keeps moving while its parent re-renders every five seconds", () => {
		vi.useFakeTimers();
		vi.setSystemTime(after(5 * 60));
		const { rerender } = render(<LastLogin at={at} now={() => new Date()} />);
		expect(screen.getByText("5 minutes ago")).toBeTruthy();
		for (let i = 0; i < 24; i++) {
			act(() => {
				vi.advanceTimersByTime(5_000);
			});
			rerender(<LastLogin at={at} now={() => new Date()} />);
		}
		expect(screen.getByText("7 minutes ago")).toBeTruthy();
	});

	it("says no sign-in is recorded, never an invalid date", () => {
		render(<LastLogin at={null} />);
		expect(screen.getByText("Never")).toBeTruthy();
		expect(screen.queryByText(/Invalid Date|NaN/)).toBeNull();
		cleanup();
		render(<LastLogin at="not a date" />);
		expect(screen.getByText("Never")).toBeTruthy();
	});
});
