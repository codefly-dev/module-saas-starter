import { NextResponse } from "next/server";
import { activeFixtureName, loadActiveFixture } from "@/lib/fixtures/loader";

/**
 * GET /api/fixtures — returns the active fixture data as JSON, sign-in tokens
 * included, to an unauthenticated caller (the dev login page).
 *
 * Served only when BOTH hold: the Codefly environment is local, and a fixture
 * is explicitly selected. Codefly's E2E runner exercises a production Next
 * build, so NODE_ENV cannot be the development-auth boundary; the environment
 * Codefly names is. Anything else — no environment, an unknown one, a deployed
 * one with a fixture selected by mistake — is 404, the same answer as a route
 * that does not exist. The gate lives in the loader so no other reader of the
 * fixture file can skip it.
 */
export function GET() {
	const name = activeFixtureName();
	if (!name) {
		return NextResponse.json({ error: "no active fixture" }, { status: 404 });
	}

	const fixture = loadActiveFixture();
	if (!fixture) {
		return NextResponse.json(
			{ error: `fixture "${name}" not found` },
			{ status: 404 },
		);
	}

	return NextResponse.json({ name, ...fixture });
}
