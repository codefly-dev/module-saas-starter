import { checkRuntimeCompatibility } from "@/solutions/compatibility";
import {
	consumeRegistrationToken,
	SOLUTION_REGISTRATION_HEADER,
	type SolutionRegistrationClaims,
	verifySolutionRegistration,
} from "@/solutions/registration-authority";
import {
	observeAuthorityReachable,
	observeAuthorityRefusal,
	observeRegistrationBeat,
	observeRegistrationRemoved,
	type RegistrationRefusal,
} from "@/solutions/registration-log";
import { viewerEntitlements } from "@/solutions/entitlements";
import {
	cachedProjection,
	entitledSolutions,
	loadSolutionsWithRevision,
	navProjection,
	parseManifest,
	registerSolution,
	type SolutionWriteResult,
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
	// Recorded here, where the verdict is known, and not as a registrant state:
	// a beat that did not verify names no registrant this host believes. The
	// key set being unreachable lasts many beats and is one condition, not one
	// per beat — and a beat that DOES verify is the only evidence the key set
	// is reachable again, which is why the recovery is recorded here too.
	if (verdict === "unavailable") {
		observeAuthorityRefusal("unreachable");
		return Response.json(
			{ error: "registration_authority_unavailable" },
			{ status: 503, headers: { "retry-after": "5" } },
		);
	}
	if (verdict === "invalid" || !consumeRegistrationToken(verdict)) {
		observeAuthorityRefusal("refused");
		return Response.json({ error: "unauthorized" }, { status: 401 });
	}
	observeAuthorityReachable();
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
	const answer = await registerBeat(request);
	switch (answer.outcome) {
		case "unverified":
			// The credential never verified, so there is no registrant to
			// attribute this beat to; authorize() already recorded it against
			// the credential check itself.
			break;
		case "registered": {
			const body = (await answer.response.clone().json()) as {
				revision: number;
				status: string;
			};
			observeRegistrationBeat(answer.solution, {
				ok: true,
				revision: body.revision,
				status: body.status,
			});
			break;
		}
		default:
			observeRegistrationBeat(answer.solution, {
				ok: false,
				httpStatus: answer.response.status,
				reason: answer.reason,
				detail: answer.detail,
			});
	}
	return answer.response;
}

/**
 * One beat's answer.
 *
 * Three outcomes rather than a response plus optional fields: a beat whose
 * credential did not verify has no registrant to name, and a refusal always
 * has a reason. Spelling that out is what keeps the caller from needing a
 * default for a reason that is never actually absent.
 */
type BeatAnswer =
	| { outcome: "unverified"; response: Response }
	| { outcome: "registered"; solution: string; response: Response }
	| {
			outcome: "refused";
			solution: string;
			response: Response;
			reason: RegistrationRefusal;
			detail?: string;
	  };

async function registerBeat(request: Request): Promise<BeatAnswer> {
	const claims = await authorize(request);
	if (claims instanceof Response) {
		return { outcome: "unverified", response: claims };
	}
	const solution = claims.solution;
	let body: unknown;
	try {
		body = await request.json();
	} catch {
		return {
			outcome: "refused",
			solution,
			response: Response.json({ error: "invalid_json" }, { status: 400 }),
			reason: "invalid_json",
		};
	}
	const manifest = parseManifest(body);
	if (!manifest) {
		return {
			outcome: "refused",
			solution,
			response: Response.json({ error: "invalid_manifest" }, { status: 422 }),
			reason: "invalid_manifest",
		};
	}
	if (manifest.id !== claims.solution) {
		return {
			outcome: "refused",
			solution,
			response: Response.json(
				{ error: "solution_not_authorized" },
				{ status: 403 },
			),
			reason: "solution_not_authorized",
			detail: `manifest names "${manifest.id}"`,
		};
	}
	// Compatibility is enforced BEFORE the write, so an incompatible remote never
	// reaches a browser and an existing, working registration keeps serving. The
	// reasons ride the response and the server log: a registrant told only "409"
	// would have nothing to act on.
	const verdict = checkRuntimeCompatibility(manifest);
	if (!verdict.compatible) {
		return {
			outcome: "refused",
			solution,
			response: Response.json(
				{ error: "incompatible_runtime", reasons: verdict.reasons },
				{ status: 409 },
			),
			reason: "incompatible_runtime",
			detail: verdict.reasons.join("; "),
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
			outcome: "refused",
			solution,
			response: writeFailure(result),
			reason: `registry ${result.reason}`,
		};
	}
	return {
		outcome: "registered",
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
 * means the id belongs to someone else, `rejected` means the registry will not
 * admit this manifest — change it, do not retry it — and `unavailable` means
 * back off. Answering 200 for any of these would let a solution believe it is
 * serving when it is not — the exact incoherence this registry exists to
 * prevent.
 */
function writeFailure(
	failure: Extract<SolutionWriteResult, { ok: false }>,
): Response {
	switch (failure.reason) {
		case "conflict":
			return Response.json({ error: "revision_conflict" }, { status: 409 });
		case "forbidden":
			return Response.json(
				{ error: "not_registration_owner" },
				{ status: 403 },
			);
		case "rejected":
			// The declared audit event types are the one part of a manifest only
			// the registry can judge (whether the namespace is bound to this
			// solution and free, whether a changed field set only grows), so
			// this is the same class of answer as invalid_manifest: the
			// registrant must change what it sends. `detail` names the rule.
			return Response.json(
				{
					error: "registration_rejected",
					...(failure.detail ? { detail: failure.detail } : {}),
				},
				{ status: 422 },
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
		return writeFailure(result);
	}
	observeRegistrationRemoved(id, result.revision);
	return Response.json({ ok: true, revision: result.revision });
}

// GET is the navigation projection for ONE VIEWER: the id, the nav entry the
// browser polls to render the Solutions menu, and whether it can be opened —
// nothing else. It carries no field a browser does not render. Where a solution's
// code is served from, which backend service fronts it, and its dashboard
// declaration are deployment topology; they are served by the internal detail
// lookup (app/api/internal/solutions) to callers holding the cluster-internal
// token.
//
// It is authenticated (issue #949). It used to be ungated and answered every
// caller the same deployment-wide set, so the menu showed what was *registered*
// rather than what the viewer may *use*. The verified organization and viewer it
// now narrows on exist only behind the gateway's ext_authz — see
// solutions/entitlements.ts for why nothing in this handler may derive them — and
// an unauthenticated caller gets 401 rather than an empty menu, which would be
// indistinguishable from an organization that installed nothing.
export async function GET(request: Request): Promise<Response> {
	const entitlements = await viewerEntitlements(request);
	if (entitlements === "unauthenticated") {
		return Response.json({ error: "unauthenticated" }, { status: 401 });
	}
	if (entitlements === "forbidden") {
		return Response.json({ error: "no_organization" }, { status: 403 });
	}
	if (entitlements === "unavailable") {
		// The authority could not answer. Never an empty list: the menu holds its
		// last known set rather than emptying on an authority blip, exactly as it
		// does for an unreadable registry.
		return Response.json({ error: "authority_unavailable" }, { status: 503 });
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
