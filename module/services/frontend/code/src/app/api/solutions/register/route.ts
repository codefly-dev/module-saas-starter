import {
	loadSolutions,
	parseManifest,
	registerSolution,
	unregisterSolution,
} from "@/solutions/registry";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

/**
 * Self-registration endpoint. A solution POSTs its manifest here on startup so
 * the host learns about it at runtime. This is generic: the host validates the
 * shape and stores it, never referencing any specific solution.
 *
 * NOTE: cluster-internal only. In a deployed environment this must be reachable
 * solely from inside the mesh (NetworkPolicy), never from the public edge.
 */
export async function POST(request: Request): Promise<Response> {
	let body: unknown;
	try {
		body = await request.json();
	} catch {
		return Response.json({ error: "invalid_json" }, { status: 400 });
	}
	const manifest = parseManifest(body);
	if (!manifest) {
		return Response.json({ error: "invalid_manifest" }, { status: 422 });
	}
	registerSolution(manifest);
	return Response.json({ ok: true, id: manifest.id });
}

export async function DELETE(request: Request): Promise<Response> {
	const id = new URL(request.url).searchParams.get("id");
	if (!id) {
		return Response.json({ error: "missing_id" }, { status: 400 });
	}
	unregisterSolution(id);
	return Response.json({ ok: true });
}

export async function GET(): Promise<Response> {
	return Response.json({ solutions: loadSolutions() });
}
