"use client";

import Link from "next/link";
import { useEffect, useState } from "react";

interface SolutionNav {
	id: string;
	nav: { title: string; path: string; order?: number };
}

/**
 * Live list of registered solutions, read from the runtime registry. Solutions
 * self-register at startup, so this reflects whatever is currently deployed —
 * the host names none of them.
 */
export function useRegisteredSolutions(): SolutionNav[] {
	const [solutions, setSolutions] = useState<SolutionNav[]>([]);
	useEffect(() => {
		let cancelled = false;
		const load = () =>
			fetch("/api/solutions/register", { cache: "no-store" })
				.then((response) => (response.ok ? response.json() : { solutions: [] }))
				.then((data: { solutions?: SolutionNav[] }) => {
					if (!cancelled) {
						setSolutions(data.solutions ?? []);
					}
				})
				.catch(() => {});
		load();
		const interval = setInterval(load, 10_000);
		return () => {
			cancelled = true;
			clearInterval(interval);
		};
	}, []);
	return solutions;
}

/** Sidebar/inline navigation for registered solutions. */
export function SolutionsMenu({ variant = "list" }: { variant?: "list" | "cards" }) {
	const solutions = useRegisteredSolutions();
	if (solutions.length === 0) {
		return null;
	}
	if (variant === "cards") {
		return (
			<div className="grid gap-3 sm:grid-cols-2">
				{solutions.map((solution) => (
					<Link
						key={solution.id}
						href={solution.nav.path}
						className="rounded-xl border p-4 transition-colors hover:bg-accent/40"
					>
						<div className="text-sm font-medium">{solution.nav.title}</div>
						<div className="text-xs opacity-60">{solution.id}</div>
					</Link>
				))}
			</div>
		);
	}
	return (
		<nav className="flex flex-col gap-1">
			{solutions.map((solution) => (
				<Link
					key={solution.id}
					href={solution.nav.path}
					className="rounded-md px-3 py-2 text-sm transition-colors hover:bg-accent/40"
				>
					{solution.nav.title}
				</Link>
			))}
		</nav>
	);
}
