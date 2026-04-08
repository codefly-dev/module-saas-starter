"use client";

import createClient from "openapi-fetch";
import type { paths } from "@/gen/api";

const API_BASE = process.env.NEXT_PUBLIC_BACKEND_URL || "http://localhost:8080";

let clientInstance: ReturnType<typeof createClient<paths>> | null = null;

export function useAPIClient() {
  if (!clientInstance) {
    clientInstance = createClient<paths>({ baseUrl: API_BASE });
  }
  return clientInstance;
}
