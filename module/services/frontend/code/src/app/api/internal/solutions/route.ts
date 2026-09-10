import { isTrustedInternalCall } from "@/lib/internal-token";
import { detailProjection, loadSolutions } from "@/solutions/registry";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

/**
 * Internal detail lookup for the registered solutions: the fields a caller
 * needs to resolve a solution's remote and its backend, which the public
 * navigation projection (app/api/solutions/register) does not carry.
 *
 * Its one caller today is the proxy's CSP derivation (src/proxy.ts), which runs
 * in a context whose module singletons are not shared with route handlers and
 * so cannot read the in-process registry directly. It asks over loopback with
 * the same cluster-internal token registration requires.
 */
export async function GET(request: Request): Promise<Response> {
	if (!isTrustedInternalCall(request)) {
		return Response.json({ error: "unauthorized" }, { status: 401 });
	}
	return Response.json({ solutions: loadSolutions().map(detailProjection) });
}
