import "server-only";

import { checkRuntimeCompatibility } from "@/solutions/compatibility";

import { assertDataGraph, type DataGraph } from "@codefly/saas-plugin-manifest";
import type { DeclaredSource } from "@codefly-dev/saas-ui/solution";
import { getEndpoints, getWorkspaceSecret } from "codefly";

/**
 * Read-only projection of the host's declared solution registry. The gateway
 * brokers the durable accounts record; this process caches only its snapshot.
 * Only active records with compatible frontend and backend halves are rendered.
 */

export interface SolutionManifest {
	id: string;
	/**
	 * The immutable solution target the host declared this record under — the
	 * key a per-viewer projection joins entitlements against.
	 *
	 * Stamped from the registry snapshot, NOT parsed from the solution's own
	 * manifest: the manifest is a document the solution wrote, and a solution
	 * naming its own target could claim the identity an organisation consented
	 * to for something else. Empty for a record nothing declared, which joins to
	 * no entitlement.
	 */
	targetId: string;
	/**
	 * Major of the manifest wire shape this solution was built against.
	 * Validated by `checkRuntimeCompatibility` before the read projection renders it.
	 */
	schemaVersion: number;
	nav: { title: string; path: string; order?: number };
	frontend: {
		type: "module-federation";
		manifestUrl: string;
		exposedModule: string;
		/** Major of the host↔remote runtime contract the remote expects. */
		hostContract: number;
		/** Semver range the remote requires of the host's React instance. */
		reactRange?: string;
		/**
		 * Semver ranges the remote requires of the host's other sealed shared
		 * packages, keyed by package name.
		 */
		shared?: Record<string, string>;
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
	/**
	 * What this solution offers inside a client that is not this host's own web
	 * app — a word processor, a spreadsheet. Absent for a solution that
	 * offers nothing outside the host.
	 */
	surfaces?: SolutionSurface[];
	/**
	 * The external sources this solution is built on, declared once at
	 * registration instead of asked of every person who opens it.
	 *
	 * A solution built on one known repository already knows which repository;
	 * putting that question to the tenant is how a shared source manager, or a
	 * copied connect form, ends up inside a solution. The host validates the
	 * declaration here, stores it with the registration, and hands it back to
	 * the mounted remote as `SolutionBinding.declaredSources` — so the kit's
	 * `<DeclaredSourceCard>` can ask only for the credential, and this issue
	 * adds no RPC.
	 *
	 * It is a statement, not an authority: declaring a source connects
	 * nothing, grants nothing, and gives the solution no read of its contents.
	 * Whether the organization has connected it is answered by the host's own
	 * `DatasourceService`, against the viewer's own permissions.
	 */
	sources?: DeclaredSource[];
}

/**
 * One thing a solution offers inside one kind of client. The host stores the
 * declaration and projects it to the client that asked; what a surface means,
 * and when to show it, is settled between the solution and that client.
 */
export interface SolutionSurface {
	id: string;
	/**
	 * The kind of client this surface is for. Deliberately an opaque slug: the
	 * set of kinds is deployment configuration (the registered-client registry),
	 * so a new kind must not need a host release to become addressable.
	 */
	client: string;
	title: string;
	description?: string;
	/** Path, on the solution's own origin, to a self-contained ES module. */
	module: string;
	/** Major of the surface contract the client owns. The host never reads it. */
	contract: number;
	/** Which documents the surface applies to, as the client understands them. */
	applies?: "always" | { tagged: string[] };
	/** Journal namespaces the client reconciles this surface on. */
	events?: string[];
}

/** The per-viewer navigation projection (see navProjection in projections.ts). */
export type SolutionNav = Pick<SolutionManifest, "id" | "nav"> & {
	/**
	 * False when the solution is installed and granted but its installation is
	 * not healthy right now — an offboarded owner of record, a revoked or disabled
	 * agent, a standing grant that lapsed.
	 *
	 * Such a solution stays IN the projection. The organization did install it and
	 * the viewer was granted it, so dropping it would send someone looking for a
	 * grant that already exists; what must not happen is routing it as though it
	 * were serving. A consumer renders it disabled and says why it cannot be
	 * opened.
	 */
	available: boolean;
};

/** The internal detail projection (see detailProjection). */
export type SolutionDetail = Omit<
	SolutionManifest,
	"dashboard" | "surfaces" | "sources"
>;

/** The per-client surface projection (see surfacesProjection in projections.ts). */
export interface SolutionClientSurfaces {
	id: string;
	title: string;
	/** Origin a surface's `module` path is resolved against. */
	origin: string;
	/** See SolutionNav.available — the same distinction, for a non-host client. */
	available: boolean;
	surfaces: SolutionSurface[];
}

/**
 * An id a consumer keys on, and the shape both a client kind and a surface id
 * take. Narrow on purpose: these are addressed in URLs and object keys.
 */
const SAFE_SLUG = /^[a-z0-9](?:[a-z0-9_-]*[a-z0-9])?$/;

/**
 * Whether a value is shaped like a client kind. Registration and the read side
 * share this one rule: a kind the registry would refuse to store must not read
 * back as "nothing is offered for you", which is indistinguishable from a
 * correctly-spelled kind that nobody serves.
 */
export function isClientKind(value: string): boolean {
	return SAFE_SLUG.test(value);
}

/**
 * Whether a value is shaped like a registered solution id — the rule parseManifest
 * admits an id by, and the gateway's own. A value that fails it can never name a
 * registration, however it was spelled elsewhere.
 */
export function isSolutionId(value: string): boolean {
	return SAFE_SLUG.test(value);
}

/**
 * A nav path is rendered directly as an <a href> in the sidebar and home
 * cards, and a surface module is fetched by a client against the solution's own
 * origin. Both must be absolute, path-only values so a manifest can never turn
 * one into an open redirect, a cross-origin fetch, or a `javascript:`/`data:`
 * URI. The host refuses unsafe values when reading the declared manifest.
 */
function isSafeAbsolutePath(path: string): boolean {
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
 * The same-origin base through which this host serves one solution's backend:
 * the solution proxy route (`app/api/solutions/[id]/proxy/[...path]`), which
 * forwards to the gateway's `/solutions/{alias}/…` passthrough — and that
 * passthrough serves `/assets` and `/.well-known` without a bearer precisely so a
 * browser can load a remote from here.
 */
export function solutionProxyBase(id: string): string {
	return `/api/solutions/${encodeURIComponent(id)}/proxy`;
}

/**
 * A manifest URL is handed to the Module Federation runtime, which fetches and
 * executes the script it points at inside the host origin. It is one of:
 *
 * - an absolute http(s) URL with no embedded credentials: a remote served from
 *   an origin the BROWSER can reach, loaded from there;
 * - a root-relative path on the solution's own backend (`/assets/…`): the host
 *   serves it same-origin through the solution proxy, so the registrant never
 *   has to know an address the browser can reach — which a pod cannot know,
 *   and which a `localhost` default gets wrong in every deployment.
 *
 * Never a protocol-relative, `javascript:`, `data:`, or `file:` value, and never
 * a path that climbs out of the solution's own namespace.
 */
function isSafeManifestUrl(value: string): boolean {
	if (value.startsWith("/")) {
		return (
			isSafeAbsolutePath(value) &&
			!value
				.split(/[?#]/, 1)[0]
				.split("/")
				.some((segment) => segment === ".." || segment === ".")
		);
	}
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
 * Where the browser loads a registered remote's manifest from. An absolute URL
 * is used as registered; a root-relative one is a path on the solution's backend
 * and is served through this host's own origin (see solutionProxyBase), so
 * `'self'` covers it in the CSP and nothing pod-local ever reaches a browser.
 *
 * A path already under this solution's proxy base is taken as is, so a
 * registrant that spells the host-served form out lands on the same URL.
 */
export function browserManifestUrl(
	manifest: Pick<SolutionManifest, "id" | "frontend">,
): string {
	const registered = manifest.frontend.manifestUrl;
	if (!registered.startsWith("/")) {
		return registered;
	}
	const base = solutionProxyBase(manifest.id);
	return registered.startsWith(`${base}/`)
		? registered
		: `${base}${registered}`;
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
 * How long the gateway gets to answer a snapshot read. It serves this from its
 * own in-memory cache, so it is fast or it is wedged. Matches the bound
 * src/proxy.ts puts on the same class of loopback lookup.
 */
const REGISTRY_READ_TIMEOUT_MS = 2_000;

interface RegistrySnapshot {
	revision: number;
	solutions: SolutionManifest[];
	byId: Map<string, SolutionManifest>;
	expiresAt: number;
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
// navigation endpoint and the solution page. Anchor the cache on globalThis
// so every module graph in this process shares one snapshot and one in-flight
// fetch.
const globalForRegistry = globalThis as typeof globalThis & {
	__solutionSnapshot?: RegistrySnapshot | null;
	__solutionSnapshotInFlight?: Promise<RegistrySnapshot | null> | null;
	__solutionSnapshotFailure?: { reason: string; reads: number } | null;
};

const GATEWAY_REGISTRY_PATH = "/solutions/_registry";
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
	/**
	 * The immutable solution target this record is declared under, from the
	 * gateway's projection. It is the HOST's fact, never the solution's: it is
	 * read from the record's declaration here and deliberately not from the
	 * manifest the solution itself registered, because a solution must not be
	 * able to name the identity an organisation consented to.
	 */
	targetId?: unknown;
}

/**
 * Turn the gateway's projection into manifests. A record is admitted only when
 * it is ACTIVE and its manifest still parses: the document crossed a process
 * boundary since it was validated, and re-validating is cheaper than trusting
 * that a stored blob is still well-formed.
 */
function manifestsFromSnapshot(payload: unknown): {
	revision: number;
	solutions: SolutionManifest[];
} | null {
	if (typeof payload !== "object" || payload === null) return null;
	const { revision, solutions } = payload as {
		revision?: unknown;
		solutions?: unknown;
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
		if (!checkRuntimeCompatibility(parsed).compatible) {
			console.error(`solution registry: incompatible runtime for ${String(entry.id)}`);
			continue;
		}
		// The target is stamped from the record, overwriting anything the
		// solution's own manifest carried in that field. parseManifest already
		// rejects unknown keys it does not model, but stamping unconditionally
		// means a future parser that let the field through could not be used to
		// claim a target either.
		manifests.push({
			...parsed,
			targetId: typeof entry.targetId === "string" ? entry.targetId : "",
		});
	}
	manifests.sort((a, b) => (a.nav.order ?? 0) - (b.nav.order ?? 0));
	return {
		revision: typeof revision === "number" ? revision : 0,
		solutions: manifests,
	};
}

/**
 * The snapshot read's standing failure, if it is failing.
 *
 * Every browser polling the navigation drives this read, so a registry outage
 * printed a line per read for as long as it lasted. It has to be readable, so it is reported as a condition: the
 * first failed read, the same failure again only at milestone counts, and the
 * recovery. A one-shot latch would be the other failure — quiet while the
 * thing is still broken.
 *
 * It lives beside the snapshot on globalThis, not in a module-level binding,
 * for the reason the snapshot does: every Next module graph in the process
 * shares one registry, and a per-graph copy of this would report the same
 * outage once per graph and recover from it once per graph.
 */
function snapshotUnreadable(reason: string, cause?: unknown): null {
	const failure = globalForRegistry.__solutionSnapshotFailure ?? null;
	if (failure?.reason === reason) {
		failure.reads += 1;
		if (isStandingConditionMilestone(failure.reads)) {
			console.error(
				`solution registry: ${reason}, still, after ${failure.reads} reads`,
			);
		}
		return null;
	}
	globalForRegistry.__solutionSnapshotFailure = { reason, reads: 1 };
	if (cause === undefined) {
		console.error(`solution registry: ${reason}`);
	} else {
		console.error(`solution registry: ${reason}`, cause);
	}
	return null;
}

function snapshotReadable(): void {
	const failure = globalForRegistry.__solutionSnapshotFailure ?? null;
	if (failure === null) {
		return;
	}
	const { reason, reads } = failure;
	globalForRegistry.__solutionSnapshotFailure = null;
	console.info(
		`solution registry: snapshot readable again after ${reads} failed ${reads === 1 ? "read" : "reads"} (${reason})`,
	);
}

async function fetchSnapshot(): Promise<RegistrySnapshot | null> {
	const origin = gatewayOrigin();
	const token = internalToken();
	if (!origin || !token) {
		return snapshotUnreadable("gateway endpoint or internal token unresolved");
	}
	let response: Response;
	try {
		response = await fetch(`${origin}${GATEWAY_REGISTRY_PATH}`, {
			headers: { [INTERNAL_TOKEN_HEADER]: token, accept: "application/json" },
			cache: "no-store",
			signal: AbortSignal.timeout(REGISTRY_READ_TIMEOUT_MS),
		});
	} catch (err) {
		return snapshotUnreadable("gateway unreachable", err);
	}
	if (!response.ok) {
		return snapshotUnreadable(`gateway answered ${response.status}`);
	}
	const parsed = manifestsFromSnapshot(await response.json().catch(() => null));
	if (parsed === null) {
		return snapshotUnreadable("malformed registry snapshot");
	}
	snapshotReadable();
	return {
		revision: parsed.revision,
		solutions: parsed.solutions,
		byId: new Map(parsed.solutions.map((solution) => [solution.id, solution])),
		expiresAt: Date.now() + SNAPSHOT_TTL_MS,
	};
}

/** Coalesce concurrent reads; fail closed when an expired snapshot cannot refresh. */
async function snapshot(): Promise<RegistrySnapshot | null> {
	const cached = globalForRegistry.__solutionSnapshot ?? null;
	if (cached !== null && Date.now() < cached.expiresAt) return cached;
	if (!globalForRegistry.__solutionSnapshotInFlight) {
		globalForRegistry.__solutionSnapshotInFlight = fetchSnapshot()
			.then((fresh) => {
				globalForRegistry.__solutionSnapshot = fresh;
				return fresh;
			})
			.finally(() => {
				globalForRegistry.__solutionSnapshotInFlight = null;
			});
	}
	return globalForRegistry.__solutionSnapshotInFlight;
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

/**
 * The registered set together with the revision it carries — what a caller that
 * caches a projection needs, since the registered set is one of the inputs that
 * projection is a function of.
 *
 * It is a separate reader rather than a widened `loadSolutions` so the callers
 * that only need the list keep the narrower return, and so the revision is
 * obtained from the SAME snapshot the manifests came from. Reading the list and
 * then asking for a revision separately could pair manifests with a revision from
 * a later refetch, and the cache key would then claim a set it did not describe.
 */
export async function loadSolutionsWithRevision(): Promise<
	{ revision: number; solutions: SolutionManifest[] } | SolutionRegistryFailure
> {
	const current = await snapshot();
	if (current === null) return "unavailable";
	return { revision: current.revision, solutions: current.solutions };
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
 * What a caller holding the cluster-internal token may read: everything needed
 * to resolve the remote and its backend. The dashboard graph is left out — it
 * is read in-process by the solution page (findSolution), never over HTTP, so
 * no reader would spend the bytes.
 */
export function detailProjection(manifest: SolutionManifest): SolutionDetail {
	return {
		id: manifest.id,
		targetId: manifest.targetId,
		schemaVersion: manifest.schemaVersion,
		nav: { ...manifest.nav },
		frontend: { ...manifest.frontend },
		backend: { ...manifest.backend },
	};
}

// What a manifest asserts by saying nothing: the major that was in force when
// these fields were introduced. FROZEN at 1 on purpose — deriving it from the
// host's CURRENT major would default every silent manifest to the value it is
// then compared against, so the check could never fail for the one population it
// exists for: a solution built against an older contract meeting an upgraded
// host. Bumping a host major must never move these.
const UNDECLARED_MANIFEST_SCHEMA_MAJOR = 1;
const UNDECLARED_HOST_CONTRACT_MAJOR = 1;

/** A flat map of package name → semver range, or null when the value is not one. */
function parseSharedRanges(
	value: unknown,
): Record<string, string> | null | undefined {
	if (value === undefined) {
		return undefined;
	}
	if (typeof value !== "object" || value === null || Array.isArray(value)) {
		return null;
	}
	// Null-prototype: on a plain object literal, assigning "__proto__" a string is
	// a spec no-op, so a requirement declared under that key would vanish before
	// anything checked it — accepted and silently ignored, the exact failure this
	// gate exists to remove.
	const shared: Record<string, string> = Object.create(null);
	for (const [pkg, range] of Object.entries(value)) {
		if (typeof range !== "string" || range === "") {
			return null;
		}
		shared[pkg] = range;
	}
	return shared;
}

/**
 * Which documents a surface applies to, or null when the value is neither of
 * the two shapes. The host does not evaluate it — the client does — but it
 * must not hand a client a third shape it has no branch for.
 */
function parseApplies(
	value: unknown,
): SolutionSurface["applies"] | null | undefined {
	if (value === undefined) {
		return undefined;
	}
	if (value === "always") {
		return "always";
	}
	if (typeof value !== "object" || value === null) {
		return null;
	}
	const { tagged } = value as { tagged?: unknown };
	if (
		!Array.isArray(tagged) ||
		tagged.some((tag) => typeof tag !== "string" || tag === "")
	) {
		return null;
	}
	return { tagged: [...(tagged as string[])] };
}

/**
 * The declared client surfaces, or null when any one of them is malformed —
 * the whole registration then fails closed, as it does for a dashboard. A
 * solution that meant to offer a surface should learn its declaration is
 * invalid, not silently lose it and look like it offers nothing.
 */
function parseSurfaces(value: unknown): SolutionSurface[] | null | undefined {
	if (value === undefined) {
		return undefined;
	}
	if (!Array.isArray(value)) {
		return null;
	}
	const surfaces: SolutionSurface[] = [];
	const seen = new Set<string>();
	for (const entry of value) {
		if (typeof entry !== "object" || entry === null) {
			return null;
		}
		const candidate = entry as Record<string, unknown>;
		if (
			typeof candidate.id !== "string" ||
			!SAFE_SLUG.test(candidate.id) ||
			typeof candidate.client !== "string" ||
			!SAFE_SLUG.test(candidate.client) ||
			typeof candidate.title !== "string" ||
			candidate.title === "" ||
			typeof candidate.module !== "string" ||
			!isSafeAbsolutePath(candidate.module) ||
			!Number.isInteger(candidate.contract) ||
			(candidate.contract as number) < 1
		) {
			return null;
		}
		if (
			candidate.description !== undefined &&
			typeof candidate.description !== "string"
		) {
			return null;
		}
		if (
			candidate.events !== undefined &&
			(!Array.isArray(candidate.events) ||
				candidate.events.some(
					(event) => typeof event !== "string" || event === "",
				))
		) {
			return null;
		}
		const applies = parseApplies(candidate.applies);
		if (applies === null) {
			return null;
		}
		// A client keys on (solution id, surface id), so two surfaces sharing an
		// id inside one solution make that key ambiguous rather than redundant.
		const key = `${candidate.client}/${candidate.id}`;
		if (seen.has(key)) {
			return null;
		}
		seen.add(key);
		surfaces.push({
			id: candidate.id,
			client: candidate.client,
			title: candidate.title,
			description: candidate.description as string | undefined,
			module: candidate.module,
			contract: candidate.contract as number,
			applies,
			events:
				candidate.events === undefined
					? undefined
					: [...(candidate.events as string[])],
		});
	}
	return surfaces;
}

/**
 * The repository shape `AddGitHubSourceRequest.repo` enforces. Restated here,
 * at the other end of the same journey, because this manifest is stored at
 * registration and read by a card that submits it unedited: a repository the
 * connect RPC would refuse must be refused when it is DECLARED, not when
 * somebody finally presses Connect weeks later and the error names a field
 * they cannot see.
 */
const DECLARED_REPO = /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/;

/**
 * The declared sources, or null when any one of them is malformed — the whole
 * registration then fails closed, as it does for a dashboard or a surface. A
 * solution that meant to declare the repository it is built on should learn
 * its declaration is invalid, not silently lose it and show its users a card
 * asking them to pick a repository.
 *
 * Bounds mirror the connect RPC's (64 paths, 512 characters each, a 255-
 * character ref) for the same reason the pattern does.
 */
function parseDeclaredSources(
	value: unknown,
): DeclaredSource[] | null | undefined {
	if (value === undefined) {
		return undefined;
	}
	if (!Array.isArray(value)) {
		return null;
	}
	const sources: DeclaredSource[] = [];
	const seen = new Set<string>();
	for (const entry of value) {
		if (typeof entry !== "object" || entry === null) {
			return null;
		}
		const candidate = entry as Record<string, unknown>;
		if (
			candidate.provider !== "github" ||
			typeof candidate.repo !== "string" ||
			candidate.repo.length > 255 ||
			!DECLARED_REPO.test(candidate.repo)
		) {
			return null;
		}
		// Blank is refused rather than trimmed away, here and for the label and
		// the paths below. A ref of " " satisfies the connect RPC's bounds (its
		// branch has a maximum and no minimum), so it would be accepted at
		// registration, submitted by the card, accepted by the host, and fail
		// at GitHub against a branch of that name — the late failure this
		// validation exists to prevent. A declaration that means "the default
		// branch" omits the field; one that holds only spaces is a mistake, and
		// a mistake in configuration is reported, not silently repaired.
		if (
			candidate.ref !== undefined &&
			(typeof candidate.ref !== "string" ||
				candidate.ref.trim() === "" ||
				candidate.ref.length > 255)
		) {
			return null;
		}
		if (
			candidate.label !== undefined &&
			(typeof candidate.label !== "string" ||
				candidate.label.trim() === "" ||
				candidate.label.length > 255)
		) {
			return null;
		}
		let paths: string[] | undefined;
		if (candidate.paths !== undefined) {
			if (
				!Array.isArray(candidate.paths) ||
				candidate.paths.length > 64 ||
				candidate.paths.some(
					(path) =>
						typeof path !== "string" ||
						path.trim() === "" ||
						path.length > 512,
				)
			) {
				return null;
			}
			paths = [...(candidate.paths as string[])];
		}
		// Two declarations of the same repository would leave a card with no way
		// to say which one it renders — the same ambiguity the card refuses to
		// resolve when an ORGANIZATION has connected a repository twice, except
		// that here the solution's own manifest is the thing contradicting
		// itself, so it is refused rather than reported.
		const key = candidate.repo.toLowerCase();
		if (seen.has(key)) {
			return null;
		}
		seen.add(key);
		sources.push({
			provider: "github",
			repo: candidate.repo,
			...(paths ? { paths } : {}),
			...(candidate.ref ? { ref: candidate.ref as string } : {}),
			...(candidate.label ? { label: candidate.label as string } : {}),
		});
	}
	return sources;
}

/** Structural validation of a declared solution manifest. */
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
		// The same rule every surface id already answers to. Accepting any
		// non-empty string here let the id carry whatever the caller liked into
		// a log line and into solutionProxyBase's URL path; the id is a slug
		// everywhere it is used, so it is checked as one where it enters.
		!SAFE_SLUG.test(candidate.id) ||
		!nav ||
		typeof nav.title !== "string" ||
		typeof nav.path !== "string" ||
		!isSafeAbsolutePath(nav.path) ||
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
	const surfaces = parseSurfaces(candidate.surfaces);
	if (surfaces === null) {
		return null;
	}
	const sources = parseDeclaredSources(candidate.sources);
	if (sources === null) {
		return null;
	}
	// A declared major must be a real major; an ABSENT one means "built against
	// the contract that existed when the field appeared", which is the only claim
	// the host can act on.
	const schemaVersion =
		candidate.schemaVersion ?? UNDECLARED_MANIFEST_SCHEMA_MAJOR;
	const hostContract = frontend.hostContract ?? UNDECLARED_HOST_CONTRACT_MAJOR;
	if (
		!Number.isInteger(schemaVersion) ||
		(schemaVersion as number) < 1 ||
		!Number.isInteger(hostContract) ||
		(hostContract as number) < 1
	) {
		return null;
	}
	const shared = parseSharedRanges(frontend.shared);
	if (shared === null) {
		return null;
	}
	if (
		frontend.reactRange !== undefined &&
		(typeof frontend.reactRange !== "string" || frontend.reactRange === "")
	) {
		return null;
	}
	return {
		id: candidate.id,
		// parseManifest reads a document the SOLUTION wrote, so it never carries
		// a target: the host stamps it from the registry record in
		// manifestsFromSnapshot. Empty here is therefore correct and not a
		// missing field — a manifest that reached a projection without being
		// stamped joins to no entitlement, which is the fail-closed answer.
		targetId: "",
		schemaVersion: schemaVersion as number,
		nav: {
			title: nav.title,
			path: nav.path,
			order: typeof nav.order === "number" ? nav.order : undefined,
		},
		frontend: {
			type: "module-federation",
			manifestUrl: frontend.manifestUrl,
			exposedModule: frontend.exposedModule,
			hostContract: hostContract as number,
			reactRange: frontend.reactRange as string | undefined,
			shared,
		},
		backend: {
			serviceAlias,
			capabilityPath:
				backend && typeof backend.capabilityPath === "string"
					? backend.capabilityPath
					: undefined,
		},
		dashboard,
		surfaces,
		sources,
	};
}

function isStandingConditionMilestone(occurrences: number): boolean {
	if (occurrences < 10) return false;
	let threshold = 10;
	while (threshold < occurrences) threshold *= 10;
	return threshold === occurrences;
}
