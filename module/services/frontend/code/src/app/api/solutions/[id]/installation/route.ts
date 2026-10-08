import { entitlementFailureResponse, isEntitlementFailure, viewerEntitlements } from "@/solutions/entitlements";
import { entitledSolutions } from "@/solutions/projections";
import { loadSolutionsWithRevision } from "@/solutions/registry";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

interface RouteContext {
	params: Promise<{ id: string }>;
}

/**
 * The verified viewer organization's installation of ONE solution, for the
 * outlet to hand the solution page (`SolutionBinding.installationId`).
 *
 * It is a projection, so it answers from the gateway-verified viewer's
 * entitlements and nothing the browser said: the route alias in the path only
 * selects the registered record, whose immutable TARGET is the join key — the
 * same join the navigation makes (projections.ts `entitledSolutions`). An alias
 * is reusable and a target is not, so a replacement binding never inherits the
 * predecessor's installation. At most one active installation exists per
 * (organization, target), so the join has at most one answer.
 *
 * A viewer the gateway does not entitle to the target gets 404, the same as a
 * solution that is not registered: neither discloses that the other exists.
 */
export async function GET(request: Request, context: RouteContext): Promise<Response> {
	const { id } = await context.params;
	const entitlements = await viewerEntitlements(request);
	if (isEntitlementFailure(entitlements)) {
		return entitlementFailureResponse(entitlements);
	}
	const registered = await loadSolutionsWithRevision();
	if (registered === "unavailable") {
		return Response.json({ error: "registry_unavailable" }, { status: 503 });
	}
	// The navigation's own join, over the whole registry, so this answer and the
	// menu can never disagree about which solutions the viewer may use.
	const entitlement = entitledSolutions(registered.solutions, entitlements).find(({ manifest }) => manifest.id === id)?.entitlement;
	if (!entitlement || entitlement.installationId === "") {
		return Response.json({ error: "not_installed" }, { status: 404, headers: { "cache-control": "no-store" } });
	}
	return Response.json(
		{ installationId: entitlement.installationId, healthy: entitlement.healthy },
		{ headers: { "cache-control": "no-store" } },
	);
}
