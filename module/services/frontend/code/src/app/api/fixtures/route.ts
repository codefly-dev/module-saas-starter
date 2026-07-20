import { NextResponse } from "next/server";
import { activeFixtureName, loadActiveFixture } from "@/lib/fixtures/loader";

/**
 * GET /api/fixtures — returns the active fixture data as JSON.
 * Only available when CODEFLY__FIXTURE is explicitly set. Codefly's E2E
 * runner exercises a production Next build, so NODE_ENV cannot be used as
 * the development-auth boundary; the explicit fixture selection is that
 * boundary. Returns 404 when no fixture is active.
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
