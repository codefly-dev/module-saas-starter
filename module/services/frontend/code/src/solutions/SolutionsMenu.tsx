"use client";

import Link from "next/link";
import { useSyncExternalStore } from "react";

import { authedFetch } from "@/lib/connect/token-store";

interface SolutionNav {
	id: string;
	nav: { title: string; path: string; order?: number };
	/**
	 * False when the solution is installed and granted but its installation is
	 * not healthy. It stays in the menu, disabled: the organization did install it
	 * and this viewer was granted it, so hiding it would send someone looking for
	 * a grant that already exists.
	 */
	available?: boolean;
}

// Single shared poll loop for the registered-solutions list. Every mounted
// consumer (sidebar group + home cards) subscribes to this one store instead of
// each spinning up its own interval and fetch, so the dashboard makes one
// request every 10s regardless of how many components read the list.
type Listener = () => void;

const EMPTY: SolutionNav[] = [];
let snapshot: SolutionNav[] = EMPTY;
const listeners = new Set<Listener>();
let timer: ReturnType<typeof setInterval> | null = null;

function sameList(a: SolutionNav[], b: SolutionNav[]): boolean {
	if (a === b) return true;
	if (a.length !== b.length) return false;
	return a.every((item, index) => {
		const other = b[index];
		return (
			item.id === other.id &&
			item.nav.title === other.nav.title &&
			item.nav.path === other.nav.path &&
			// Availability is rendered, so a change in it must re-render. Leaving it
			// out of the comparison would freeze a solution's disabled state for the
			// life of the page.
			item.available === other.available
		);
	});
}

async function refresh(): Promise<void> {
	try {
		// The menu is this viewer's own (#949), so the listing is authenticated and
		// answers what THIS viewer may use. authedFetch attaches the host's bearer
		// and, on a lapsed access token, exchanges it once and retries — the same
		// recovery every other authenticated call gets. A bare fetch would 401 on
		// every poll after the access token aged out and empty a working menu.
		const response = await authedFetch("/api/solutions/register", {
			cache: "no-store",
		});
		if (!response.ok) {
			// Every non-OK answer keeps the last known list. 503 is an unreadable
			// registry or an authority that could not answer; 401 is a session this
			// poll could not refresh, which the transport's own recovery handles by
			// redirecting. Treating any of them as "no solutions" would empty a
			// working nav on a blip — the reason the route distinguishes them from an
			// empty projection at all.
			return;
		}
		const data: { solutions?: SolutionNav[] } = await response.json();
		const next = data.solutions ?? [];
		// Keep the reference stable when nothing changed so subscribers don't
		// re-render on every poll (useSyncExternalStore compares by identity).
		if (!sameList(snapshot, next)) {
			snapshot = next;
			for (const listener of listeners) {
				listener();
			}
		}
	} catch {
		// Network blip — keep the last known list.
	}
}

function subscribe(listener: Listener): () => void {
	listeners.add(listener);
	if (listeners.size === 1) {
		void refresh();
		timer = setInterval(refresh, 10_000);
	}
	return () => {
		listeners.delete(listener);
		if (listeners.size === 0 && timer) {
			clearInterval(timer);
			timer = null;
		}
	};
}

function getSnapshot(): SolutionNav[] {
	return snapshot;
}

/**
 * Live list of registered solutions, read from the runtime registry. Solutions
 * self-register at startup, so this reflects whatever is currently deployed —
 * the host names none of them. All consumers share one poll loop (see above).
 */
export function useRegisteredSolutions(): SolutionNav[] {
	return useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
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
				{solutions.map((solution) =>
					solution.available === false ? (
						// Rendered, not linked. The grant exists, so the entry belongs
						// here; the installation does not currently serve, so following
						// it would fail after a navigation rather than before one.
						<div
							key={solution.id}
							aria-disabled="true"
							title="This solution is installed but not available right now."
							className="rounded-xl border p-4 opacity-50"
						>
							<div className="text-sm font-medium">{solution.nav.title}</div>
							<div className="text-xs opacity-60">Unavailable</div>
						</div>
					) : (
						<Link
							key={solution.id}
							href={solution.nav.path}
							className="rounded-xl border p-4 transition-colors hover:bg-accent/40"
						>
							<div className="text-sm font-medium">{solution.nav.title}</div>
							<div className="text-xs opacity-60">{solution.id}</div>
						</Link>
					),
				)}
			</div>
		);
	}
	return (
		<nav className="flex flex-col gap-1">
			{solutions.map((solution) =>
				solution.available === false ? (
					<span
						key={solution.id}
						aria-disabled="true"
						title="This solution is installed but not available right now."
						className="rounded-md px-3 py-2 text-sm opacity-50"
					>
						{solution.nav.title}
					</span>
				) : (
					<Link
						key={solution.id}
						href={solution.nav.path}
						className="rounded-md px-3 py-2 text-sm transition-colors hover:bg-accent/40"
					>
						{solution.nav.title}
					</Link>
				),
			)}
		</nav>
	);
}

/**
 * Dashboard-home "Solutions" section. Renders its own heading only when at
 * least one solution is registered, so the default starter (no solutions) does
 * not show a dangling empty heading.
 */
export function SolutionsHomeSection() {
	const solutions = useRegisteredSolutions();
	if (solutions.length === 0) {
		return null;
	}
	return (
		<section className="flex flex-col gap-3">
			<h2 className="text-sm font-semibold uppercase tracking-wide opacity-60">
				Solutions
			</h2>
			<SolutionsMenu variant="cards" />
		</section>
	);
}
