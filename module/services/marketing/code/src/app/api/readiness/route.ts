import { NextResponse } from "next/server";
import { loadPublicPlans } from "@/lib/catalog";

export const dynamic = "force-dynamic";

export async function GET() {
  const catalog = await loadPublicPlans();
  return NextResponse.json(
    {
      status: "ready",
      service: "marketing",
      dependencies: {
        publicCatalog:
          catalog.kind === "available" ? "available" : catalog.kind,
      },
    },
    {
      headers: {
        "Cache-Control": "no-store",
      },
    },
  );
}
