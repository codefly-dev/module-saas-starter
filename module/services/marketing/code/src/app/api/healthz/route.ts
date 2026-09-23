// The pinned Next.js deployment agent probes this dependency-free endpoint.
// Keep the existing public health URL compatible with the same handler.
export { GET } from "../health/route";

export const dynamic = "force-dynamic";
