import type { APIKeyEnvironmentLabel } from "./types";

export function formatEnvironment(env: number): APIKeyEnvironmentLabel {
  switch (env) {
    case 1:
      return "live";
    case 2:
      return "test";
    default:
      return "unknown";
  }
}

export function maskKeyPrefix(prefix: string): string {
  if (!prefix) return "****";
  return `${prefix}${"*".repeat(24)}`;
}
