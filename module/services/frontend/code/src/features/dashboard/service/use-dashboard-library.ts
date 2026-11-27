"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
	type DashboardRecord,
	type DashboardVisibility,
	DEFAULT_DASHBOARD_VISIBILITY,
} from "../model/record";
import { dashboard } from "../model/schema";
import {
	type CreateDashboardInput,
	createBrowserDashboardLibrary,
	type DashboardLibrary,
} from "./dashboard-library";

export interface DashboardLibraryState {
	// Every dashboard the viewer can act on, newest activity first.
	records: DashboardRecord[];
	isLoading: boolean;
	readError: Error | null;
	// The last read or write failure, or null after a success. A failed mutation
	// (a full store, a concurrently-deleted record) surfaces here rather than
	// throwing past the fire-and-forget callers.
	error: Error | null;
	// Rejects on failure so a caller that must not proceed on a failed create
	// (opening the new dashboard) can await it; the failure is also on `error`.
	create(input: CreateDashboardInput): Promise<DashboardRecord>;
	// The fire-and-forget mutations never reject — they surface failures through
	// `error` — so a `void`-invoked handler cannot leak an unhandled rejection.
	rename(id: string, name: string): Promise<boolean>;
	setVisibility(id: string, visibility: DashboardVisibility): Promise<boolean>;
	// Copy an existing dashboard into a new private one. Resolves null when the
	// source id is gone or the copy fails.
	duplicate(id: string): Promise<DashboardRecord | null>;
	remove(id: string): Promise<boolean>;
}

function byRecency(records: DashboardRecord[]): DashboardRecord[] {
	return [...records].sort((a, b) => b.updatedAt.localeCompare(a.updatedAt));
}

/**
 * React state over a {@link DashboardLibrary}. Reads the collection once on
 * mount and then follows the store's subscription, so a mutation from this hook,
 * another tab, or (once server-backed) another device all land the same way. A
 * store defaults to localStorage scoped by the passed key; an authenticated
 * caller injects a server-backed store and the hook API is unchanged. An
 * injected `library` must be a stable reference across renders.
 */
export function useDashboardLibrary(
	storageKey: string,
	library?: DashboardLibrary,
): DashboardLibraryState {
	const activeLibrary = useMemo(
		() => library ?? createBrowserDashboardLibrary(storageKey),
		[library, storageKey],
	);
	// The subscription is the source of truth after mount; an initial async load
	// must not clobber a change that already arrived.
	const supersededRef = useRef(false);

	const [records, setRecords] = useState<DashboardRecord[]>([]);
	const [error, setError] = useState<Error | null>(null);
	const [readError, setReadError] = useState<Error | null>(null);
	const [isLoading, setLoading] = useState(true);

	const [previousLibrary, setPreviousLibrary] = useState(activeLibrary);
	if (previousLibrary !== activeLibrary) {
		setPreviousLibrary(activeLibrary);
		setRecords([]);
		setError(null);
		setReadError(null);
		setLoading(true);
	}

	useEffect(() => {
		let cancelled = false;
		supersededRef.current = false;

		Promise.resolve()
			.then(() => activeLibrary.list())
			.then(
				(loaded) => {
					if (!cancelled && !supersededRef.current) {
						setRecords(byRecency(loaded));
						setLoading(false);
					}
				},
				(cause) => {
					if (!cancelled && !supersededRef.current) {
						setError(cause as Error);
						setReadError(cause as Error);
						setLoading(false);
						setRecords([]);
					}
				},
			);

		const unsubscribe = activeLibrary.subscribe((change) => {
			if (cancelled) return;
			supersededRef.current = true;
			setLoading(false);
			if (change.kind === "error") {
				setError(change.error);
				setReadError(change.error);
				setRecords([]);
				return;
			}
			setError(null);
			setReadError(null);
			setRecords(byRecency(change.records));
		});

		return () => {
			cancelled = true;
			unsubscribe();
		};
	}, [activeLibrary]);

	// Run a mutation, routing success/failure into `error`. A store op may throw
	// synchronously (localStorage quota) or reject (a server store), so the op is
	// invoked inside the try; on failure `error` is set and the error rethrown so
	// each caller decides whether to reject or absorb it.
	const guard = useCallback(async <T>(op: () => T | Promise<T>): Promise<T> => {
		try {
			const result = await op();
			setError(null);
			return result;
		} catch (cause) {
			setError(cause as Error);
			throw cause;
		}
	}, []);

	const create = useCallback(
		(input: CreateDashboardInput) => guard(() => activeLibrary.create(input)),
		[activeLibrary, guard],
	);

	const rename = useCallback(
		(id: string, name: string) =>
			guard(() => activeLibrary.update(id, { name })).then(
				() => true,
				() => false,
			),
		[activeLibrary, guard],
	);

	const setVisibility = useCallback(
		(id: string, visibility: DashboardVisibility) =>
			guard(() => activeLibrary.update(id, { visibility })).then(
				() => true,
				() => false,
			),
		[activeLibrary, guard],
	);

	const duplicate = useCallback(
		(id: string) =>
			guard(async () => {
				const source = await activeLibrary.get(id);
				if (source === null) return null;
				return activeLibrary.create({
					name: `${source.name} (copy)`,
					spec: source.spec,
					visibility: DEFAULT_DASHBOARD_VISIBILITY,
				});
			}).then(
				(record) => record,
				() => null,
			),
		[activeLibrary, guard],
	);

	const remove = useCallback(
		(id: string) =>
			guard(() => activeLibrary.remove(id)).then(
				() => true,
				() => false,
			),
		[activeLibrary, guard],
	);

	return {
		records,
		error,
		isLoading,
		readError,
		create,
		rename,
		setVisibility,
		duplicate,
		remove,
	};
}

// The starting spec for a freshly created, empty dashboard: a titled canvas the
// user fills from the editor.
export function emptyDashboardSpec(name: string) {
	return dashboard({ title: name.trim(), metrics: [] });
}
