import "server-only";

import { assertDataGraph, type DataGraph } from "@codefly/saas-plugin-manifest";
import { getEndpoints, getWorkspaceSecret } from "codefly";

/**
 * Runtime solution registry (generic host seam).
 *
 * A "solution" is an independently deployed module the host has NO build-time
 * knowledge of. Solutions self-register at startup by POSTing their manifest to
 * the host (see app/api/solutions/register). Nothing in this file — or anywhere
 * in the saas module — names a specific solution.
 *
 * Registrations are NOT stored in this process. They live in one durable,
 * versioned record per solution (issue #534), which the auth-gateway brokers:
 * this frontend has no route to the accounts internal listener, and the gateway
 * already brokers that listener for module registration. What lives here is a
 * short-lived cache of the snapshot, rebuilt on demand, so a restart recovers
 * every registration and two replicas serve the same set.
 *
 * The host only ever renders a solution whose record is ACTIVE — both halves
 * registered, leases live, contract versions agreeing. A solution that
 * registered its page but not its backend is durable and visible to an
 * operator, and deliberately absent from the navigation: it is exactly the
 * disagreement this registry exists to prevent.
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

/** The public navigation projection (see navProjection). */
export type SolutionNav = Pick<SolutionManifest, "id" | "nav">;

/** The internal detail projection (see detailProjection). */
export type SolutionDetail = Omit<SolutionManifest, "dashboard">;

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

/**
 * How long a snapshot is served before the next read refetches it. This is the
 * frontend's half of the convergence bound: a registration made anywhere is
 * reflected here within this window plus the gateway's own reconcile interval.
 * It is short because the read is one loopback-adjacent JSON fetch of a list
 * that holds tens of entries at most.
 */
const SNAPSHOT_TTL_MS = 5_000;

/**
 * The oldest a snapshot may be and still be served while a refetch is failing.
 *
 * A blip must not empty the navigation, so a failed refetch keeps the previous
 * snapshot — but only up to the point where the gateway would already have
 * dropped every record in it. The gateway re-derives liveness from each
 * registration's lease, so once a snapshot is older than that lease nothing in
 * it is provably still registered: serving it renders pages whose backend the
 * gateway has already stopped routing, which is the page/backend disagreement
 * this registry exists to prevent. Past the lease this replica reports
 * "unavailable" rather than guessing.
 *
 * The window belongs to the gateway — it is the lease it grants registrants —
 * and now travels with every snapshot as `leaseSeconds`. This literal is only
 * the fallback for a rolling upgrade in which a newer frontend reads an older
 * gateway; it is deliberately not a second source of truth, because a mirrored
 * copy goes silently wrong the moment the gateway's lease changes.
 */
const FALLBACK_SNAPSHOT_MAX_AGE_MS = 120_000;

interface RegistrySnapshot {
	revision: number;
	solutions: SolutionManifest[];
	byId: Map<string, SolutionManifest>;
	expiresAt: number;
	/** When the gateway last answered; the staleness ceiling is measured from here. */
	fetchedAt: number;
	/** The gateway's lease window: how long this may outlive a failed refetch. */
	maxAgeMs: number;
}

/**
 * Why a read could not be answered from authoritative state. `unavailable` is
 * deliberately distinct from an empty registry: a caller must be able to tell
 * "no solution is registered" from "this replica cannot see the registry", and
 * answer 503 rather than 404 for the second.
 */
export type SolutionRegistryFailure = "unavailable";

// Next's dev server evaluates route handlers and pages in separate module
// graphs, so a plain module-level variable is NOT shared between the
// registration endpoint and the solution page. Anchor the cache on globalThis
// so every module graph in this process shares one snapshot and one in-flight
// fetch.
const globalForRegistry = globalThis as typeof globalThis & {
	__solutionSnapshot?: RegistrySnapshot | null;
	__solutionSnapshotInFlight?: Promise<RegistrySnapshot | null> | null;
};

const GATEWAY_REGISTRY_PATH = "/solutions/_registry";
const GATEWAY_FRONTEND_REGISTER_PATH = "/solutions/_frontend";
const GATEWAY_REGISTER_PATH = "/solutions/_register";
const INTERNAL_TOKEN_HEADER = "X-Codefly-Internal-Token";

/**
 * The auth-gateway origin, resolved from Codefly service discovery. It must NOT
 * be derived from the incoming request (the client-controlled Host header):
 * routing a server-side fetch through Host is an SSRF sink.
 */
function gatewayOrigin(): string | null {
	const endpoint = getEndpoints().find(
		(candidate) =>
			candidate.service === "auth-gateway" && candidate.name === "rest",
	);
	if (!endpoint?.address) return null;
	try {
		return new URL(endpoint.address).origin;
	} catch {
		return null;
	}
}

/** The cluster-internal credential the registry endpoints require. */
function internalToken(): string | null {
	const token = getWorkspaceSecret(
		"internal-auth",
		"CODEFLY_INTERNAL_TOKEN",
	)?.trim();
	return token ? token : null;
}

interface GatewayRegistryEntry {
	id?: unknown;
	status?: unknown;
	manifest?: unknown;
}

/**
 * Turn the gateway's projection into manifests. A record is admitted only when
 * it is ACTIVE and its manifest still parses: the document crossed a process
 * boundary since it was validated, and re-validating is cheaper than trusting
 * that a stored blob is still well-formed.
 */
function manifestsFromSnapshot(payload: unknown): {
	revision: number;
	maxAgeMs: number;
	solutions: SolutionManifest[];
} | null {
	if (typeof payload !== "object" || payload === null) return null;
	const { revision, solutions, leaseSeconds } = payload as {
		revision?: unknown;
		solutions?: unknown;
		leaseSeconds?: unknown;
	};
	if (!Array.isArray(solutions)) return null;
	const manifests: SolutionManifest[] = [];
	for (const entry of solutions as GatewayRegistryEntry[]) {
		if (entry?.status !== "active" || typeof entry.manifest !== "string") {
			continue;
		}
		let parsed: SolutionManifest | null = null;
		try {
			parsed = parseManifest(JSON.parse(entry.manifest));
		} catch {
			parsed = null;
		}
		if (parsed === null) {
			console.error(
				`solution registry: stored manifest for ${String(entry.id)} no longer validates`,
			);
			continue;
		}
		manifests.push(parsed);
	}
	manifests.sort((a, b) => (a.nav.order ?? 0) - (b.nav.order ?? 0));
	return {
		revision: typeof revision === "number" ? revision : 0,
		maxAgeMs:
			typeof leaseSeconds === "number" && leaseSeconds > 0
				? leaseSeconds * 1_000
				: FALLBACK_SNAPSHOT_MAX_AGE_MS,
		solutions: manifests,
	};
}

async function fetchSnapshot(): Promise<RegistrySnapshot | null> {
	const origin = gatewayOrigin();
	const token = internalToken();
	if (!origin || !token) {
		console.error(
			"solution registry: gateway endpoint or internal token unresolved",
		);
		return null;
	}
	let response: Response;
	try {
		response = await fetch(`${origin}${GATEWAY_REGISTRY_PATH}`, {
			headers: { [INTERNAL_TOKEN_HEADER]: token, accept: "application/json" },
			cache: "no-store",
		});
	} catch (err) {
		console.error("solution registry: gateway unreachable", err);
		return null;
	}
	if (!response.ok) {
		console.error(`solution registry: gateway answered ${response.status}`);
		return null;
	}
	const parsed = manifestsFromSnapshot(await response.json().catch(() => null));
	if (parsed === null) {
		console.error("solution registry: malformed registry snapshot");
		return null;
	}
	return {
		revision: parsed.revision,
		solutions: parsed.solutions,
		byId: new Map(parsed.solutions.map((solution) => [solution.id, solution])),
		expiresAt: Date.now() + SNAPSHOT_TTL_MS,
		fetchedAt: Date.now(),
		maxAgeMs: parsed.maxAgeMs,
	};
}

/**
 * The current snapshot, refetched when it has aged out. Concurrent readers
 * coalesce onto one fetch, and a failed refetch keeps serving the previous
 * snapshot rather than emptying the navigation on a single blip — a registry
 * outage must degrade, not delete. Degrading is bounded, though: past
 * SNAPSHOT_MAX_AGE_MS the snapshot is dropped rather than served, because
 * beyond the gateway's lease nothing in it is provably still registered.
 */
async function snapshot(): Promise<RegistrySnapshot | null> {
	const cached = globalForRegistry.__solutionSnapshot ?? null;
	if (cached !== null && Date.now() < cached.expiresAt) return cached;
	if (!globalForRegistry.__solutionSnapshotInFlight) {
		globalForRegistry.__solutionSnapshotInFlight = fetchSnapshot()
			.then((fresh) => {
				if (fresh !== null) {
					globalForRegistry.__solutionSnapshot = fresh;
					return fresh;
				}
				const stale = globalForRegistry.__solutionSnapshot ?? null;
				if (
					stale !== null &&
					Date.now() - stale.fetchedAt >= stale.maxAgeMs
				) {
					globalForRegistry.__solutionSnapshot = null;
					return null;
				}
				return stale;
			})
			.finally(() => {
				globalForRegistry.__solutionSnapshotInFlight = null;
			});
	}
	return globalForRegistry.__solutionSnapshotInFlight;
}

/** Drop the cached snapshot so the next read reflects a write immediately. */
function invalidateSnapshot(): void {
	globalForRegistry.__solutionSnapshot = null;
}

/**
 * The outcome of a registration write. `conflict` is the registry refusing a
 * stale or resurrecting write — the caller must re-read and retry, not retry
 * blindly.
 */
export type SolutionWriteResult =
	| { ok: true; revision: number; status: string }
	| { ok: false; reason: "unavailable" | "conflict" | "forbidden" };

async function writeToGateway(
	path: string,
	init: RequestInit,
): Promise<SolutionWriteResult> {
	const origin = gatewayOrigin();
	const token = internalToken();
	if (!origin || !token) {
		console.error(
			"solution registry: gateway endpoint or internal token unresolved",
		);
		return { ok: false, reason: "unavailable" };
	}
	let response: Response;
	try {
		response = await fetch(`${origin}${path}`, {
			...init,
			headers: {
				...(init.headers ?? {}),
				[INTERNAL_TOKEN_HEADER]: token,
				"content-type": "application/json",
			},
			cache: "no-store",
		});
	} catch (err) {
		console.error("solution registry: gateway unreachable", err);
		return { ok: false, reason: "unavailable" };
	}
	if (response.status === 409) return { ok: false, reason: "conflict" };
	if (response.status === 403) return { ok: false, reason: "forbidden" };
	if (!response.ok) {
		console.error(`solution registry: write answered ${response.status}`);
		return { ok: false, reason: "unavailable" };
	}
	invalidateSnapshot();
	const body = (await response.json().catch(() => ({}))) as {
		revision?: unknown;
		status?: unknown;
	};
	return {
		ok: true,
		revision: typeof body.revision === "number" ? body.revision : 0,
		status: typeof body.status === "string" ? body.status : "unknown",
	};
}

/**
 * Register the frontend half of a solution.
 *
 * The manifest travels as the exact JSON text this host validated. The registry
 * stores it verbatim and never reinterprets it, and byte stability is what lets
 * a re-registration be recognised as a lease renewal rather than a change —
 * which keeps a heartbeat from churning every replica's cache.
 */
export function registerSolution(
	manifest: SolutionManifest,
	options: { reactivate?: boolean } = {},
): Promise<SolutionWriteResult> {
	return writeToGateway(GATEWAY_FRONTEND_REGISTER_PATH, {
		method: "POST",
		body: JSON.stringify({
			id: manifest.id,
			manifest: JSON.stringify(manifest),
			reactivate: options.reactivate === true,
		}),
	});
}

/**
 * Deregister a solution outright. Both halves go away together and a tombstone
 * remains, so a retiring deployment's delayed heartbeat cannot recreate what an
 * operator removed.
 */
export function unregisterSolution(id: string): Promise<SolutionWriteResult> {
	return writeToGateway(
		`${GATEWAY_REGISTER_PATH}?id=${encodeURIComponent(id)}`,
		{ method: "DELETE" },
	);
}

/**
 * Every solution the host should render, ordered for the navigation. Returns
 * the failure marker rather than an empty list when no snapshot can be
 * obtained, so a caller never renders "no solutions" for an outage.
 */
export async function loadSolutions(): Promise<
	SolutionManifest[] | SolutionRegistryFailure
> {
	const current = await snapshot();
	return current === null ? "unavailable" : current.solutions;
}

/** One solution by id, or why it could not be resolved. */
export async function findSolution(
	id: string,
): Promise<SolutionManifest | null | SolutionRegistryFailure> {
	const current = await snapshot();
	if (current === null) return "unavailable";
	return current.byId.get(id) ?? null;
}

/**
 * What every signed-in browser may read: the id and the nav entry the Solutions
 * menu renders. A manifest also carries deployment topology — the origin the
 * solution's code is served from, the backend service that fronts it, and its
 * dashboard declaration — which no browser needs to render a link, so the
 * public listing projects it away rather than shipping it to every poll.
 */
export function navProjection(manifest: SolutionManifest): SolutionNav {
	return { id: manifest.id, nav: { ...manifest.nav } };
}

/**
 * What a caller holding the cluster-internal token may read: everything needed
 * to resolve the remote and its backend. The dashboard graph is left out — it
 * is read in-process by the solution page (findSolution), never over HTTP, so
 * no reader would spend the bytes.
 */
export function detailProjection(manifest: SolutionManifest): SolutionDetail {
	return {
		id: manifest.id,
		nav: { ...manifest.nav },
		frontend: { ...manifest.frontend },
		backend: { ...manifest.backend },
	};
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
