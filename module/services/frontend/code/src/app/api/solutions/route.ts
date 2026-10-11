import { entitlementFailureResponse, isEntitlementFailure, viewerEntitlements } from "@/solutions/entitlements";
import { cachedProjection, entitledSolutions, navProjection } from "@/solutions/projections";
import { loadSolutionsWithRevision } from "@/solutions/registry";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

// Read the viewer's navigation from declared presence and verified entitlements.
export async function GET(request: Request): Promise<Response> {
	const entitlements = await viewerEntitlements(request);
	if (isEntitlementFailure(entitlements)) {
		return entitlementFailureResponse(entitlements);
	}
	const registered = await loadSolutionsWithRevision();
	// An empty registry and an unreadable one must not render the same: the
	// first correctly shows no solutions, the second would silently empty a
	// working navigation.
	if (registered === "unavailable") {
		return Response.json({ error: "registry_unavailable" }, { status: 503 });
	}
	// The nav projection has no client kind of its own — this host's own web app is
	// the one consumer — so the kind component of the cache key is a constant
	// naming it, kept distinct from any registered client kind.
	const solutions = cachedProjection(
		entitlements,
		"\u0000host-nav",
		registered.revision,
		() =>
			entitledSolutions(registered.solutions, entitlements).map(
				({ manifest, entitlement }) => navProjection(manifest, entitlement),
			),
	);
	return Response.json({ solutions });
}
