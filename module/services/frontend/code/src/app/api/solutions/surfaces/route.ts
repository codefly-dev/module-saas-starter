import { requestPublicOrigin } from "@/lib/public-origin";
import {
	isClientKind,
	loadSolutions,
	surfacesProjection,
} from "@/solutions/registry";

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
 * Like the navigation projection beside it, this is deliberately ungated, and
 * like it this carries no manifest path and no backend service. It does carry
 * each solution's origin, because a surface module is a path the client fetches
 * against that origin: withholding it would not keep the origin from a caller
 * that can use a surface at all, only make the answer unusable.
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
	const registered = await loadSolutions();
	// An empty registry and an unreadable one must not render the same: the
	// first correctly offers nothing, the second would silently retract every
	// surface a client is currently showing.
	if (registered === "unavailable") {
		return Response.json({ error: "registry_unavailable" }, { status: 503 });
	}
	// A solution served through this host resolves its modules against this
	// host's own public origin (see surfacesProjection).
	const hostOrigin = requestPublicOrigin(request);
	return Response.json({
		solutions: registered
			.map((manifest) => surfacesProjection(manifest, client, hostOrigin))
			.filter((projected) => projected !== null),
	});
}
