"use client";

import Link from "next/link";
import { useSyncExternalStore } from "react";

import { decodeJWTPayload } from "@/lib/auth-session";
import {
	authedFetch,
	getToken,
	subscribeToken,
} from "@/lib/connect/token-store";

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
let stopWatchingToken: (() => void) | null = null;

// Whose list `snapshot` is. The list is per viewer and per organization (#949),
// and it lives in this module, not in a component — so it outlives an unmount, a
// sign-out and the next person signing in on the same tab. Without an owner, the
// next viewer is shown the previous viewer's solutions until their own first
// poll succeeds, and indefinitely if it never does.
let snapshotViewer: string | null = null;

/**
 * The viewer the current token speaks for: the effective subject (the
 * impersonated user during an impersonation, as the server narrows on) in its
 * organization, or null when signed out.
 *
 * This reads the token's claims without verifying them, which is deliberate and
 * safe HERE: the answer only decides whether to throw away a client-side copy of a
 * list, never what anyone may see — the server narrows on the identity the gateway
 * verified. It is the same presentational use lib/auth-session.ts's
 * resolveSessionUser makes of the claims.
 */
function currentViewer(): string | null {
	const token = getToken();
	if (!token) return null;
	const claims = decodeJWTPayload(token);
	const subject =
		typeof claims.acting === "string" && claims.acting
			? claims.acting
			: typeof claims.sub === "string"
				? claims.sub
				: "";
	const org = typeof claims.org === "string" ? claims.org : "";
	return `${subject}\u0000${org}`;
}

function publish(next: SolutionNav[]): void {
	// Keep the reference stable when nothing changed so subscribers don't
	// re-render on every poll (useSyncExternalStore compares by identity).
	if (sameList(snapshot, next)) return;
	snapshot = next;
	for (const listener of listeners) {
		listener();
	}
}

/**
 * Drop the list when it belongs to someone else. A routine token refresh keeps the
 * same viewer, so it changes nothing and the menu does not flicker; a sign-out, a
 * different person, or a switch of organization empties it at once.
 */
function forgetIfViewerChanged(): boolean {
	const viewer = currentViewer();
	if (viewer === snapshotViewer) return false;
	snapshotViewer = viewer;
	publish(EMPTY);
	return true;
}

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
	forgetIfViewerChanged();
	const viewer = snapshotViewer;
	if (viewer === null) return;
	try {
		// The menu is this viewer's own (#949), so the listing is authenticated and
		// answers what THIS viewer may use. authedFetch attaches the host's bearer
		// and, on a lapsed access token, exchanges it once and retries — the same
		// recovery every other authenticated call gets. A bare fetch would 401 on
		// every poll after the access token aged out and empty a working menu.
		const response = await authedFetch("/api/solutions", {
			cache: "no-store",
		});
		// Answered for a viewer who is no longer here: a poll started before a
		// sign-out or a switch must not paint its list over the next viewer's.
		if (currentViewer() !== viewer) return;
		if (response.status === 401) {
			// authedFetch already tried to recover the session and could not, so
			// this viewer is no longer signed in. Their list is not kept on screen.
			publish(EMPTY);
			return;
		}
		if (!response.ok) {
			// Every other non-OK answer keeps this viewer's last known list. 503 is
			// an unreadable registry or an authority that could not answer; 429 is
			// the organization's read budget. Treating any of them as "no solutions"
			// would empty a working nav on a blip — the reason the route
			// distinguishes them from an empty projection at all.
			return;
		}
		const data: { solutions?: SolutionNav[] } = await response.json();
		if (currentViewer() !== viewer) return;
		publish(data.solutions ?? []);
	} catch {
		// Network blip — keep the last known list.
	}
}

function subscribe(listener: Listener): () => void {
	listeners.add(listener);
	if (listeners.size === 1) {
		void refresh();
		timer = setInterval(refresh, 10_000);
		// Re-check ownership the moment the token changes rather than on the next
		// tick, so a new viewer never waits up to one poll with someone else's list.
		stopWatchingToken = subscribeToken(() => {
			if (forgetIfViewerChanged()) void refresh();
		});
	}
	return () => {
		listeners.delete(listener);
		if (listeners.size === 0) {
			if (timer) {
				clearInterval(timer);
				timer = null;
			}
			stopWatchingToken?.();
			stopWatchingToken = null;
		}
	};
}

function getSnapshot(): SolutionNav[] {
	// React reads this during render, before subscribe has run: on a remount after
	// a sign-out or a switch, returning `snapshot` as-is would paint the previous
	// viewer's list for that render. EMPTY is a constant, so the answer stays
	// stable for useSyncExternalStore until refresh publishes this viewer's own.
	return currentViewer() === snapshotViewer ? snapshot : EMPTY;
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
