import type { DashboardDef } from "../model/schema";
import { DashboardSpecError, parseDashboardSpec } from "../model/validate";

type Awaitable<T> = T | Promise<T>;

function asSpecError(cause: unknown): DashboardSpecError {
	return cause instanceof DashboardSpecError
		? cause
		: new DashboardSpecError("spec could not be validated", { cause });
}

// A change to the persisted draft observed from outside this store instance —
// another browser tab today, another device once the store is server-backed.
export type DashboardDraftChange =
	// A valid draft is now present.
	| { readonly kind: "spec"; readonly spec: DashboardDef }
	// The draft was removed; consumers fall back to their current initial spec.
	| { readonly kind: "cleared" }
	// A draft is present but could not be read (corrupt, schema-drifted, or a
	// transport failure). Surfaced rather than rendered.
	| { readonly kind: "error"; readonly error: Error };

/**
 * The persistence seam for a dashboard draft. `useDashboardDraft` owns spec
 * validation and error surfacing; a store only reads, writes, removes, and
 * announces external changes, so every implementation shares one notion of a
 * valid draft. localStorage is one implementation; the eventual server-backed
 * store (org- or user-scoped, decided with the ownership model) is another and
 * drops in behind the same interface.
 *
 * Reads and writes may be asynchronous; a synchronous implementation simply
 * returns a value instead of a promise.
 */
export interface DashboardDraftStore {
	// The currently persisted spec, or null when none is stored. Rejects/throws
	// with a DashboardSpecError when the stored spec cannot be parsed or fails
	// validation, or a plain Error when it cannot be read.
	load(): Awaitable<DashboardDef | null>;
	// Persist a spec. Rejects/throws with a plain Error on failure.
	save(spec: DashboardDef): Awaitable<void>;
	// Remove the persisted spec. Rejects/throws with a plain Error on failure.
	clear(): Awaitable<void>;
	// Observe changes made outside this instance. Returns an unsubscribe.
	subscribe(listener: (change: DashboardDraftChange) => void): () => void;
}

/**
 * localStorage-backed store: the placeholder home for a draft until the settings
 * service (or a dedicated dashboards store) owns it. `window` is touched lazily,
 * never at construction, so the store is safe to build during a server render.
 * Cross-tab edits arrive through the `storage` event, resolved from the event's
 * own value so a synthetic event (and a tab that never re-reads storage) behaves
 * the same as a real one.
 */
export function createBrowserDashboardDraftStore(
	storageKey: string,
	getStorage: () => Storage = () => window.localStorage,
): DashboardDraftStore {
	return {
		load() {
			const raw = getStorage().getItem(storageKey);
			return raw === null ? null : parseDashboardSpec(raw);
		},
		save(spec) {
			getStorage().setItem(storageKey, JSON.stringify(spec));
		},
		clear() {
			getStorage().removeItem(storageKey);
		},
		subscribe(listener) {
			const onStorage = (event: StorageEvent) => {
				if (event.key !== storageKey) return;
				if (event.newValue === null) {
					listener({ kind: "cleared" });
					return;
				}
				try {
					listener({ kind: "spec", spec: parseDashboardSpec(event.newValue) });
				} catch (cause) {
					listener({ kind: "error", error: asSpecError(cause) });
				}
			};
			window.addEventListener("storage", onStorage);
			return () => window.removeEventListener("storage", onStorage);
		},
	};
}

/**
 * Process-memory store: no persistence across reloads, but the same interface —
 * useful for tests and for a preview surface that should not touch a user's
 * stored draft. A save/clear notifies every other subscriber of the instance,
 * so two consumers sharing one store stay in sync the way tabs (or devices) do.
 */
export function createMemoryDashboardDraftStore(
	seed: DashboardDef | null = null,
): DashboardDraftStore {
	let stored = seed;
	const listeners = new Set<(change: DashboardDraftChange) => void>();
	const emit = (change: DashboardDraftChange) => {
		for (const listener of listeners) listener(change);
	};
	return {
		load() {
			return stored;
		},
		save(spec) {
			stored = spec;
			emit({ kind: "spec", spec });
		},
		clear() {
			stored = null;
			emit({ kind: "cleared" });
		},
		subscribe(listener) {
			listeners.add(listener);
			return () => {
				listeners.delete(listener);
			};
		},
	};
}
