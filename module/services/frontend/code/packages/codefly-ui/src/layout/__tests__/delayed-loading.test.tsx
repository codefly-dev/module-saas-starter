import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	DelayedLoading,
	LOADING_DELAY_MS,
	LOADING_MIN_VISIBLE_MS,
	Spinner,
	useDelayedLoading,
} from "../delayed-loading.js";

afterEach(cleanup);

beforeEach(() => {
	vi.useFakeTimers({ shouldAdvanceTime: true });
});
afterEach(() => {
	vi.useRealTimers();
});

const advance = async (ms: number) => {
	await act(async () => {
		await vi.advanceTimersByTimeAsync(ms);
	});
};

/** The indicator, or null. */
const indicator = () => screen.queryByRole("status");

function Harness({
	active,
	delayMs,
	minVisibleMs,
}: {
	active: boolean;
	delayMs?: number;
	minVisibleMs?: number;
}) {
	return (
		<DelayedLoading
			active={active}
			label="Loading sources"
			{...(delayMs === undefined ? {} : { delayMs })}
			{...(minVisibleMs === undefined ? {} : { minVisibleMs })}
		/>
	);
}

describe("DelayedLoading never flashes", () => {
	it("shows nothing at all for a wait shorter than the delay", async () => {
		// The rule's whole point: most waits end inside this window, and an
		// indicator that came and went inside it would tell the reader nothing
		// they could not already see.
		const { rerender } = render(<Harness active />);
		expect(indicator()).toBeNull();

		await advance(LOADING_DELAY_MS - 20);
		expect(indicator()).toBeNull();

		rerender(<Harness active={false} />);
		await advance(5_000);
		expect(indicator()).toBeNull();
	});

	it("shows nothing right up to the delay, then shows it", async () => {
		render(<Harness active />);

		await advance(LOADING_DELAY_MS - 1);
		expect(indicator()).toBeNull();

		await advance(1);
		expect(indicator()).not.toBeNull();
		expect(screen.getByText("Loading sources")).toBeTruthy();
	});

	it("holds the indicator for the minimum once it has appeared", async () => {
		// A wait that ends just past the delay would otherwise show the indicator
		// for a few milliseconds — the same blink, moved later rather than removed.
		const { rerender } = render(<Harness active />);
		await advance(LOADING_DELAY_MS);
		expect(indicator()).not.toBeNull();

		rerender(<Harness active={false} />);
		await advance(LOADING_MIN_VISIBLE_MS - 20);
		expect(indicator()).not.toBeNull();

		await advance(20);
		expect(indicator()).toBeNull();
	});

	it("holds only the remainder of the floor, measured from when it appeared", async () => {
		// The floor runs from the moment the indicator appeared, not from the
		// moment the wait began. So a wait that ends 100ms after the indicator
		// went up still owes the reader the other 200ms — and no more.
		const { rerender } = render(<Harness active />);
		await advance(LOADING_DELAY_MS);
		await advance(100);
		rerender(<Harness active={false} />);

		await advance(LOADING_MIN_VISIBLE_MS - 100 - 20);
		expect(indicator()).not.toBeNull();
		await advance(20);
		expect(indicator()).toBeNull();
	});

	it("hides immediately when the hold has already elapsed during the wait", async () => {
		const { rerender } = render(<Harness active />);
		await advance(LOADING_DELAY_MS + LOADING_MIN_VISIBLE_MS + 100);
		expect(indicator()).not.toBeNull();

		rerender(<Harness active={false} />);
		await advance(0);
		expect(indicator()).toBeNull();
	});

	it("does not restart the delay when a wait restarts while the indicator is up", async () => {
		// A series of quick refetches must not tear the indicator down and build
		// it back up; that is the flicker this primitive exists to prevent,
		// arriving by a different route.
		const { rerender } = render(<Harness active />);
		await advance(LOADING_DELAY_MS);
		expect(indicator()).not.toBeNull();

		rerender(<Harness active={false} />);
		await advance(50);
		rerender(<Harness active />);
		await advance(50);

		expect(indicator()).not.toBeNull();
	});

	it("starts a fresh delay for a wait that begins after the indicator is gone", async () => {
		const { rerender } = render(<Harness active />);
		await advance(LOADING_DELAY_MS);
		rerender(<Harness active={false} />);
		await advance(LOADING_MIN_VISIBLE_MS);
		expect(indicator()).toBeNull();

		rerender(<Harness active />);
		await advance(LOADING_DELAY_MS - 1);
		expect(indicator()).toBeNull();
		await advance(1);
		expect(indicator()).not.toBeNull();
	});

	it("honours a caller's own timings", async () => {
		render(<Harness active delayMs={1_000} minVisibleMs={50} />);

		await advance(500);
		expect(indicator()).toBeNull();
		await advance(500);
		expect(indicator()).not.toBeNull();
	});

	it("renders a caller's own indicator instead of the spinner", async () => {
		render(
			<DelayedLoading active label="Loading">
				<p>Loading data sources…</p>
			</DelayedLoading>,
		);
		await advance(LOADING_DELAY_MS);

		expect(screen.getByText("Loading data sources…")).toBeTruthy();
		// The caller owns the whole presentation here, including its own roles.
		expect(indicator()).toBeNull();
	});

	it("never mounts a caller's indicator for a wait that did not earn one", async () => {
		const { rerender } = render(
			<DelayedLoading active label="Loading">
				<p>Loading data sources…</p>
			</DelayedLoading>,
		);
		await advance(LOADING_DELAY_MS - 20);
		rerender(
			<DelayedLoading active={false} label="Loading">
				<p>Loading data sources…</p>
			</DelayedLoading>,
		);
		await advance(5_000);

		expect(screen.queryByText("Loading data sources…")).toBeNull();
	});

	it("cleans its timer up when unmounted mid-wait", async () => {
		const { unmount } = render(<Harness active />);
		unmount();
		// A surviving timer would call setState on an unmounted tree.
		await expect(advance(5_000)).resolves.toBeUndefined();
	});
});

describe("useDelayedLoading", () => {
	function Probe({ active }: { active: boolean }) {
		return <span>{useDelayedLoading(active) ? "waiting" : "idle"}</span>;
	}

	it("is the same decision without the presentation", async () => {
		const { rerender } = render(<Probe active />);
		expect(screen.getByText("idle")).toBeTruthy();

		await advance(LOADING_DELAY_MS);
		expect(screen.getByText("waiting")).toBeTruthy();

		rerender(<Probe active={false} />);
		await advance(LOADING_MIN_VISIBLE_MS);
		expect(screen.getByText("idle")).toBeTruthy();
	});
});

describe("Spinner", () => {
	it("announces politely under an accessible name", () => {
		vi.useRealTimers();
		render(<Spinner label="Loading sources" />);

		const status = screen.getByRole("status", { name: "Loading sources" });
		expect(status.getAttribute("aria-live")).toBe("polite");
		expect(status.getAttribute("aria-busy")).toBe("true");
	});

	it("fades rather than spins for a reader who asked for reduced motion", () => {
		// Rotation is vestibular-triggering. Dropping the animation entirely would
		// leave a ring that sits still and conveys nothing, so the reduced-motion
		// case fades instead — busy either way, but only one moves through space.
		vi.useRealTimers();
		const { container } = render(<Spinner label="Loading" />);

		const ring = container.querySelector("[aria-hidden='true']");
		expect(ring?.className).toContain("motion-safe:animate-spin");
		expect(ring?.className).toContain("motion-reduce:animate-pulse");
	});

	it("hides the ring itself from assistive technology", () => {
		vi.useRealTimers();
		const { container } = render(<Spinner label="Loading" />);

		// The label carries the meaning; a second announcement of the decoration
		// would read as noise.
		expect(container.querySelector("[aria-hidden='true']")).not.toBeNull();
	});
});
