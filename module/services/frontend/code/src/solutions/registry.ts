import "server-only";

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
		/** Logical Codefly service the gateway proxies solution traffic to. */
		serviceAlias: string;
		capabilityPath?: string;
	};
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
		!nav ||
		typeof nav.title !== "string" ||
		typeof nav.path !== "string" ||
		!frontend ||
		frontend.type !== "module-federation" ||
		typeof frontend.manifestUrl !== "string" ||
		typeof frontend.exposedModule !== "string" ||
		!backend ||
		typeof backend.serviceAlias !== "string"
	) {
		return null;
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
			serviceAlias: backend.serviceAlias,
			capabilityPath:
				typeof backend.capabilityPath === "string"
					? backend.capabilityPath
					: undefined,
		},
	};
}
