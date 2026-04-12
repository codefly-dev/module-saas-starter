"use client";

import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import type { Interceptor } from "@connectrpc/connect";

// ── Token store (bridges React auth context ↔ Connect transport) ──

let currentToken: string | null = null;

export function setToken(token: string | null) {
  currentToken = token;
}

export function getToken(): string | null {
  return currentToken;
}

// ── Transport with auth interceptor ──

const GATEWAY_URL =
  process.env.NEXT_PUBLIC_API_CONNECT || "http://localhost:8080";

const authInterceptor: Interceptor = (next) => async (req) => {
  const token = getToken();
  if (token) {
    req.header.set("Authorization", `Bearer ${token}`);
  }
  return next(req);
};

export const apiTransport = createConnectTransport({
  baseUrl: GATEWAY_URL,
  interceptors: [authInterceptor],
});

// ── Service client factory ──

export function createServiceClient<T extends Parameters<typeof createClient>[0]>(service: T) {
  return createClient(service, apiTransport);
}
