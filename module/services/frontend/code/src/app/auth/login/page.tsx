"use client";

// Sign-in landing page.
//
// Shows a button per configured OAuth provider (WorkOS, Google, Auth0...)
// based on NEXT_PUBLIC_* env vars. Clicking a button redirects the
// browser to the provider's hosted login; the callback at
// /auth/callback completes the flow.
//
// When a fixture is active (CODEFLY__FIXTURE set), the page fetches
// the fixture users from /api/fixtures and shows one-click login
// buttons for each seeded identity.

import { useEffect, useMemo, useState } from "react";
import { useAuth, availableProviders } from "@/lib/auth";
import type { FixtureUser } from "@/lib/fixtures/types";

interface FixtureResponse {
  name: string;
  users: FixtureUser[];
}

export default function LoginPage() {
  const { signInWith, login } = useAuth();
  const providers = useMemo(() => availableProviders(), []);
  const [error, setError] = useState<string | null>(null);
  const [fixtureUsers, setFixtureUsers] = useState<FixtureUser[]>([]);
  const [fixtureName, setFixtureName] = useState<string | null>(null);

  // Dev-mode fallback form fields (manual entry).
  const [devIdentity, setDevIdentity] = useState("");
  const [devEmail, setDevEmail] = useState("");
  const devMode = providers.length === 0;

  // Fetch fixture users when in dev mode.
  useEffect(() => {
    if (!devMode) return;
    fetch("/api/fixtures")
      .then((res) => (res.ok ? (res.json() as Promise<FixtureResponse>) : null))
      .then((data) => {
        if (data?.users) {
          setFixtureUsers(data.users);
          setFixtureName(data.name);
        }
      })
      .catch((err) => {
        console.error("Failed to load fixture users:", err);
      });
  }, [devMode]);

  async function handleFixtureLogin(user: FixtureUser) {
    setError(null);
    try {
      await login(user.provider, user.provider_id, user.email);
      window.location.href = "/";
    } catch (err) {
      setError(err instanceof Error ? err.message : "Login failed");
    }
  }

  async function handleDevLogin(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    try {
      await login("dev", devIdentity, devEmail);
      window.location.href = "/";
    } catch (err) {
      setError(err instanceof Error ? err.message : "Login failed");
    }
  }

  return (
    <div
      style={{
        maxWidth: "400px",
        margin: "4rem auto",
        padding: "2rem",
        fontFamily: "system-ui",
      }}
    >
      <h1>Sign in</h1>

      {error && (
        <p style={{ color: "red", marginBottom: "1rem" }}>{error}</p>
      )}

      {providers.length > 0 && (
        <div style={{ display: "flex", flexDirection: "column", gap: "0.75rem" }}>
          {providers.map((p) => (
            <button
              key={p.id}
              type="button"
              onClick={() => {
                try {
                  signInWith(p.id);
                } catch (err) {
                  setError(err instanceof Error ? err.message : "Redirect failed");
                }
              }}
              style={{
                padding: "0.75rem 1rem",
                border: "1px solid #ccc",
                borderRadius: "0.5rem",
                cursor: "pointer",
                background: "white",
                fontSize: "1rem",
              }}
            >
              Continue with {p.displayName}
            </button>
          ))}
        </div>
      )}

      {devMode && fixtureUsers.length > 0 && (
        <div style={{ marginTop: "1rem" }}>
          <h2>Fixture users{fixtureName ? ` (${fixtureName})` : ""}</h2>
          <p style={{ fontSize: "0.875rem", color: "#666", marginBottom: "0.75rem" }}>
            Click a user to sign in instantly with their seeded identity.
          </p>
          <div style={{ display: "flex", flexDirection: "column", gap: "0.5rem" }}>
            {fixtureUsers.map((u) => (
              <button
                key={u.provider_id}
                type="button"
                onClick={() => handleFixtureLogin(u)}
                style={{
                  padding: "0.75rem 1rem",
                  border: "1px solid #ccc",
                  borderRadius: "0.5rem",
                  cursor: "pointer",
                  background: "white",
                  fontSize: "0.9rem",
                  textAlign: "left",
                }}
              >
                <strong>{u.name}</strong>
                <br />
                <span style={{ fontSize: "0.8rem", color: "#666" }}>
                  {u.email} &middot; {u.role}
                </span>
              </button>
            ))}
          </div>
        </div>
      )}

      {devMode && (
        <div style={{ marginTop: "2rem" }}>
          <h2>Manual dev login</h2>
          <p style={{ fontSize: "0.875rem", color: "#666" }}>
            Enter a fixture identity directly if you don&apos;t see the user above.
          </p>
          <form onSubmit={handleDevLogin} style={{ display: "flex", flexDirection: "column", gap: "0.5rem" }}>
            <label>
              Provider id (e.g. dev-admin)
              <input
                type="text"
                value={devIdentity}
                onChange={(e) => setDevIdentity(e.target.value)}
                required
                style={{ width: "100%", padding: "0.5rem" }}
              />
            </label>
            <label>
              Email
              <input
                type="email"
                value={devEmail}
                onChange={(e) => setDevEmail(e.target.value)}
                required
                style={{ width: "100%", padding: "0.5rem" }}
              />
            </label>
            <button
              type="submit"
              style={{
                padding: "0.75rem",
                background: "#0066cc",
                color: "white",
                border: "none",
                borderRadius: "0.5rem",
                cursor: "pointer",
              }}
            >
              Sign in (dev)
            </button>
          </form>
        </div>
      )}
    </div>
  );
}
