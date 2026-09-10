import { NextResponse } from "next/server";
import { resolveAccountsBindings } from "../../../../server/accounts-bindings.mjs";

// Readiness, not liveness: the probe resolves the single product API path, so a
// composition that reaches no auth-gateway/rest never takes traffic. The
// resolver reads the SDK's already-injected environment, so this stays a local
// check with no outbound call. force-dynamic because a prerendered answer would
// report the build's configuration rather than the running server's.
export const dynamic = "force-dynamic";

export function GET() {
	try {
		resolveAccountsBindings();
	} catch (error) {
		return NextResponse.json(
			{
				status: "misconfigured",
				reason: error instanceof Error ? error.message : String(error),
			},
			{ status: 503 },
		);
	}
	return NextResponse.json({ status: "ok" });
}
