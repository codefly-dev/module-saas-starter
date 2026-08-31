import { NextResponse } from "next/server";

// Liveness/readiness endpoint. Committed (not generated) so every build serves it
// regardless of scaffolding — the k8s deployment probes /api/healthz. Mirrors the
// marketing service's /api/health route.
export const dynamic = "force-dynamic";

export function GET() {
  return NextResponse.json(
    { status: "ok", service: "frontend" },
    { headers: { "Cache-Control": "no-store" } },
  );
}
