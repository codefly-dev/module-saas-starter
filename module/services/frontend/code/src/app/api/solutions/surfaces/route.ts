import { loadSolutions, surfacesProjection } from "@/solutions/registry";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

/**
 * The client-facing projection of what registered solutions offer inside one
 * kind of client — a word processor, a spreadsheet — rather than inside
 * this host's own pages.
 *
 * A client that cannot ask this has to ship a table of which solution offers
 * what, which goes stale the moment a solution is registered, withdrawn, or
 * changes what it offers. Asking for its own kind gets it exactly what applies
 * and nothing else.
 *
 * Like the navigation projection beside it, this is deliberately ungated: it
 * carries only what a solution publishes about itself to the clients it wants
 * to be used from, and none of the deployment topology the internal detail
 * lookup holds. The `module` path is resolved against the solution's own
 * origin, which the caller already holds — this host does not hand one out.
 */
export async function GET(request: Request): Promise<Response> {
	// The kind is required, never defaulted to "all": a projection that widens
	// when the parameter is forgotten would hand every client every other
	// client's surfaces, which is the opposite of what asking for a kind means.
	const client = new URL(request.url).searchParams.get("client");
	if (!client) {
		return Response.json({ error: "missing_client" }, { status: 400 });
	}
	const registered = await loadSolutions();
	// An empty registry and an unreadable one must not render the same: the
	// first correctly offers nothing, the second would silently retract every
	// surface a client is currently showing.
	if (registered === "unavailable") {
		return Response.json({ error: "registry_unavailable" }, { status: 503 });
	}
	return Response.json({
		solutions: registered
			.map((manifest) => surfacesProjection(manifest, client))
			.filter((projected) => projected !== null),
	});
}
