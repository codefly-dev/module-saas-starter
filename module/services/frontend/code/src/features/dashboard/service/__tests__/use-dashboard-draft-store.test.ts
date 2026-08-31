import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { dashboard, event, metric } from "../../model/schema";
import { DashboardSpecError } from "../../model/validate";
import {
	createMemoryDashboardDraftStore,
	type DashboardDraftStore,
} from "../draft-store";
import { useDashboardDraft } from "../use-dashboard-draft";

const KEY = "dashboard:injected";

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

const stored = dashboard({
	title: "Stored",
	metrics: [
		metric({ title: "By category", groupBy: "category", chart: "bar" }),
	],
});

// A store whose reads and writes complete on a later microtask, standing in for
// the eventual server-backed store, over the in-memory implementation. `memory`
// is exposed so a test can drive an external change (another device) directly.
function createAsyncStore(seed: ReturnType<typeof dashboard> | null = null): {
	store: DashboardDraftStore;
	failSaves: () => void;
	memory: DashboardDraftStore;
} {
	const memory = createMemoryDashboardDraftStore(seed);
	let saveShouldFail = false;
	return {
		failSaves: () => {
			saveShouldFail = true;
		},
		memory,
		store: {
			load: () => Promise.resolve(memory.load()),
			save: (next) =>
				saveShouldFail
					? Promise.reject(new Error("transport down"))
					: Promise.resolve(memory.save(next)),
			clear: () => Promise.resolve(memory.clear()),
			subscribe: memory.subscribe,
		},
	};
}

describe("useDashboardDraft with an injected store", () => {
	beforeEach(() => window.localStorage.clear());
	afterEach(() => window.localStorage.clear());

	it("persists to the injected store, not localStorage", () => {
		const store = createMemoryDashboardDraftStore();
		const { result } = renderHook(() => useDashboardDraft(KEY, initial, store));
		act(() => result.current.setSpec(edited));
		expect(result.current.spec).toEqual(edited);
		expect(store.load()).toEqual(edited);
		expect(window.localStorage.getItem(KEY)).toBeNull();
	});

	it("reset clears the injected store and returns to the initial spec", () => {
		const store = createMemoryDashboardDraftStore();
		const { result } = renderHook(() => useDashboardDraft(KEY, initial, store));
		act(() => result.current.setSpec(edited));
		act(() => result.current.reset());
		expect(result.current.spec).toEqual(initial);
		expect(store.load()).toBeNull();
	});

	it("reflects a spec pushed by the store from another consumer", () => {
		const store = createMemoryDashboardDraftStore();
		const { result } = renderHook(() => useDashboardDraft(KEY, initial, store));
		act(() => store.save(edited));
		expect(result.current.spec).toEqual(edited);
	});

	it("restores an asynchronously loaded draft after mount", async () => {
		const { store } = createAsyncStore(edited);
		const { result } = renderHook(() => useDashboardDraft(KEY, initial, store));
		expect(result.current.spec).toEqual(initial);
		await waitFor(() => expect(result.current.spec).toEqual(edited));
		expect(result.current.error).toBeNull();
	});

	it("keeps a valid edit when an async persist rejects", async () => {
		const { store, failSaves } = createAsyncStore();
		const { result } = renderHook(() => useDashboardDraft(KEY, initial, store));
		failSaves();
		act(() => result.current.setSpec(edited));
		// The edit is applied immediately, before the persist settles.
		expect(result.current.spec).toEqual(edited);
		await waitFor(() => expect(result.current.error).toBeInstanceOf(Error));
		expect(result.current.error).not.toBeInstanceOf(DashboardSpecError);
		expect(result.current.spec).toEqual(edited);
	});

	it("does not let an in-flight async load overwrite a local edit", async () => {
		// The store has a stored draft that load() will resolve on a later tick.
		const { store } = createAsyncStore(stored);
		const { result } = renderHook(() => useDashboardDraft(KEY, initial, store));
		// Edit before the load resolves.
		act(() => result.current.setSpec(edited));
		expect(result.current.spec).toEqual(edited);
		// Let the in-flight load settle; the edit must survive, not revert to the
		// stored value.
		await act(async () => {
			await Promise.resolve();
			await Promise.resolve();
		});
		expect(result.current.spec).toEqual(edited);
	});

	it("does not let an in-flight async load overwrite an external change", async () => {
		const { store, memory } = createAsyncStore(stored);
		const { result } = renderHook(() => useDashboardDraft(KEY, initial, store));
		// Another device pushes an edit before the initial load resolves.
		act(() => memory.save(edited));
		expect(result.current.spec).toEqual(edited);
		await act(async () => {
			await Promise.resolve();
			await Promise.resolve();
		});
		expect(result.current.spec).toEqual(edited);
	});
});
