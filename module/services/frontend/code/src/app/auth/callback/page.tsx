"use client";

// OAuth callback landing page.
//
// The flow:
//  1. User clicked "Sign in with <provider>" on /auth/login.
//  2. Browser redirected to provider's hosted login.
//  3. Provider redirected back here with ?code=... &state=...
//  4. This page calls completeOAuth, which verifies state (CSRF),
//     POSTs the code to the backend /v1/auth/authenticate, stores
//     the returned JWT + refresh token, and redirects to the
//     original page (or "/").
//
// Renders a minimal "Signing you in…" placeholder while that runs.
// On error, shows the error and a link back to /auth/login.

import { useEffect, useState, Suspense } from "react";
import { useSearchParams } from "next/navigation";
import { useAuth } from "@/lib/auth";

function CallbackHandler() {
  const { completeOAuth } = useAuth();
  const params = useSearchParams();
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const code = params.get("code");
    const state = params.get("state");
    const providerError = params.get("error");
    const providerErrorDescription = params.get("error_description");

    if (providerError) {
      setError(
        `${providerError}${providerErrorDescription ? `: ${providerErrorDescription}` : ""}`,
      );
      return;
    }
    if (!code || !state) {
      setError("Missing code or state in callback URL");
      return;
    }

    completeOAuth(code, state).catch((e) => {
      setError(e instanceof Error ? e.message : "Sign-in failed");
    });
  }, [params, completeOAuth]);

  if (error) {
    return (
      <div style={{ padding: "2rem", fontFamily: "system-ui" }}>
        <h1>Sign-in failed</h1>
        <p style={{ color: "red" }}>{error}</p>
        <p>
          <a href="/auth/login">Try again</a>
        </p>
      </div>
    );
  }

  return (
    <div style={{ padding: "2rem", fontFamily: "system-ui" }}>
      <h1>Signing you in…</h1>
      <p>Please wait while we complete the sign-in process.</p>
    </div>
  );
}

export default function AuthCallbackPage() {
  return (
    <Suspense
      fallback={
        <div style={{ padding: "2rem", fontFamily: "system-ui" }}>
          <h1>Signing you in…</h1>
        </div>
      }
    >
      <CallbackHandler />
    </Suspense>
  );
}
