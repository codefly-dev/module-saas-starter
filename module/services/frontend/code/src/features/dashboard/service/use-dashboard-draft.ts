"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import type { DashboardDef } from "../model/schema";
import {
	assertDashboardSpec,
	DashboardSpecError,
	parseDashboardSpec,
} from "../model/validate";

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
	// spec was rejected; a plain Error when local storage itself could not be
	// read, written, or cleared.
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

/**
 * Holds a dashboard spec in React state backed by localStorage, so a runtime
 * edit — swap a metric, add or remove a widget — is just a new spec object and
 * survives a reload. Every spec that enters is validated: an invalid persisted
 * draft, an invalid `setSpec`, or an invalid `initial` is rejected without the
 * bad spec becoming the trusted active spec, and the reason surfaces through
 * `error` rather than a thrown render. localStorage access is likewise guarded:
 * a valid edit is applied in memory even when the store rejects the write, and
 * a storage failure surfaces through `error` instead of escaping. Edits made in
 * another tab are picked up through the `storage` event.
 *
 * This is the placeholder home for the draft; the eventual owner is the
 * settings service (`@codefly/saas-settings`).
 */
export function useDashboardDraft(
	storageKey: string,
	initial: DashboardDef,
): DashboardDraft {
	// Track the latest `initial` so `reset` and a cross-tab clear return to the
	// caller's current default rather than a value frozen at first mount.
	const initialRef = useRef(initial);
	initialRef.current = initial;

	const [state, setState] = useState<DraftState>(() => {
		try {
			assertDashboardSpec(initial);
			return { spec: initial, error: null };
		} catch (cause) {
			return { spec: initial, error: asSpecError(cause) };
		}
	});

	// Restore a persisted draft on the client only, after the initial render, so
	// server and first client render agree on `initial` and hydration stays
	// stable; then track edits from other tabs. A corrupt draft or a storage
	// error is surfaced, never rendered or thrown.
	useEffect(() => {
		const apply = (raw: string) => {
			try {
				setState({ spec: parseDashboardSpec(raw), error: null });
			} catch (cause) {
				setState((prev) => ({ spec: prev.spec, error: asSpecError(cause) }));
			}
		};

		try {
			const raw = window.localStorage.getItem(storageKey);
			if (raw !== null) apply(raw);
		} catch (cause) {
			setState((prev) => ({
				spec: prev.spec,
				error: storageError("load", cause),
			}));
		}

		const onStorage = (event: StorageEvent) => {
			if (event.key !== storageKey) return;
			// A removed/cleared key elsewhere returns this tab to the default.
			if (event.newValue === null) {
				setState({ spec: initialRef.current, error: null });
				return;
			}
			apply(event.newValue);
		};
		window.addEventListener("storage", onStorage);
		return () => window.removeEventListener("storage", onStorage);
	}, [storageKey]);

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
			let error: Error | null = null;
			try {
				window.localStorage.setItem(storageKey, JSON.stringify(next));
			} catch (cause) {
				error = storageError("save", cause);
			}
			setState({ spec: next, error });
		},
		[storageKey],
	);

	const reset = useCallback(() => {
		let error: Error | null = null;
		try {
			window.localStorage.removeItem(storageKey);
		} catch (cause) {
			error = storageError("reset", cause);
		}
		setState({ spec: initialRef.current, error });
	}, [storageKey]);

	return { spec: state.spec, setSpec, reset, error: state.error };
}
