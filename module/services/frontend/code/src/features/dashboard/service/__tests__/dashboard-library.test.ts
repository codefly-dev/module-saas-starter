import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DashboardNameError, type DashboardRecord } from "../../model/record";
import { dashboard, metric } from "../../model/schema";
import { DashboardSpecError } from "../../model/validate";
import {
	createBrowserDashboardLibrary,
	createMemoryDashboardLibrary,
	type DashboardLibrary,
	type DashboardLibraryChange,
	dashboardRecordStore,
} from "../dashboard-library";
import type { DashboardDraftChange } from "../draft-store";

const KEY = "dashboard:library-test";

// A store method may be sync (memory/browser) or async (a server store). Defer
// the call into a promise so a synchronous throw asserts through `.rejects` the
// same way a rejected promise would.
const attempt = <T>(fn: () => T | Promise<T>): Promise<T> =>
	Promise.resolve().then(fn);

const specA = dashboard({
	title: "A",
	metrics: [metric({ title: "Top", groupBy: "event_type", chart: "bar" })],
});
const specB = dashboard({
	title: "B",
	metrics: [metric({ title: "By cat", groupBy: "category", chart: "bar" })],
});

// A monotonic clock and counter so records are deterministic and ordered.
function fixtures() {
	let tick = 0;
	return {
		now: () => `2026-08-31T00:00:${String(tick++).padStart(2, "0")}.000Z`,
		newId: (() => {
			let n = 0;
			return () => `id-${++n}`;
		})(),
	};
}

// Both backings implement one contract; run the shared behavior against each.
const backings: Array<[string, () => DashboardLibrary]> = [
	["memory", () => createMemoryDashboardLibrary([], fixtures())],
	["browser", () => createBrowserDashboardLibrary(KEY, { ...fixtures() })],
];

describe.each(backings)("DashboardLibrary (%s)", (_name, build) => {
	beforeEach(() => window.localStorage.clear());
	afterEach(() => {
		vi.restoreAllMocks();
		window.localStorage.clear();
	});

	it("creates a private, timestamped record and lists it", async () => {
		const library = build();
		const record = await library.create({ name: "  Weekly  ", spec: specA });
		expect(record.name).toBe("Weekly");
		expect(record.visibility).toBe("private");
		expect(record.createdAt).toBe(record.updatedAt);
		expect(await library.list()).toEqual([record]);
		expect(await library.get(record.id)).toEqual(record);
	});

	it("rejects an empty name and an invalid spec on create", async () => {
		const library = build();
		await expect(
			attempt(() => library.create({ name: "   ", spec: specA })),
		).rejects.toThrow(DashboardNameError);
		await expect(
			attempt(() =>
				library.create({
					name: "Bad",
					spec: { version: 1, metrics: "nope" } as never,
				}),
			),
		).rejects.toThrow(DashboardSpecError);
	});

	it("updates name, spec, and visibility and advances updatedAt", async () => {
		const library = build();
		const created = await library.create({ name: "One", spec: specA });
		const shared = await library.update(created.id, {
			name: "Renamed",
			spec: specB,
			visibility: "org",
		});
		expect(shared.name).toBe("Renamed");
		expect(shared.spec).toEqual(specB);
		expect(shared.visibility).toBe("org");
		expect(shared.createdAt).toBe(created.createdAt);
		expect(shared.updatedAt > created.updatedAt).toBe(true);
	});

	it("rejects an update to an unknown id", async () => {
		const library = build();
		await expect(
			attempt(() => library.update("missing", { name: "x" })),
		).rejects.toThrow();
	});

	it("removes a record and is a no-op for an unknown id", async () => {
		const library = build();
		const record = await library.create({ name: "One", spec: specA });
		await library.remove("missing");
		expect(await library.list()).toHaveLength(1);
		await library.remove(record.id);
		expect(await library.list()).toEqual([]);
	});

	it("notifies subscribers of the full collection on each mutation", async () => {
		const library = build();
		const changes: DashboardLibraryChange[] = [];
		const unsubscribe = library.subscribe((change) => changes.push(change));
		const record = await library.create({ name: "One", spec: specA });
		await library.remove(record.id);
		unsubscribe();
		await library.create({ name: "Two", spec: specB });
		expect(changes).toEqual([
			{ kind: "records", records: [record] },
			{ kind: "records", records: [] },
		]);
	});
});

describe("createBrowserDashboardLibrary storage", () => {
	beforeEach(() => window.localStorage.clear());
	afterEach(() => window.localStorage.clear());

	it("does not touch window at construction", () => {
		const getStorage = vi.fn(() => window.localStorage);
		createBrowserDashboardLibrary(KEY, { getStorage });
		expect(getStorage).not.toHaveBeenCalled();
	});

	it("persists a versioned container and reads it back", async () => {
		const library = createBrowserDashboardLibrary(KEY, fixtures());
		const record = await library.create({ name: "One", spec: specA });
		const stored = JSON.parse(window.localStorage.getItem(KEY)!);
		expect(stored.version).toBe(1);
		expect(stored.records).toEqual([record]);
	});

	it("throws when the stored container is malformed", async () => {
		window.localStorage.setItem(KEY, "{ not json");
		const library = createBrowserDashboardLibrary(KEY);
		await expect(attempt(() => library.list())).rejects.toThrow();
	});

	it("emits the parsed collection on a cross-tab write", () => {
		const library = createBrowserDashboardLibrary(KEY, fixtures());
		const changes: DashboardLibraryChange[] = [];
		library.subscribe((change) => changes.push(change));
		const record: DashboardRecord = {
			id: "id-x",
			name: "Elsewhere",
			spec: specA,
			visibility: "org",
			createdAt: "2026-08-31T00:00:00.000Z",
			updatedAt: "2026-08-31T00:00:00.000Z",
		};
		window.dispatchEvent(
			new StorageEvent("storage", {
				key: KEY,
				newValue: JSON.stringify({ version: 1, records: [record] }),
			}),
		);
		expect(changes).toEqual([{ kind: "records", records: [record] }]);
	});
});

describe("dashboardRecordStore", () => {
	it("bridges one record to the draft-store contract", async () => {
		const library = createMemoryDashboardLibrary([], fixtures());
		const record = await library.create({ name: "One", spec: specA });
		const store = dashboardRecordStore(library, record.id);

		expect(await store.load()).toEqual(specA);
		await store.save(specB);
		expect((await library.get(record.id))?.spec).toEqual(specB);
	});

	it("maps a record's spec update and delete to draft changes", async () => {
		const library = createMemoryDashboardLibrary([], fixtures());
		const record = await library.create({ name: "One", spec: specA });
		const store = dashboardRecordStore(library, record.id);
		const changes: DashboardDraftChange[] = [];
		store.subscribe((change) => changes.push(change));

		await library.update(record.id, { spec: specB });
		await library.remove(record.id);

		expect(changes).toEqual([
			{ kind: "spec", spec: specB },
			{ kind: "cleared" },
		]);
	});
});
