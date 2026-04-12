import { createConnectTransport } from "@connectrpc/connect-web";
import type { Interceptor } from "@connectrpc/connect";
import { getToken } from "./token-store";

/**
 * Connect transport for the API backend, going through the auth-sidecar
 * gateway. All Connect RPC calls go through this single entry point.
 *
 * The auth interceptor automatically injects the Bearer token from the
 * token store on every request. The AuthProvider keeps the store in sync.
 */
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
