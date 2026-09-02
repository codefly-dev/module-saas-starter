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
	// The last read or write failure, or null after a success.
	error: Error | null;
	create(input: CreateDashboardInput): Promise<DashboardRecord>;
	rename(id: string, name: string): Promise<DashboardRecord>;
	setVisibility(
		id: string,
		visibility: DashboardVisibility,
	): Promise<DashboardRecord>;
	// Copy an existing dashboard into a new private one. Resolves null when the
	// source id is gone.
	duplicate(id: string): Promise<DashboardRecord | null>;
	remove(id: string): Promise<void>;
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

	useEffect(() => {
		let cancelled = false;
		supersededRef.current = false;

		Promise.resolve(activeLibrary.list()).then(
			(loaded) => {
				if (!cancelled && !supersededRef.current) setRecords(byRecency(loaded));
			},
			(cause) => {
				if (!cancelled) setError(cause as Error);
			},
		);

		const unsubscribe = activeLibrary.subscribe((change) => {
			if (cancelled) return;
			supersededRef.current = true;
			if (change.kind === "error") {
				setError(change.error);
				return;
			}
			setError(null);
			setRecords(byRecency(change.records));
		});

		return () => {
			cancelled = true;
			unsubscribe();
		};
	}, [activeLibrary]);

	const create = useCallback(
		(input: CreateDashboardInput) =>
			Promise.resolve(activeLibrary.create(input)),
		[activeLibrary],
	);

	const rename = useCallback(
		(id: string, name: string) =>
			Promise.resolve(activeLibrary.update(id, { name })),
		[activeLibrary],
	);

	const setVisibility = useCallback(
		(id: string, visibility: DashboardVisibility) =>
			Promise.resolve(activeLibrary.update(id, { visibility })),
		[activeLibrary],
	);

	const duplicate = useCallback(
		async (id: string) => {
			const source = await activeLibrary.get(id);
			if (source === null) return null;
			return activeLibrary.create({
				name: `${source.name} (copy)`,
				spec: source.spec,
				visibility: DEFAULT_DASHBOARD_VISIBILITY,
			});
		},
		[activeLibrary],
	);

	const remove = useCallback(
		(id: string) =>
			Promise.resolve(activeLibrary.remove(id)).then(() => undefined),
		[activeLibrary],
	);

	return { records, error, create, rename, setVisibility, duplicate, remove };
}

// The starting spec for a freshly created, empty dashboard: a titled canvas the
// user fills from the editor.
export function emptyDashboardSpec(name: string) {
	return dashboard({ title: name.trim(), metrics: [] });
}
