// One handler for versioned assets and the ordinary component proxy.
export const dynamic = "force-dynamic";
export const runtime = "nodejs";
export { GET, HEAD } from "@/app/api/solutions/[id]/proxy/[...path]/route";
