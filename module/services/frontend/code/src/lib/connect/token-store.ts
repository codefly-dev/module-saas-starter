/**
 * Module-level token store that bridges React auth context and the
 * Connect transport interceptor. The AuthProvider writes the token here
 * on login/refresh; the interceptor reads it on every RPC call.
 */

let currentToken: string | null = null;

export function setToken(token: string | null) {
  currentToken = token;
}

export function getToken(): string | null {
  return currentToken;
}
