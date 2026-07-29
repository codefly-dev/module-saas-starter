import { NextResponse } from "next/server";
import { marketingEnvironment } from "@/config/environment";

export const dynamic = "force-dynamic";

export function GET() {
  const environment = marketingEnvironment();
  return NextResponse.json(
    {
      status: "ok",
      service: "marketing",
      release: environment.release,
      enabled: environment.enabled,
    },
    {
      headers: {
        "Cache-Control": "no-store",
      },
    },
  );
}
