import "server-only";

import type {
	SolutionEntitlement,
	ViewerEntitlements,
} from "@/solutions/entitlements";
import {
	type SolutionClientSurfaces,
	type SolutionManifest,
	type SolutionNav,
	solutionProxyBase,
} from "@/solutions/registry";

/**
 * The per-viewer solution projections (issue #949): the navigation menu and the
 * per-client surface listing, narrowed to what one viewer may use.
 *
 * Kept apart from registry.ts on purpose. The registry decides what is
 * REGISTERED — and through findSolution, whether `/s/{id}` renders at all — and
 * that stays deployment-wide and blind to any tenant's installations. This file
 * is the one place that joins the registered set against a viewer's entitlements.
 * module/tools/solution_registration_boundary_test.go holds the two apart: it
 * scans registry.ts for installation coupling, and would fail if this narrowing
 * moved back into it.
 */

/**
 * The registered solutions this viewer may use, each paired with the entitlement
 * that admitted it, in the order the registry ordered them.
 *
 * The join key is the immutable solution TARGET: `installations.target_id` on the
 * authority side and `manifest.targetId` here, which the registry projection
 * carries from the declaration that produced the record.
 *
 * It used to be the solution id — the route alias — on both sides. An alias is
 * deliberately reusable by a later binding, so that join made a REPLACEMENT
 * solution inherit the predecessor's menu entry and its team exposure, with no
 * administrator having acted. The target is one continuous period of one
 * binding's presence and is never reused, so the replacement joins to nothing.
 *
 * A registered solution with no entitlement is absent: deployed but uninstalled,
 * or installed but not granted to this viewer, are both invisible. An entitlement
 * with no registration is absent too, and deliberately quiet — an organization may
 * hold an installation for a solution this deployment does not serve (it was
 * deregistered, or has not registered yet), and that is not an error to report to
 * the viewer whose menu is being drawn.
 */
export function entitledSolutions(
	registered: SolutionManifest[],
	entitlements: ViewerEntitlements,
): Array<{ manifest: SolutionManifest; entitlement: SolutionEntitlement }> {
	const pairs: Array<{
		manifest: SolutionManifest;
		entitlement: SolutionEntitlement;
	}> = [];
	const joined = new Set<string>();
	for (const manifest of registered) {
		// A registered record that nothing declared carries no target. It is
		// joinable to no entitlement, which is the fail-closed answer: presence
		// nobody declared is presence nobody could have consented to.
		if (manifest.targetId === "") continue;
		const entitlement = entitlements.byTarget.get(manifest.targetId);
		if (entitlement === undefined) continue;
		joined.add(manifest.targetId);
		pairs.push({ manifest, entitlement });
	}
	for (const target of entitlements.byTarget.keys()) {
		if (!joined.has(target)) reportUnjoinableEntitlement(target);
	}
	return pairs;
}

// Identifiers already reported, process-wide. On globalThis for the reason the
// registry snapshot is: Next evaluates route handlers in separate module graphs,
// and a per-graph set would report the same identifier once per graph.
const globalForUnjoinable = globalThis as typeof globalThis & {
	__unjoinableSolutionIdentifiers?: Set<string>;
};

/**
 * Say, once per identifier, that an organization holds an installation that no
 * registration can ever match.
 *
 * Under the alias join this reported a shape mismatch — an installation naming
 * something that was not a slug could never match a registered id. The target
 * join has no shape to mismatch: both sides are uuids assigned by the host. What
 * remains worth saying is narrower and still real: the viewer holds an
 * entitlement for a target the registry does not currently serve.
 *
 * That is ORDINARY and so it is reported at debug volume, not as an error. An
 * organisation keeps an installation while the solution is restarting, being
 * redeployed, or temporarily withdrawn; the host withdrawing presence revokes the
 * installation in the same transaction, so a lasting mismatch means the two have
 * genuinely diverged and an operator should be able to see it without it crying
 * wolf on every rollout.
 */
function reportUnjoinableEntitlement(id: string): void {
	if (!globalForUnjoinable.__unjoinableSolutionIdentifiers) {
		globalForUnjoinable.__unjoinableSolutionIdentifiers = new Set();
	}
	const reported = globalForUnjoinable.__unjoinableSolutionIdentifiers;
	if (reported.has(id)) return;
	reported.add(id);
	console.debug(
		`solution projections: this viewer is entitled to solution target ${JSON.stringify(id)}, which the registry does not currently serve, so it appears in no projection. Ordinary during a redeploy; lasting means presence and installation have diverged.`,
	);
}

/**
 * The cache of computed projections, keyed on everything a projection is a
 * function of.
 *
 * The snapshot cache above it is deployment-wide, because the registered set is.
 * A projection is not: it depends on the viewer's organization, on the authority
 * state that admitted each solution, and on the client kind asked for. So the key
 * carries all four.
 *
 * The revision is a digest of the entitled set itself (see entitlements.ts), which
 * is what makes this safe: a grant or a revoke changes the set, so it changes the
 * key, so a cached menu cannot outlive the grant that justified it. A TTL could not
 * give that — it would only bound how long a revoked viewer keeps seeing a solution.
 *
 * Note what this does NOT cache: the authority answer. Every request asks the
 * gateway. What is reused is the manifest shaping, on a key that provably moves
 * whenever authority does.
 */
const PROJECTION_CACHE_LIMIT = 256;

const globalForProjections = globalThis as typeof globalThis & {
	__solutionProjectionCache?: Map<string, unknown>;
};

function projectionCache(): Map<string, unknown> {
	// On globalThis for the same reason the snapshot is: Next evaluates route
	// handlers in separate module graphs, so a module-level Map would be one cache
	// per graph, each missing what the others computed.
	if (!globalForProjections.__solutionProjectionCache) {
		globalForProjections.__solutionProjectionCache = new Map();
	}
	return globalForProjections.__solutionProjectionCache;
}

/**
 * Memoize a projection on (organization, viewer, authority revision, client kind,
 * registry revision).
 *
 * The registry revision is in the key because the registered set is the other
 * input: a solution re-registering with a new nav title must re-render even though
 * no grant moved.
 *
 * Eviction is oldest-first at a fixed ceiling rather than by time. Every key
 * component is a content fingerprint, so an entry is never stale — only unused —
 * and the ceiling exists to bound memory across viewers, not to expire anything.
 */
export function cachedProjection<T>(
	entitlements: ViewerEntitlements,
	client: string,
	registryRevision: number,
	compute: () => T,
): T {
	const key = [
		entitlements.org,
		entitlements.viewer,
		entitlements.revision,
		client,
		String(registryRevision),
	].join("\u0000");
	const cache = projectionCache();
	const hit = cache.get(key);
	if (hit !== undefined) return hit as T;
	const computed = compute();
	if (cache.size >= PROJECTION_CACHE_LIMIT) {
		const oldest = cache.keys().next();
		if (!oldest.done) cache.delete(oldest.value);
	}
	cache.set(key, computed);
	return computed;
}

/**
 * Drop every cached projection.
 *
 * Exposed for tests. A registry write does NOT need it: the registry revision is
 * part of every key, so a write that changes the registered set changes the key,
 * and a write that changes nothing (a heartbeat renewal) leaves a projection that
 * is still correct.
 */
export function invalidateProjections(): void {
	globalForProjections.__solutionProjectionCache = new Map();
}

/**
 * What one signed-in viewer may read: the id and the nav entry the Solutions menu
 * renders, for a solution that viewer may actually use. A manifest also carries
 * deployment topology — the origin the solution's code is served from, the backend
 * service that fronts it, and its dashboard declaration — which no browser needs
 * to render a link, so the projection drops it rather than shipping it to every
 * poll.
 *
 * The entitlement is a REQUIRED argument, not an optional narrowing. A parameter
 * that could be omitted would widen this projection back to the deployment-wide
 * set the moment a caller forgot it, and the failure would look like working code:
 * every viewer would see every registered solution, which is exactly the defect
 * this narrowing removes. Requiring it means a caller cannot project without
 * having asked the authority.
 */
export function navProjection(
	manifest: SolutionManifest,
	entitlement: SolutionEntitlement,
): SolutionNav {
	return {
		id: manifest.id,
		nav: { ...manifest.nav },
		available: entitlement.healthy,
	};
}

/**
 * What one kind of client may read: the surfaces declared for that kind, named
 * by the solution offering them. A client asks for its own kind and gets
 * exactly what applies to it, so learning what is on offer no longer means
 * shipping a table of who offers what inside every client.
 *
 * Registration is still deployment-wide; what a viewer may USE is not. The
 * entitlement argument is that narrowing, and it is required for the reason
 * navProjection's is: an optional one would silently restore the deployment-wide
 * answer whenever a caller left it out.
 *
 * Returns null when this solution declares nothing for that client, which is
 * how the listing leaves it out rather than listing it empty.
 */
export function surfacesProjection(
	manifest: SolutionManifest,
	client: string,
	entitlement: SolutionEntitlement,
	hostOrigin?: string,
): SolutionClientSurfaces | null {
	const surfaces = (manifest.surfaces ?? []).filter(
		(surface) => surface.client === client,
	);
	if (surfaces.length === 0) {
		return null;
	}
	// A solution served through this host has no origin of its own a client can
	// reach: its modules are paths on its backend, which this host serves under
	// the solution's proxy base. Without the host's own origin there is nothing
	// to resolve them against, so the solution is left out rather than answered
	// with an address that cannot work.
	const servedByHost = manifest.frontend.manifestUrl.startsWith("/");
	if (servedByHost && !hostOrigin) {
		return null;
	}
	const modulePath = (module: string) =>
		servedByHost ? `${solutionProxyBase(manifest.id)}${module}` : module;
	return {
		id: manifest.id,
		title: manifest.nav.title,
		available: entitlement.healthy,
		// A declared module is a path on the solution's origin, so without the
		// origin no caller can fetch one. The origin is not withheld topology
		// here the way the manifest path is: the client fetches the module from
		// it directly, and every signed-in document already carries it in the
		// CSP that admits the same origin's code.
		origin: servedByHost
			? (hostOrigin as string)
			: new URL(manifest.frontend.manifestUrl).origin,
		// A shallow copy would share `applies.tagged` and `events` with the
		// cached snapshot, which outlives this response and is read by every
		// later caller — including other client kinds, which read the same
		// manifest objects.
		surfaces: surfaces.map((surface) => ({
			...surface,
			module: modulePath(surface.module),
			applies:
				typeof surface.applies === "object"
					? { tagged: [...surface.applies.tagged] }
					: surface.applies,
			events: surface.events === undefined ? undefined : [...surface.events],
		})),
	};
}
