import {
	assertDashboardName,
	type DashboardRecord,
	type DashboardRecordPatch,
	type DashboardVisibility,
	DEFAULT_DASHBOARD_VISIBILITY,
	isDashboardVisibility,
} from "../model/record";
import { DASHBOARD_SPEC_VERSION, type DashboardDef } from "../model/schema";
import { assertDashboardSpec } from "../model/validate";
import type { DashboardDraftChange, DashboardDraftStore } from "./draft-store";

type Awaitable<T> = T | Promise<T>;

export const DASHBOARD_LIBRARY_VERSION = 1;

// A change to the persisted collection observed from outside a given store
// instance — another browser tab today, another device once server-backed.
export type DashboardLibraryChange =
	// The full current collection. Emitted after every local mutation and on
	// every external write, so a subscriber never has to diff.
	| { readonly kind: "records"; readonly records: DashboardRecord[] }
	// The collection is present but could not be read (corrupt or schema-drifted
	// storage, or a transport failure). Surfaced rather than rendered.
	| { readonly kind: "error"; readonly error: Error };

export interface CreateDashboardInput {
	readonly name: string;
	readonly spec: DashboardDef;
	readonly visibility?: DashboardVisibility;
}

/**
 * The persistence seam for a user's dashboards — the CRUD counterpart to
 * {@link DashboardDraftStore}, which persists a single anonymous draft. A store
 * owns identity and timestamps; callers own validation intent. localStorage is
 * one implementation; the eventual server-backed store (org-scoped, RLS, with
 * ownership and cross-user org-shared reads) is another and drops in behind the
 * same interface. `share` is `update` with a visibility; `duplicate` is `create`
 * with a copied spec — both compose on this surface rather than widening it.
 *
 * Reads and writes may be asynchronous; a synchronous implementation returns a
 * value instead of a promise.
 */
export interface DashboardLibrary {
	// Every dashboard in the collection. Rejects/throws when storage cannot be
	// read or parsed.
	list(): Awaitable<DashboardRecord[]>;
	// One dashboard by id, or null when it is not in the collection.
	get(id: string): Awaitable<DashboardRecord | null>;
	// Create a named dashboard. Rejects/throws with a DashboardNameError on an
	// empty name or a DashboardSpecError on an invalid spec.
	create(input: CreateDashboardInput): Awaitable<DashboardRecord>;
	// Return the record with this explicit id, creating it from the input when
	// absent. Never overwrites an existing record — a caller that owns a
	// well-known id (e.g. the external-driver channel) uses this to bind to a
	// stable record without persisting which id it is.
	ensure(
		input: { id: string } & CreateDashboardInput,
	): Awaitable<DashboardRecord>;
	// Apply a partial update and return the new record. Rejects/throws when no
	// dashboard has the id, or with a name/spec error for an invalid patch.
	update(id: string, patch: DashboardRecordPatch): Awaitable<DashboardRecord>;
	// Remove a dashboard. A missing id is a no-op.
	remove(id: string): Awaitable<void>;
	// Observe changes made outside this call site. Returns an unsubscribe.
	subscribe(listener: (change: DashboardLibraryChange) => void): () => void;
}

interface Clock {
	now(): string;
	newId(): string;
}

const systemClock: Clock = {
	now: () => new Date().toISOString(),
	newId: () => crypto.randomUUID(),
};

function asError(cause: unknown): Error {
	return cause instanceof Error
		? cause
		: new Error("Dashboard library operation failed", { cause });
}

function assertRecord(value: unknown): DashboardRecord {
	if (typeof value !== "object" || value === null) {
		throw new Error("Dashboard record is not an object");
	}
	const record = value as Record<string, unknown>;
	if (typeof record.id !== "string" || typeof record.name !== "string") {
		throw new Error("Dashboard record is missing id or name");
	}
	if (
		typeof record.createdAt !== "string" ||
		typeof record.updatedAt !== "string"
	) {
		throw new Error("Dashboard record is missing timestamps");
	}
	if (!isDashboardVisibility(record.visibility)) {
		throw new Error("Dashboard record has an unknown visibility");
	}
	// The spec is the one part shared with a static literal; hold it to the same
	// validator every other write boundary uses.
	assertDashboardSpec(record.spec);
	return {
		id: record.id,
		name: record.name,
		spec: record.spec,
		visibility: record.visibility,
		createdAt: record.createdAt,
		updatedAt: record.updatedAt,
	};
}

function parseLibrary(raw: string): DashboardRecord[] {
	let parsed: unknown;
	try {
		parsed = JSON.parse(raw);
	} catch (cause) {
		throw new Error("Dashboard library could not be parsed", { cause });
	}
	const container = parsed as { records?: unknown };
	if (!Array.isArray(container.records)) {
		throw new Error("Dashboard library is malformed");
	}
	return container.records.map(assertRecord);
}

function serializeLibrary(records: DashboardRecord[]): string {
	return JSON.stringify({ version: DASHBOARD_LIBRARY_VERSION, records });
}

interface Backing {
	read(): DashboardRecord[];
	persist(records: DashboardRecord[]): void;
	emit(records: DashboardRecord[]): void;
}

// The CRUD core shared by every synchronous backing. It owns identity,
// timestamps, and write-time validation so no implementation re-derives them; a
// backing only supplies where the records live and how a mutation is announced.
function crud(
	backing: Backing,
	clock: Clock,
): Pick<
	DashboardLibrary,
	"list" | "get" | "create" | "ensure" | "update" | "remove"
> {
	const commit = (records: DashboardRecord[]) => {
		backing.persist(records);
		backing.emit(records);
	};
	const insert = (
		id: string,
		{
			name,
			spec,
			visibility = DEFAULT_DASHBOARD_VISIBILITY,
		}: CreateDashboardInput,
	): DashboardRecord => {
		assertDashboardName(name);
		assertDashboardSpec(spec);
		const at = clock.now();
		const record: DashboardRecord = {
			id,
			name: name.trim(),
			spec,
			visibility,
			createdAt: at,
			updatedAt: at,
		};
		commit([...backing.read(), record]);
		return record;
	};
	return {
		list: () => backing.read(),
		get: (id) => backing.read().find((record) => record.id === id) ?? null,
		create: (input) => insert(clock.newId(), input),
		ensure: ({ id, ...input }) => {
			const existing = backing.read().find((record) => record.id === id);
			return existing ?? insert(id, input);
		},
		update: (id, patch) => {
			const records = backing.read();
			const index = records.findIndex((record) => record.id === id);
			if (index === -1) throw new Error(`No dashboard with id "${id}".`);
			if (patch.name !== undefined) assertDashboardName(patch.name);
			if (patch.spec !== undefined) assertDashboardSpec(patch.spec);
			const next: DashboardRecord = {
				...records[index],
				...(patch.name !== undefined ? { name: patch.name.trim() } : {}),
				...(patch.spec !== undefined ? { spec: patch.spec } : {}),
				...(patch.visibility !== undefined
					? { visibility: patch.visibility }
					: {}),
				updatedAt: clock.now(),
			};
			const nextRecords = [...records];
			nextRecords[index] = next;
			commit(nextRecords);
			return next;
		},
		remove: (id) => {
			const records = backing.read();
			const nextRecords = records.filter((record) => record.id !== id);
			if (nextRecords.length !== records.length) commit(nextRecords);
		},
	};
}

/**
 * localStorage-backed collection: the placeholder home for a user's dashboards
 * until a server store owns them. `window` is touched lazily, never at
 * construction, so the store is safe to build during a server render. A local
 * mutation notifies this instance's subscribers directly (localStorage fires no
 * event in the writing tab); a write from another tab arrives through the
 * `storage` event, resolved from the event's own value.
 */
export function createBrowserDashboardLibrary(
	storageKey: string,
	deps: {
		getStorage?: () => Storage;
		now?: () => string;
		newId?: () => string;
	} = {},
): DashboardLibrary {
	const getStorage = deps.getStorage ?? (() => window.localStorage);
	const clock: Clock = {
		now: deps.now ?? systemClock.now,
		newId: deps.newId ?? systemClock.newId,
	};
	const listeners = new Set<(change: DashboardLibraryChange) => void>();
	const backing: Backing = {
		read() {
			const raw = getStorage().getItem(storageKey);
			return raw === null ? [] : parseLibrary(raw);
		},
		persist(records) {
			getStorage().setItem(storageKey, serializeLibrary(records));
		},
		emit(records) {
			for (const listener of listeners) listener({ kind: "records", records });
		},
	};
	return {
		...crud(backing, clock),
		subscribe(listener) {
			listeners.add(listener);
			const onStorage = (event: StorageEvent) => {
				if (event.key !== storageKey) return;
				try {
					listener({
						kind: "records",
						records:
							event.newValue === null ? [] : parseLibrary(event.newValue),
					});
				} catch (cause) {
					listener({ kind: "error", error: asError(cause) });
				}
			};
			window.addEventListener("storage", onStorage);
			return () => {
				listeners.delete(listener);
				window.removeEventListener("storage", onStorage);
			};
		},
	};
}

/**
 * Process-memory collection: no persistence across reloads, but the same
 * interface — useful for tests and for any surface that should not touch a
 * user's stored dashboards. Injectable `now`/`newId` make records deterministic
 * under test.
 */
export function createMemoryDashboardLibrary(
	seed: DashboardRecord[] = [],
	deps: { now?: () => string; newId?: () => string } = {},
): DashboardLibrary {
	let stored = [...seed];
	const clock: Clock = {
		now: deps.now ?? systemClock.now,
		newId: deps.newId ?? systemClock.newId,
	};
	const listeners = new Set<(change: DashboardLibraryChange) => void>();
	const backing: Backing = {
		read: () => stored,
		persist(records) {
			stored = records;
		},
		emit(records) {
			for (const listener of listeners) listener({ kind: "records", records });
		},
	};
	return {
		...crud(backing, clock),
		subscribe(listener) {
			listeners.add(listener);
			return () => {
				listeners.delete(listener);
			};
		},
	};
}

// The blank canvas a record-backed draft resets to: a spec with no widgets.
const BLANK_SPEC: DashboardDef = {
	version: DASHBOARD_SPEC_VERSION,
	metrics: [],
};

function recordChangeMapper(id: string) {
	return (change: DashboardLibraryChange): DashboardDraftChange => {
		if (change.kind === "error") {
			return { kind: "error", error: change.error };
		}
		const record = change.records.find((entry) => entry.id === id);
		return record ? { kind: "spec", spec: record.spec } : { kind: "cleared" };
	};
}

/**
 * Adapt one library record to the {@link DashboardDraftStore} the editor and
 * authoring loop already bind, so opening a dashboard reuses the whole editing
 * surface unchanged: a save is a spec update on the record, and an external
 * change to that record re-renders the canvas. `clear` resets the record to a
 * blank canvas — a draft reset must not destroy the named record it belongs to,
 * so it empties the spec rather than deleting the dashboard.
 */
export function dashboardRecordStore(
	library: DashboardLibrary,
	id: string,
): DashboardDraftStore {
	const toSpecChange = recordChangeMapper(id);
	return {
		load: () =>
			Promise.resolve(library.get(id)).then((record) => record?.spec ?? null),
		save: (spec) =>
			Promise.resolve(library.update(id, { spec })).then(() => undefined),
		clear: () =>
			Promise.resolve(library.update(id, { spec: BLANK_SPEC })).then(
				() => undefined,
			),
		subscribe: (listener) =>
			library.subscribe((change) => listener(toSpecChange(change))),
	};
}

/**
 * A {@link DashboardDraftStore} for a reserved, well-known record id — the seam
 * the external-driver channel binds so a composing module's `setDashboard`
 * lands in a dashboard the "My Dashboards" surface lists and opens, instead of
 * an anonymous draft no surface renders. Unlike {@link dashboardRecordStore},
 * `save` creates the record on first write (the viewer may never have opened it)
 * and updates only its spec thereafter, so a rename or share the viewer makes
 * survives the next driver edit.
 */
export function driverDashboardStore(
	library: DashboardLibrary,
	target: { id: string; name: string },
): DashboardDraftStore {
	const { id, name } = target;
	const toSpecChange = recordChangeMapper(id);
	return {
		load: () =>
			Promise.resolve(library.get(id)).then((record) => record?.spec ?? null),
		save: (spec) =>
			Promise.resolve(library.get(id))
				.then((existing) =>
					existing
						? library.update(id, { spec })
						: library.ensure({ id, name, spec }),
				)
				.then(() => undefined),
		clear: () =>
			Promise.resolve(library.update(id, { spec: BLANK_SPEC })).then(
				() => undefined,
			),
		subscribe: (listener) =>
			library.subscribe((change) => listener(toSpecChange(change))),
	};
}
