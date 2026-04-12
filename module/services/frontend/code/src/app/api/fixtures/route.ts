import { NextResponse } from "next/server";
import { loadActiveFixture, activeFixtureName } from "@/lib/fixtures/loader";

/**
 * GET /api/fixtures — returns the active fixture data as JSON.
 * Only available when CODEFLY__FIXTURE is set AND not in production.
 * Returns 404 in production or when no fixture is active.
 */
export function GET() {
  if (process.env.NODE_ENV === "production") {
    return NextResponse.json({ error: "not available" }, { status: 404 });
  }

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
