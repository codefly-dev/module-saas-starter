import { checkRuntimeCompatibility } from "@/solutions/compatibility";
import {
	consumeRegistrationToken,
	SOLUTION_REGISTRATION_HEADER,
	type SolutionRegistrationClaims,
	verifySolutionRegistration,
} from "@/solutions/registration-authority";
import {
	loadSolutions,
	navProjection,
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
 * Registration mutates what the nav renders and what the solution route loads
 * as a Module Federation remote — code that then runs in the host origin with
 * the viewer's credentials — so it is authorized on the publisher's OWN signed,
 * solution-bound credential, the same one the gateway requires, rather than on
 * a secret every component in the mesh holds. A credential for one solution
 * confers no authority over another.
 *
 * That credential is the whole boundary on the public front door, and nothing
 * below the application can narrow it: `frontend/http` is a public module
 * export, so this path shares TCP 3000 with every browser-facing page, a
 * NetworkPolicy selects pods and ports rather than paths, and the mesh's L7
 * policy is enforced by a waypoint that never sees ingress-originated traffic.
 * POST/DELETE here are declared as `internal_http_routes` in the topology
 * binding and denied in the mesh from every in-mesh principal outside the
 * frontend's declared callers, which contains lateral use of a leaked
 * credential but does not gate the front door. See
 * module/DEPLOYMENT_TOPOLOGY.md, "Cluster-internal HTTP routes".
 */

/**
 * Verify the presented credential and burn its single use. The jti is burned
 * per process, so the same credential still authorizes the gateway's own check
 * on the write this route forwards.
 */
async function authorize(
	request: Request,
): Promise<SolutionRegistrationClaims | null> {
	const claims = await verifySolutionRegistration(
		request.headers.get(SOLUTION_REGISTRATION_HEADER),
	);
	if (!claims) {
		return null;
	}
	return consumeRegistrationToken(claims) ? claims : null;
}

export async function POST(request: Request): Promise<Response> {
	const claims = await authorize(request);
	if (!claims) {
		return Response.json({ error: "unauthorized" }, { status: 401 });
	}
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
	if (manifest.id !== claims.solution) {
		return Response.json({ error: "solution_not_authorized" }, { status: 403 });
	}
	// Compatibility is enforced BEFORE the write, so an incompatible remote never
	// reaches a browser and an existing, working registration keeps serving. The
	// reasons ride the response and the server log: a registrant told only "409"
	// would have nothing to act on.
	const verdict = checkRuntimeCompatibility(manifest);
	if (!verdict.compatible) {
		console.error(
			`solution registration refused as incompatible: ${manifest.id}: ${verdict.reasons.join("; ")}`,
		);
		return Response.json(
			{ error: "incompatible_runtime", reasons: verdict.reasons },
			{ status: 409 },
		);
	}
	// A solution whose registration was deregistered has to say so to come back:
	// an ordinary retry from a retiring deployment must not resurrect what an
	// operator removed.
	const reactivate =
		typeof body === "object" &&
		body !== null &&
		(body as { reactivate?: unknown }).reactivate === true;
	const result = await registerSolution(manifest, {
		reactivate,
		credential: request.headers.get(SOLUTION_REGISTRATION_HEADER) ?? undefined,
	});
	if (!result.ok) {
		return writeFailure(result.reason);
	}
	return Response.json({
		ok: true,
		id: manifest.id,
		revision: result.revision,
		status: result.status,
	});
}

/**
 * Registration is a write to a shared, versioned record, so it can fail in ways
 * a caller must tell apart: `conflict` means re-read and retry, `forbidden`
 * means the id belongs to someone else, `unavailable` means back off. Answering
 * 200 for any of these would let a solution believe it is serving when it is
 * not — the exact incoherence this registry exists to prevent.
 */
function writeFailure(
	reason: "unavailable" | "conflict" | "forbidden",
): Response {
	switch (reason) {
		case "conflict":
			return Response.json({ error: "revision_conflict" }, { status: 409 });
		case "forbidden":
			return Response.json(
				{ error: "not_registration_owner" },
				{ status: 403 },
			);
		default:
			return Response.json({ error: "registry_unavailable" }, { status: 503 });
	}
}

export async function DELETE(request: Request): Promise<Response> {
	const claims = await authorize(request);
	if (!claims) {
		return Response.json({ error: "unauthorized" }, { status: 401 });
	}
	const id = new URL(request.url).searchParams.get("id");
	if (!id) {
		return Response.json({ error: "missing_id" }, { status: 400 });
	}
	if (id !== claims.solution) {
		return Response.json({ error: "solution_not_authorized" }, { status: 403 });
	}
	const result = await unregisterSolution(
		id,
		request.headers.get(SOLUTION_REGISTRATION_HEADER) ?? undefined,
	);
	if (!result.ok) {
		return writeFailure(result.reason);
	}
	return Response.json({ ok: true, revision: result.revision });
}

// GET is the public navigation projection: the id and the nav entry the browser
// polls to render the Solutions menu, and nothing else. It is deliberately not
// gated on the internal token — every signed-in browser needs it — so it must
// carry no field a browser does not render. Where a solution's code is served
// from, which backend service fronts it, and its dashboard declaration are
// deployment topology; they are served by the internal detail lookup
// (app/api/internal/solutions) to callers holding the cluster-internal token.
export async function GET(): Promise<Response> {
	const registered = await loadSolutions();
	// An empty registry and an unreadable one must not render the same: the
	// first correctly shows no solutions, the second would silently empty a
	// working navigation.
	if (registered === "unavailable") {
		return Response.json({ error: "registry_unavailable" }, { status: 503 });
	}
	return Response.json({ solutions: registered.map(navProjection) });
}
