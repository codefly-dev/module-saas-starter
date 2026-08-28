import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
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
afterEach(() => window.localStorage.clear());

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
});
