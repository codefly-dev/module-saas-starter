"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { DashboardDef } from "../model/schema";
import { assertDashboardSpec, DashboardSpecError } from "../model/validate";
import {
	createBrowserDashboardDraftStore,
	type DashboardDraftStore,
} from "./draft-store";

export interface DashboardDraft {
	// The active spec. Valid whenever `error` is null; if an invalid `initial`
	// was supplied it is held here as a best effort with `error` set, so a bad
	// default surfaces rather than crashing the render.
	spec: DashboardDef;
	// Replaces the active spec and persists it. A malformed or incoherent spec
	// is rejected — the active spec is left untouched and `error` explains why.
	// A valid spec is always applied in memory even if persisting it fails, so a
	// full or blocked store never costs the user their edit.
	setSpec: (next: DashboardDef) => void;
	// Discards the persisted draft and returns to the current initial spec.
	reset: () => void;
	// The last failure, or null after a success. A DashboardSpecError when a
	// spec was rejected; a plain Error when the store itself could not be read,
	// written, or cleared.
	error: Error | null;
}

interface DraftState {
	spec: DashboardDef;
	error: Error | null;
}

function asSpecError(cause: unknown): DashboardSpecError {
	return cause instanceof DashboardSpecError
		? cause
		: new DashboardSpecError("spec could not be validated", { cause });
}

function storageError(operation: string, cause: unknown): Error {
	return new Error(`Dashboard draft ${operation} failed`, { cause });
}

function isPromiseLike(value: unknown): value is Promise<unknown> {
	return (
		typeof (value as { then?: unknown } | null | undefined)?.then === "function"
	);
}

/**
 * Holds a dashboard spec in React state backed by a {@link DashboardDraftStore},
 * so a runtime edit — swap a metric, add or remove a widget — is just a new spec
 * object and survives a reload. Every spec that enters is validated here: an
 * invalid persisted draft, an invalid `setSpec`, or an invalid `initial` is
 * rejected without the bad spec becoming the trusted active spec, and the reason
 * surfaces through `error` rather than a thrown render. Persistence is likewise
 * guarded: a valid edit is applied in memory even when the store rejects the
 * write, and a store failure surfaces through `error` instead of escaping.
 *
 * The store is the only persistence seam. It defaults to localStorage; an
 * authenticated caller injects a server-backed store instead (org- or
 * user-scoped, per the ownership model), and the hook API is unchanged. Changes
 * from another tab — or another device once server-backed — arrive through the
 * store's subscription.
 *
 * An injected `store` must be a stable reference across renders (memoize it):
 * the hook re-subscribes and reloads whenever the store identity changes, so a
 * fresh instance every render would re-fetch on every render.
 */
export function useDashboardDraft(
	storageKey: string,
	initial: DashboardDef,
	store?: DashboardDraftStore,
): DashboardDraft {
	// Track the latest `initial` so `reset` and a cross-tab clear return to the
	// caller's current default rather than a value frozen at first mount.
	const initialRef = useRef(initial);
	initialRef.current = initial;

	// A store's load() may resolve on a later tick. If an edit, a reset, or an
	// external change lands before it does, that newer state wins — the resolved
	// initial load must not clobber it. Reset per store (a new store gets a fresh
	// load); flipped by every other state update.
	const supersededRef = useRef(false);

	// Default to localStorage; only build it when no store is injected.
	const activeStore = useMemo(
		() => store ?? createBrowserDashboardDraftStore(storageKey),
		[store, storageKey],
	);

	const [state, setState] = useState<DraftState>(() => {
		try {
			assertDashboardSpec(initial);
			return { spec: initial, error: null };
		} catch (cause) {
			return { spec: initial, error: asSpecError(cause) };
		}
	});

	// Restore the persisted draft after the initial render, so server and first
	// client render agree on `initial` and hydration stays stable; then track
	// changes from other tabs (or devices). A missing draft leaves the initial
	// state — including a surfaced invalid-initial error — untouched; a corrupt
	// draft or a store error is surfaced, never rendered or thrown.
	useEffect(() => {
		let cancelled = false;
		supersededRef.current = false;
		const fail = (operation: string, cause: unknown) =>
			setState((prev) => ({
				spec: prev.spec,
				error:
					cause instanceof DashboardSpecError
						? cause
						: storageError(operation, cause),
			}));
		const applyLoaded = (spec: DashboardDef | null) => {
			if (spec === null) return;
			setState({ spec, error: null });
		};

		try {
			const loaded = activeStore.load();
			if (isPromiseLike(loaded)) {
				loaded.then(
					(spec) => {
						if (!cancelled && !supersededRef.current) {
							applyLoaded(spec as DashboardDef | null);
						}
					},
					(cause) => {
						if (!cancelled && !supersededRef.current) fail("load", cause);
					},
				);
			} else {
				applyLoaded(loaded);
			}
		} catch (cause) {
			fail("load", cause);
		}

		const unsubscribe = activeStore.subscribe((change) => {
			if (cancelled) return;
			// An external change is newer than a still-pending initial load.
			supersededRef.current = true;
			switch (change.kind) {
				case "spec":
					setState({ spec: change.spec, error: null });
					return;
				case "cleared":
					setState({ spec: initialRef.current, error: null });
					return;
				case "error":
					setState((prev) => ({ spec: prev.spec, error: change.error }));
					return;
			}
		});

		return () => {
			cancelled = true;
			unsubscribe();
		};
	}, [activeStore]);

	const setSpec = useCallback(
		(next: DashboardDef) => {
			try {
				assertDashboardSpec(next);
			} catch (cause) {
				setState((prev) => ({ spec: prev.spec, error: asSpecError(cause) }));
				return;
			}
			// Apply the valid edit unconditionally: a runtime mutation must take
			// effect even if it cannot be persisted, so a full or blocked store
			// costs the save, never the edit.
			supersededRef.current = true;
			setState({ spec: next, error: null });
			const onFail = (cause: unknown) =>
				setState((prev) => ({
					spec: prev.spec,
					error: storageError("save", cause),
				}));
			try {
				const result = activeStore.save(next);
				if (isPromiseLike(result)) result.then(undefined, onFail);
			} catch (cause) {
				onFail(cause);
			}
		},
		[activeStore],
	);

	const reset = useCallback(() => {
		supersededRef.current = true;
		setState({ spec: initialRef.current, error: null });
		const onFail = (cause: unknown) =>
			setState((prev) => ({
				spec: prev.spec,
				error: storageError("reset", cause),
			}));
		try {
			const result = activeStore.clear();
			if (isPromiseLike(result)) result.then(undefined, onFail);
		} catch (cause) {
			onFail(cause);
		}
	}, [activeStore]);

	return { spec: state.spec, setSpec, reset, error: state.error };
}
