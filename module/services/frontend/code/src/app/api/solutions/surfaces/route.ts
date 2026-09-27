import { requestPublicOrigin } from "@/lib/public-origin";
import { viewerEntitlements } from "@/solutions/entitlements";
import {
	cachedProjection,
	entitledSolutions,
	isClientKind,
	loadSolutionsWithRevision,
	surfacesProjection,
	type SolutionClientSurfaces,
} from "@/solutions/registry";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

/**
 * The client-facing projection of what the CALLING VIEWER may use inside one
 * kind of client — a word processor, a spreadsheet — rather than inside this
 * host's own pages.
 *
 * A client that cannot ask this has to ship a table of which solution offers
 * what, which goes stale the moment a solution is registered, withdrawn, or
 * changes what it offers. Asking for its own kind gets it exactly what applies
 * and nothing else.
 *
 * This route is authenticated (issue #949). It used to be deliberately ungated,
 * and answered every caller the same deployment-wide set — so the list was a
 * function of what is *registered*, not of what the caller may *use*. Narrowing it
 * needs a verified organization and viewer, which exist only behind the gateway's
 * `ext_authz`; see solutions/entitlements.ts for why nothing here may derive them.
 *
 * An unauthenticated caller therefore gets 401, not an empty list. `[]` would make
 * "you are not signed in" and "your organization installed nothing" the same
 * answer, and a client told the second goes looking at its registration instead of
 * its session.
 *
 * It still carries each solution's origin, because a surface module is a path the
 * client fetches against that origin: withholding it would not keep the origin
 * from a caller that can use a surface at all, only make the answer unusable. The
 * manifest path and backend service are still withheld.
 */
export async function GET(request: Request): Promise<Response> {
	// The kind is required, never defaulted to "all": a projection that widens
	// when the parameter is forgotten would hand every client every other
	// client's surfaces, which is the opposite of what asking for a kind means.
	const client = new URL(request.url).searchParams.get("client");
	if (!client) {
		return Response.json({ error: "missing_client" }, { status: 400 });
	}
	// A kind the registry would refuse to store cannot be served by anyone, so
	// it is a malformed request rather than an empty result. Answering [] would
	// read as "nothing is offered for you" and send the caller looking at its
	// registration instead of its spelling.
	if (!isClientKind(client)) {
		return Response.json({ error: "invalid_client" }, { status: 400 });
	}
	// Asked before the registry is read: a caller with no standing to see any
	// projection must not be able to probe whether the registry is healthy.
	const entitlements = await viewerEntitlements(request);
	if (entitlements === "unauthenticated") {
		return Response.json({ error: "unauthenticated" }, { status: 401 });
	}
	if (entitlements === "forbidden") {
		return Response.json({ error: "no_organization" }, { status: 403 });
	}
	if (entitlements === "unavailable") {
		// The authority could not answer. Distinct from an empty entitlement set
		// for the same reason an unreadable registry is distinct from an empty one:
		// answering [] would silently retract every surface a client is showing.
		return Response.json({ error: "authority_unavailable" }, { status: 503 });
	}
	const registered = await loadSolutionsWithRevision();
	// An empty registry and an unreadable one must not render the same: the
	// first correctly offers nothing, the second would silently retract every
	// surface a client is currently showing.
	if (registered === "unavailable") {
		return Response.json({ error: "registry_unavailable" }, { status: 503 });
	}
	// A solution served through this host resolves its modules against this
	// host's own public origin (see surfacesProjection).
	const hostOrigin = requestPublicOrigin(request);
	// The origin is part of the projection, so it is part of what may be reused:
	// two callers reaching this host through different public origins must not
	// share a cached answer that resolves a module against one of them.
	const solutions = cachedProjection(
		entitlements,
		`${client}\u0000${hostOrigin ?? ""}`,
		registered.revision,
		() =>
			entitledSolutions(registered.solutions, entitlements)
				.map(({ manifest, entitlement }) =>
					surfacesProjection(manifest, client, entitlement, hostOrigin),
				)
				.filter(
					(projected): projected is SolutionClientSurfaces =>
						projected !== null,
				),
	);
	return Response.json({ solutions });
}
