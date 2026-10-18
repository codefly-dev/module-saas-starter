"use client";

import { useRouter } from "next/navigation";
import { type ReactNode, useEffect } from "react";
import {
	useRegisteredSolutions,
	useRegisteredSolutionsLoaded,
} from "./SolutionsMenu";

/** Reuse the shared, viewer-scoped poll for page withdrawal and live upgrades. */
export function SolutionLivePage({
	id,
	assetVersion,
	children,
}: {
	id: string;
	assetVersion?: string;
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
	if (!current || current.available === false) {
		return (
			<p className="p-6 text-sm opacity-70">
				This page is not available to you.
			</p>
		);
	}
	if (changed) return <p className="p-6 text-sm opacity-70">Updating page…</p>;
	return children;
}
