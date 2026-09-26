import { checkRuntimeCompatibility } from "@/solutions/compatibility";
import {
	consumeRegistrationToken,
	SOLUTION_REGISTRATION_HEADER,
	type SolutionRegistrationClaims,
	verifySolutionRegistration,
} from "@/solutions/registration-authority";
import {
	observeRegistrationBeat,
	observeRegistrationRemoved,
	UNVERIFIED_REGISTRANT,
} from "@/solutions/registration-log";
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
 *
 * Answers a Response when the credential is not accepted: `401` when it was
 * judged and refused, `503` when this host could not reach the key set to judge
 * it. A registrant told `401` for the second goes looking for a provisioning or
 * ownership fault that does not exist; told `503`, it retries.
 */
async function authorize(
	request: Request,
): Promise<SolutionRegistrationClaims | Response> {
	const verdict = await verifySolutionRegistration(
		request.headers.get(SOLUTION_REGISTRATION_HEADER),
	);
	if (verdict === "unavailable") {
		// Logged by the caller, as a change of state: the key set being
		// unreachable lasts many beats and is one event, not one per beat.
		return Response.json(
			{ error: "registration_authority_unavailable" },
			{ status: 503, headers: { "retry-after": "5" } },
		);
	}
	if (verdict === "invalid" || !consumeRegistrationToken(verdict)) {
		return Response.json({ error: "unauthorized" }, { status: 401 });
	}
	return verdict;
}

/**
 * A registration beat. The heartbeat that calls this is logged by
 * {@link observeRegistrationBeat} at its state changes only — never per beat —
 * and the dev server's own per-request line for this path is switched off in
 * next.config.mjs, so the two together print nothing for a beat that finds the
 * registration as it left it.
 */
export async function POST(request: Request): Promise<Response> {
	const { solution, response, reason } = await registerBeat(request);
	if (response.ok) {
		const body = (await response.clone().json()) as {
			revision: number;
			status: string;
		};
		observeRegistrationBeat(solution, {
			ok: true,
			revision: body.revision,
			status: body.status,
		});
	} else {
		observeRegistrationBeat(solution, {
			ok: false,
			httpStatus: response.status,
			reason: reason ?? "refused",
		});
	}
	return response;
}

/**
 * One beat's answer, with the registrant it is attributed to — the verified
 * solution id, or {@link UNVERIFIED_REGISTRANT} when the credential was not
 * accepted — and, for a refusal, the reason an operator reads.
 */
interface BeatAnswer {
	solution: string;
	response: Response;
	reason?: string;
}

async function registerBeat(request: Request): Promise<BeatAnswer> {
	const claims = await authorize(request);
	if (claims instanceof Response) {
		return {
			solution: UNVERIFIED_REGISTRANT,
			response: claims,
			reason:
				claims.status === 503
					? "registration key set unreachable (answered 503, not a refusal of the credential)"
					: "credential not accepted",
		};
	}
	const solution = claims.solution;
	let body: unknown;
	try {
		body = await request.json();
	} catch {
		return {
			solution,
			response: Response.json({ error: "invalid_json" }, { status: 400 }),
			reason: "invalid_json",
		};
	}
	const manifest = parseManifest(body);
	if (!manifest) {
		return {
			solution,
			response: Response.json({ error: "invalid_manifest" }, { status: 422 }),
			reason: "invalid_manifest",
		};
	}
	if (manifest.id !== claims.solution) {
		return {
			solution,
			response: Response.json(
				{ error: "solution_not_authorized" },
				{ status: 403 },
			),
			reason: `solution_not_authorized (manifest names "${manifest.id}")`,
		};
	}
	// Compatibility is enforced BEFORE the write, so an incompatible remote never
	// reaches a browser and an existing, working registration keeps serving. The
	// reasons ride the response and the server log: a registrant told only "409"
	// would have nothing to act on.
	const verdict = checkRuntimeCompatibility(manifest);
	if (!verdict.compatible) {
		return {
			solution,
			response: Response.json(
				{ error: "incompatible_runtime", reasons: verdict.reasons },
				{ status: 409 },
			),
			reason: `incompatible_runtime: ${verdict.reasons.join("; ")}`,
		};
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
		return {
			solution,
			response: writeFailure(result.reason),
			reason: `registry ${result.reason}`,
		};
	}
	return {
		solution,
		response: Response.json({
			ok: true,
			id: manifest.id,
			revision: result.revision,
			status: result.status,
		}),
	};
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
	if (claims instanceof Response) {
		return claims;
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
	observeRegistrationRemoved(id, result.revision);
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
