import { createConnectTransport } from "@connectrpc/connect-web";

/**
 * Connect transport for the API backend, going through the auth-sidecar
 * gateway. All Connect RPC calls go through this single entry point.
 *
 * In production, this is the auth-sidecar's public HTTP endpoint.
 * In dev, codefly injects NEXT_PUBLIC_API_CONNECT pointing at the gateway.
 *
 * Connect routes use paths like /customers.UserService/GetSelf which the
 * auth-sidecar proxies to the API's Connect endpoint.
 */
const GATEWAY_URL =
  process.env.NEXT_PUBLIC_API_CONNECT || "http://localhost:8080";

export const apiTransport = createConnectTransport({
  baseUrl: GATEWAY_URL,
});
