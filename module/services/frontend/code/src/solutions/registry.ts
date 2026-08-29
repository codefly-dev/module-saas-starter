import "server-only";

import { assertDataGraph, type DataGraph } from "@codefly/saas-plugin-manifest";

/**
 * Runtime solution registry (generic host seam).
 *
 * A "solution" is an independently deployed module the host has NO build-time
 * knowledge of. Solutions self-register at startup by POSTing their manifest to
 * the host (see app/api/solutions/register). The host stores them here and
 * serves them to the navigation and the solution route. Nothing in this file —
 * or anywhere in the saas module — names a specific solution.
 *
 * The store is process-local. For a single dev/runtime instance that is
 * sufficient; a multi-replica deployment needs a shared store (Postgres/redis)
 * so every replica sees the same registrations. Tracked as a follow-up.
 */

export interface SolutionManifest {
	id: string;
	nav: { title: string; path: string; order?: number };
	frontend: {
		type: "module-federation";
		manifestUrl: string;
		exposedModule: string;
		reactRange?: string;
	};
	backend: {
		/**
		 * Logical Codefly service the gateway proxies solution traffic to.
		 * Optional in the wire manifest — defaults to the solution `id`, so the
		 * common (alias === id) case cannot drift out of sync with the gateway
		 * registration and the page route.
		 */
		serviceAlias: string;
		capabilityPath?: string;
	};
	/**
	 * A data-only dashboard declaration the host renders next to the solution's
	 * Module-Federation surface. It compiles to org-scoped audit queries at
	 * render time (see SolutionDashboard) — the solution ships the declaration,
	 * never charting code or a data source. Absent for a solution that declares
	 * no dashboard.
	 */
	dashboard?: DataGraph;
}

/**
 * A nav path is rendered directly as an <a href> in the sidebar and home
 * cards. It must be a same-origin, absolute in-app path so a manifest can never
 * turn it into an open redirect or a `javascript:`/`data:` URI. Registration is
 * authenticated (see the register route), but the host still refuses to store
 * an unsafe value.
 */
function isSafeNavPath(path: string): boolean {
	if (!path.startsWith("/") || path.startsWith("//")) {
		return false;
	}
	// Reject control chars, space, DEL, and backslash — the levers used to
	// smuggle a scheme or a protocol-relative target past a naive prefix check.
	for (const char of path) {
		const code = char.charCodeAt(0);
		if (code <= 0x20 || code === 0x7f || char === "\\") {
			return false;
		}
	}
	return true;
}

/**
 * A manifest URL is handed to the Module Federation runtime, which fetches and
 * executes the script it points at inside the host origin. It must be an
 * absolute http(s) URL with no embedded credentials — never a relative,
 * `javascript:`, `data:`, or `file:` value.
 */
function isSafeManifestUrl(value: string): boolean {
	try {
		const parsed = new URL(value);
		return (
			(parsed.protocol === "http:" || parsed.protocol === "https:") &&
			parsed.username === "" &&
			parsed.password === ""
		);
	} catch {
		return false;
	}
}

// Next's dev server evaluates route handlers and pages in separate module
// graphs, so a plain module-level Map is NOT shared between the registration
// endpoint and the solution page. Anchor the store on globalThis so every
// module graph in this process sees the same registrations.
const globalForRegistry = globalThis as typeof globalThis & {
	__solutionRegistry?: Map<string, SolutionManifest>;
};
const registry: Map<string, SolutionManifest> =
	globalForRegistry.__solutionRegistry ??
	(globalForRegistry.__solutionRegistry = new Map<string, SolutionManifest>());

export function registerSolution(manifest: SolutionManifest): void {
	registry.set(manifest.id, manifest);
}

export function unregisterSolution(id: string): void {
	registry.delete(id);
}

export function loadSolutions(): SolutionManifest[] {
	return [...registry.values()].sort(
		(a, b) => (a.nav.order ?? 0) - (b.nav.order ?? 0),
	);
}

export function findSolution(id: string): SolutionManifest | null {
	return registry.get(id) ?? null;
}

/** Minimal structural validation of a self-registration payload. */
export function parseManifest(value: unknown): SolutionManifest | null {
	if (typeof value !== "object" || value === null) {
		return null;
	}
	const candidate = value as Record<string, unknown>;
	const nav = candidate.nav as Record<string, unknown> | undefined;
	const frontend = candidate.frontend as Record<string, unknown> | undefined;
	const backend = candidate.backend as Record<string, unknown> | undefined;
	if (
		typeof candidate.id !== "string" ||
		candidate.id === "" ||
		!nav ||
		typeof nav.title !== "string" ||
		typeof nav.path !== "string" ||
		!isSafeNavPath(nav.path) ||
		!frontend ||
		frontend.type !== "module-federation" ||
		typeof frontend.manifestUrl !== "string" ||
		!isSafeManifestUrl(frontend.manifestUrl) ||
		typeof frontend.exposedModule !== "string" ||
		frontend.exposedModule === ""
	) {
		return null;
	}
	// serviceAlias is optional and defaults to the solution id (see the type),
	// collapsing the id/alias/gateway-registration keys into one in the common
	// case so they cannot silently drift.
	const serviceAlias =
		backend &&
		typeof backend.serviceAlias === "string" &&
		backend.serviceAlias !== ""
			? backend.serviceAlias
			: candidate.id;
	// The dashboard slot is optional, but a present-and-malformed graph fails the
	// whole registration closed rather than being dropped: a solution that meant
	// to ship a dashboard should learn its graph is invalid, not silently lose it.
	let dashboard: DataGraph | undefined;
	if (candidate.dashboard !== undefined) {
		try {
			assertDataGraph(candidate.dashboard);
			dashboard = candidate.dashboard;
		} catch {
			return null;
		}
	}
	return {
		id: candidate.id,
		nav: {
			title: nav.title,
			path: nav.path,
			order: typeof nav.order === "number" ? nav.order : undefined,
		},
		frontend: {
			type: "module-federation",
			manifestUrl: frontend.manifestUrl,
			exposedModule: frontend.exposedModule,
			reactRange:
				typeof frontend.reactRange === "string"
					? frontend.reactRange
					: undefined,
		},
		backend: {
			serviceAlias,
			capabilityPath:
				backend && typeof backend.capabilityPath === "string"
					? backend.capabilityPath
					: undefined,
		},
		dashboard,
	};
}
