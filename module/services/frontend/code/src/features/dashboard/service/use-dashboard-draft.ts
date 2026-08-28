"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import type { DashboardDef } from "../model/schema";
import {
	assertDashboardSpec,
	DashboardSpecError,
	parseDashboardSpec,
} from "../model/validate";

export interface DashboardDraft {
	// The active spec. Always valid, so it is always safe to hand to
	// <Dashboard>; a rejected set never lands here.
	spec: DashboardDef;
	// Replaces the active spec and persists it. A malformed or incoherent spec
	// is rejected: the active spec is left untouched and `error` explains why.
	setSpec: (next: DashboardDef) => void;
	// Discards the persisted draft and returns to the initial spec.
	reset: () => void;
	// The last rejection, or null after a successful set or load.
	error: DashboardSpecError | null;
}

interface DraftState {
	spec: DashboardDef;
	error: DashboardSpecError | null;
}

function asSpecError(cause: unknown): DashboardSpecError {
	return cause instanceof DashboardSpecError
		? cause
		: new DashboardSpecError("spec could not be validated", { cause });
}

/**
 * Holds a dashboard spec in React state backed by localStorage, so a runtime
 * edit — swap a metric, add or remove a widget — is just a new spec object and
 * survives a reload. Every spec that enters is validated: an invalid persisted
 * draft is ignored in favor of `initial`, and an invalid `setSpec` is rejected
 * without disturbing the active spec, so `<Dashboard>` only ever sees a valid
 * spec. Both failures surface through `error` rather than a thrown render.
 *
 * This is the placeholder home for the draft; the eventual owner is the
 * settings service (`@codefly/saas-settings`).
 */
export function useDashboardDraft(
	storageKey: string,
	initial: DashboardDef,
): DashboardDraft {
	// `initial` is the trusted in-code fallback; validating it once turns a
	// malformed default into an eager programmer-facing throw rather than a
	// silent render. It is captured so the hook's identity does not churn when a
	// caller passes an inline object.
	const initialRef = useRef(initial);
	const [state, setState] = useState<DraftState>(() => {
		assertDashboardSpec(initialRef.current);
		return { spec: initialRef.current, error: null };
	});

	// Restore a persisted draft on the client only, after the initial render, so
	// server and first client render agree on `initial` and hydration stays
	// stable. A corrupt draft is surfaced, not rendered.
	useEffect(() => {
		const raw = window.localStorage.getItem(storageKey);
		if (raw === null) return;
		try {
			setState({ spec: parseDashboardSpec(raw), error: null });
		} catch (cause) {
			setState((prev) => ({ spec: prev.spec, error: asSpecError(cause) }));
		}
	}, [storageKey]);

	const setSpec = useCallback(
		(next: DashboardDef) => {
			try {
				assertDashboardSpec(next);
			} catch (cause) {
				setState((prev) => ({ spec: prev.spec, error: asSpecError(cause) }));
				return;
			}
			window.localStorage.setItem(storageKey, JSON.stringify(next));
			setState({ spec: next, error: null });
		},
		[storageKey],
	);

	const reset = useCallback(() => {
		window.localStorage.removeItem(storageKey);
		setState({ spec: initialRef.current, error: null });
	}, [storageKey]);

	return { spec: state.spec, setSpec, reset, error: state.error };
}
