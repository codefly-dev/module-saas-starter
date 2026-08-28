import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { dashboard, event, metric } from "../../model/schema";
import { DashboardSpecError } from "../../model/validate";
import { useDashboardDraft } from "../use-dashboard-draft";

const KEY = "dashboard:test";

const initial = dashboard({
	title: "Initial",
	metrics: [
		metric({ title: "Top events", groupBy: "event_type", chart: "bar" }),
	],
});

const edited = dashboard({
	title: "Edited",
	metrics: [
		metric({
			title: "Logins over time",
			event: event("auth.login"),
			groupBy: "time",
			bucket: "day",
			chart: "line",
		}),
	],
});

beforeEach(() => window.localStorage.clear());
afterEach(() => {
	vi.restoreAllMocks();
	window.localStorage.clear();
});

describe("useDashboardDraft", () => {
	it("starts from the initial spec when no draft is stored", () => {
		const { result } = renderHook(() => useDashboardDraft(KEY, initial));
		expect(result.current.spec).toEqual(initial);
		expect(result.current.error).toBeNull();
	});

	it("persists a valid set and exposes it as the active spec", () => {
		const { result } = renderHook(() => useDashboardDraft(KEY, initial));
		act(() => result.current.setSpec(edited));
		expect(result.current.spec).toEqual(edited);
		expect(result.current.error).toBeNull();
		expect(JSON.parse(window.localStorage.getItem(KEY)!)).toEqual(edited);
	});

	it("restores a persisted draft on the next mount, surviving a reload", () => {
		window.localStorage.setItem(KEY, JSON.stringify(edited));
		const { result } = renderHook(() => useDashboardDraft(KEY, initial));
		expect(result.current.spec).toEqual(edited);
		expect(result.current.error).toBeNull();
	});

	it("rejects an invalid set without disturbing the active spec", () => {
		const { result } = renderHook(() => useDashboardDraft(KEY, initial));
		const broken = {
			...edited,
			metrics: [{ title: "x", groupBy: "time", chart: "line" }],
		} as unknown as typeof edited;
		act(() => result.current.setSpec(broken));
		expect(result.current.spec).toEqual(initial);
		expect(result.current.error).toBeInstanceOf(DashboardSpecError);
		expect(window.localStorage.getItem(KEY)).toBeNull();
	});

	it("surfaces a corrupt persisted draft as an error and keeps the initial spec", () => {
		window.localStorage.setItem(KEY, "{ not json");
		const { result } = renderHook(() => useDashboardDraft(KEY, initial));
		expect(result.current.spec).toEqual(initial);
		expect(result.current.error).toBeInstanceOf(DashboardSpecError);
	});

	it("clears the last error on the next valid set", () => {
		const { result } = renderHook(() => useDashboardDraft(KEY, initial));
		act(() =>
			result.current.setSpec({ version: 99 } as unknown as typeof edited),
		);
		expect(result.current.error).toBeInstanceOf(DashboardSpecError);
		act(() => result.current.setSpec(edited));
		expect(result.current.error).toBeNull();
		expect(result.current.spec).toEqual(edited);
	});

	it("reset discards the persisted draft and returns to the initial spec", () => {
		const { result } = renderHook(() => useDashboardDraft(KEY, initial));
		act(() => result.current.setSpec(edited));
		act(() => result.current.reset());
		expect(result.current.spec).toEqual(initial);
		expect(result.current.error).toBeNull();
		expect(window.localStorage.getItem(KEY)).toBeNull();
	});

	it("keeps a valid edit when persisting it throws (quota/blocked storage)", () => {
		const { result } = renderHook(() => useDashboardDraft(KEY, initial));
		vi.spyOn(window.localStorage, "setItem").mockImplementation(() => {
			throw new DOMException("quota", "QuotaExceededError");
		});
		act(() => result.current.setSpec(edited));
		// The edit is applied in memory (never lost)...
		expect(result.current.spec).toEqual(edited);
		// ...and the persistence failure surfaces as a non-validation error.
		expect(result.current.error).toBeInstanceOf(Error);
		expect(result.current.error).not.toBeInstanceOf(DashboardSpecError);
	});

	it("surfaces a storage read failure without throwing out of the effect", () => {
		vi.spyOn(window.localStorage, "getItem").mockImplementation(() => {
			throw new DOMException("blocked", "SecurityError");
		});
		const { result } = renderHook(() => useDashboardDraft(KEY, initial));
		expect(result.current.spec).toEqual(initial);
		expect(result.current.error).toBeInstanceOf(Error);
		expect(result.current.error).not.toBeInstanceOf(DashboardSpecError);
	});

	it("surfaces an invalid initial spec instead of crashing the render", () => {
		const badInitial = {
			...initial,
			metrics: [{ title: "x", groupBy: "time", chart: "line" }],
		} as unknown as typeof initial;
		const { result } = renderHook(() => useDashboardDraft(KEY, badInitial));
		expect(result.current.spec).toBe(badInitial);
		expect(result.current.error).toBeInstanceOf(DashboardSpecError);
	});

	it("picks up a valid draft written by another tab via the storage event", () => {
		const { result } = renderHook(() => useDashboardDraft(KEY, initial));
		act(() => {
			window.dispatchEvent(
				new StorageEvent("storage", {
					key: KEY,
					newValue: JSON.stringify(edited),
				}),
			);
		});
		expect(result.current.spec).toEqual(edited);
		expect(result.current.error).toBeNull();
	});

	it("returns to the initial spec when another tab clears the key", () => {
		window.localStorage.setItem(KEY, JSON.stringify(edited));
		const { result } = renderHook(() => useDashboardDraft(KEY, initial));
		expect(result.current.spec).toEqual(edited);
		act(() => {
			window.dispatchEvent(
				new StorageEvent("storage", { key: KEY, newValue: null }),
			);
		});
		expect(result.current.spec).toEqual(initial);
	});

	it("ignores storage events for a different key", () => {
		const { result } = renderHook(() => useDashboardDraft(KEY, initial));
		act(() => {
			window.dispatchEvent(
				new StorageEvent("storage", {
					key: "some:other:key",
					newValue: JSON.stringify(edited),
				}),
			);
		});
		expect(result.current.spec).toEqual(initial);
	});

	it("accepts a runtime set that removes the last widget", () => {
		const { result } = renderHook(() => useDashboardDraft(KEY, initial));
		const emptied = dashboard({ title: "Emptied", metrics: [] });
		act(() => result.current.setSpec(emptied));
		expect(result.current.spec).toEqual(emptied);
		expect(result.current.error).toBeNull();
	});

	it("reset returns to the latest initial, not the one frozen at first mount", () => {
		const second = dashboard({
			title: "Second",
			metrics: [
				metric({ title: "By category", groupBy: "category", chart: "bar" }),
			],
		});
		const { result, rerender } = renderHook(
			({ init }) => useDashboardDraft(KEY, init),
			{ initialProps: { init: initial } },
		);
		rerender({ init: second });
		act(() => result.current.reset());
		expect(result.current.spec).toEqual(second);
	});
});
