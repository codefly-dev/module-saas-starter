import { act, cleanup, render, screen } from "@testing-library/react";
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

	it("says no sign-in is recorded, never an invalid date", () => {
		render(<LastLogin at={null} />);
		expect(screen.getByText("Never")).toBeTruthy();
		expect(screen.queryByText(/Invalid Date|NaN/)).toBeNull();
		cleanup();
		render(<LastLogin at="not a date" />);
		expect(screen.getByText("Never")).toBeTruthy();
	});
});
