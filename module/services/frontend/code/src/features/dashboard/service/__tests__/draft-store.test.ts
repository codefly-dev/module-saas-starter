import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { dashboard, event, metric } from "../../model/schema";
import { DashboardSpecError } from "../../model/validate";
import {
	createBrowserDashboardDraftStore,
	createMemoryDashboardDraftStore,
	type DashboardDraftChange,
} from "../draft-store";

const KEY = "dashboard:store-test";

const spec = dashboard({
	title: "Spec",
	metrics: [
		metric({ title: "Top events", groupBy: "event_type", chart: "bar" }),
	],
});

const other = dashboard({
	title: "Other",
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

describe("createBrowserDashboardDraftStore", () => {
	beforeEach(() => window.localStorage.clear());
	afterEach(() => {
		vi.restoreAllMocks();
		window.localStorage.clear();
	});

	it("loads null when nothing is stored", () => {
		const store = createBrowserDashboardDraftStore(KEY);
		expect(store.load()).toBeNull();
	});

	it("round-trips a saved spec through storage", () => {
		const store = createBrowserDashboardDraftStore(KEY);
		store.save(spec);
		expect(JSON.parse(window.localStorage.getItem(KEY)!)).toEqual(spec);
		expect(store.load()).toEqual(spec);
	});

	it("clears the stored spec", () => {
		const store = createBrowserDashboardDraftStore(KEY);
		store.save(spec);
		store.clear();
		expect(window.localStorage.getItem(KEY)).toBeNull();
		expect(store.load()).toBeNull();
	});

	it("throws a DashboardSpecError when the stored spec is corrupt", () => {
		window.localStorage.setItem(KEY, "{ not json");
		const store = createBrowserDashboardDraftStore(KEY);
		expect(() => store.load()).toThrow(DashboardSpecError);
	});

	it("does not touch window at construction", () => {
		const getStorage = vi.fn(() => window.localStorage);
		createBrowserDashboardDraftStore(KEY, getStorage);
		expect(getStorage).not.toHaveBeenCalled();
	});

	it("resolves a cross-tab spec change from the event value", () => {
		const store = createBrowserDashboardDraftStore(KEY);
		const changes: DashboardDraftChange[] = [];
		const unsubscribe = store.subscribe((change) => changes.push(change));
		window.dispatchEvent(
			new StorageEvent("storage", {
				key: KEY,
				newValue: JSON.stringify(other),
			}),
		);
		expect(changes).toEqual([{ kind: "spec", spec: other }]);
		unsubscribe();
	});

	it("resolves a cross-tab clear from a null event value", () => {
		const store = createBrowserDashboardDraftStore(KEY);
		const changes: DashboardDraftChange[] = [];
		store.subscribe((change) => changes.push(change));
		window.dispatchEvent(
			new StorageEvent("storage", { key: KEY, newValue: null }),
		);
		expect(changes).toEqual([{ kind: "cleared" }]);
	});

	it("surfaces a corrupt cross-tab value as an error change", () => {
		const store = createBrowserDashboardDraftStore(KEY);
		const changes: DashboardDraftChange[] = [];
		store.subscribe((change) => changes.push(change));
		window.dispatchEvent(
			new StorageEvent("storage", { key: KEY, newValue: "{ not json" }),
		);
		expect(changes).toHaveLength(1);
		expect(changes[0].kind).toBe("error");
		expect((changes[0] as { error: Error }).error).toBeInstanceOf(
			DashboardSpecError,
		);
	});

	it("ignores events for a different key and after unsubscribe", () => {
		const store = createBrowserDashboardDraftStore(KEY);
		const changes: DashboardDraftChange[] = [];
		const unsubscribe = store.subscribe((change) => changes.push(change));
		window.dispatchEvent(
			new StorageEvent("storage", {
				key: "other:key",
				newValue: JSON.stringify(other),
			}),
		);
		unsubscribe();
		window.dispatchEvent(
			new StorageEvent("storage", {
				key: KEY,
				newValue: JSON.stringify(other),
			}),
		);
		expect(changes).toEqual([]);
	});
});

describe("createMemoryDashboardDraftStore", () => {
	it("loads null by default and the seed when provided", () => {
		expect(createMemoryDashboardDraftStore().load()).toBeNull();
		expect(createMemoryDashboardDraftStore(spec).load()).toEqual(spec);
	});

	it("round-trips a saved spec and clears it", () => {
		const store = createMemoryDashboardDraftStore();
		store.save(spec);
		expect(store.load()).toEqual(spec);
		store.clear();
		expect(store.load()).toBeNull();
	});

	it("notifies subscribers of saves and clears, and stops after unsubscribe", () => {
		const store = createMemoryDashboardDraftStore();
		const changes: DashboardDraftChange[] = [];
		const unsubscribe = store.subscribe((change) => changes.push(change));
		store.save(spec);
		store.clear();
		unsubscribe();
		store.save(other);
		expect(changes).toEqual([{ kind: "spec", spec }, { kind: "cleared" }]);
	});
});
