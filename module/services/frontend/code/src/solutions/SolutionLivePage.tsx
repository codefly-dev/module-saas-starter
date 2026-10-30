"use client";

import { useRouter } from "next/navigation";
import { type ReactNode, useEffect } from "react";
import {
	useRegisteredSolutions,
	useRegisteredSolutionsLoaded,
} from "./SolutionsMenu";

/**
 * Reuse the shared, viewer-scoped poll for page withdrawal and live upgrades.
 *
 * It is also the page's admission: the poll is the viewer's entitled navigation
 * projection, so a page appears only for a viewer whose organization installed
 * its target and who holds a grant there. The server render cannot decide that
 * — it holds no verified viewer (SOLUTION_REGISTRATION.md §4) — so nothing the
 * page mounts (the remote, its Setup tab, its dashboard) is rendered until the
 * projection names it, and a viewer it never names sees `denied`: the same
 * answer as an id nothing is registered under, so neither discloses the other.
 */
export function SolutionLivePage({
	id,
	assetVersion,
	denied,
	children,
}: {
	id: string;
	assetVersion?: string;
	denied?: ReactNode;
	children: ReactNode;
}) {
	const router = useRouter();
	const current = useRegisteredSolutions().find((entry) => entry.id === id);
	const loaded = useRegisteredSolutionsLoaded();
	const changed = current?.assetVersion !== assetVersion;
	const updating = !!current && current.available !== false && changed;
	useEffect(() => {
		if (!updating) return;
		router.refresh();
		// Another frontend replica may still hold its five-second snapshot. Retry
		// while it catches up; never keep rendering the previous remote meanwhile.
		const retry = setInterval(() => router.refresh(), 5_000);
		return () => clearInterval(retry);
	}, [updating, current?.assetVersion, router]);
	if (!loaded) return <p className="p-6 text-sm opacity-70">Loading page…</p>;
	if (!current) {
		return (
			denied ?? (
				<p className="p-6 text-sm opacity-70">
					This page is not available to you.
				</p>
			)
		);
	}
	// Entitled, but its installation does not serve right now.
	if (current.available === false) {
		return (
			<p className="p-6 text-sm opacity-70">
				This page is not available right now.
			</p>
		);
	}
	if (changed) return <p className="p-6 text-sm opacity-70">Updating page…</p>;
	return children;
}
