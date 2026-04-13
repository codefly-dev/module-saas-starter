"use client";

import { useEffect, useMemo, useState } from "react";
import { useAuth, availableProviders } from "@/lib/auth";
import type { FixtureUser } from "@/lib/fixtures/types";
import { LogIn, Mail, User, AlertCircle, CheckCircle2, Loader2 } from "lucide-react";

interface FixtureResponse {
  name: string;
  users: FixtureUser[];
}

export function LoginPage() {
  const { signInWith, login } = useAuth();
  const providers = useMemo(() => availableProviders(), []);
  const [error, setError] = useState<string | null>(null);
  const [fixtureUsers, setFixtureUsers] = useState<FixtureUser[]>([]);
  const [loading, setLoading] = useState<string | null>(null);

  const devMode = providers.length === 0;

  useEffect(() => {
    if (!devMode) return;
    fetch("/api/fixtures")
      .then((res) => (res.ok ? (res.json() as Promise<FixtureResponse>) : null))
      .then((data) => {
        if (data?.users) setFixtureUsers(data.users);
      })
      .catch(() => {});
  }, [devMode]);

  async function handleFixtureLogin(user: FixtureUser) {
    setError(null);
    setLoading(user.provider_id);
    try {
      await login(user.provider, user.provider_id, user.email);
      window.location.href = "/";
    } catch (err) {
      setError(err instanceof Error ? err.message : "Login failed");
      setLoading(null);
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-gradient-to-br from-gray-50 to-gray-100 dark:from-gray-950 dark:to-gray-900 px-4">
      <div className="w-full max-w-sm">
        {/* Logo */}
        <div className="flex justify-center mb-8">
          <div className="flex items-center gap-3">
            <div className="h-10 w-10 rounded-xl bg-primary flex items-center justify-center shadow-lg shadow-primary/25">
              <span className="text-lg font-bold text-primary-foreground">S</span>
            </div>
            <span className="text-xl font-semibold tracking-tight">SaaS Starter</span>
          </div>
        </div>

        {/* Card */}
        <div className="rounded-2xl border bg-card text-card-foreground shadow-xl shadow-black/5 dark:shadow-black/20">
          <div className="p-8 space-y-6">
            <div className="text-center space-y-1.5">
              <h1 className="text-2xl font-semibold tracking-tight">Sign in</h1>
              <p className="text-sm text-muted-foreground">
                Choose a method to continue
              </p>
            </div>

            {error && (
              <div className="flex items-center gap-2 rounded-lg bg-destructive/10 border border-destructive/20 p-3 text-sm text-destructive">
                <AlertCircle className="h-4 w-4 shrink-0" />
                <span>{error}</span>
              </div>
            )}

            {/* OAuth providers */}
            {providers.length > 0 && (
              <div className="space-y-2.5">
                {providers.map((p) => (
                  <button
                    key={p.id}
                    onClick={() => { try { signInWith(p.id); } catch {} }}
                    className="w-full flex items-center justify-center gap-2.5 h-11 rounded-lg border bg-background hover:bg-accent/50 text-sm font-medium transition-colors"
                  >
                    <LogIn className="h-4 w-4" />
                    Continue with {p.displayName}
                  </button>
                ))}
              </div>
            )}

            {/* Fixture users — dev mode */}
            {devMode && fixtureUsers.length > 0 && (
              <div className="space-y-2.5">
                <p className="text-xs font-medium text-muted-foreground text-center uppercase tracking-wider">
                  Dev — select a user
                </p>
                {fixtureUsers.map((u) => (
                  <button
                    key={u.provider_id}
                    onClick={() => handleFixtureLogin(u)}
                    disabled={loading === u.provider_id}
                    className="w-full flex items-center gap-3 h-14 px-4 rounded-lg border bg-background hover:bg-accent/50 text-left transition-colors disabled:opacity-50"
                  >
                    {loading === u.provider_id ? (
                      <Loader2 className="h-5 w-5 animate-spin text-muted-foreground shrink-0" />
                    ) : (
                      <div className="h-8 w-8 rounded-full bg-gradient-to-br from-violet-500 to-fuchsia-500 flex items-center justify-center shrink-0">
                        <span className="text-xs font-bold text-white">
                          {u.name.split(" ").map(n => n[0]).join("")}
                        </span>
                      </div>
                    )}
                    <div className="min-w-0">
                      <div className="text-sm font-medium truncate">{u.name}</div>
                      <div className="text-xs text-muted-foreground truncate">
                        {u.email}
                        <span className="ml-1.5 inline-flex items-center rounded-full bg-accent px-1.5 py-0.5 text-[10px] font-medium">
                          {u.role}
                        </span>
                      </div>
                    </div>
                  </button>
                ))}
              </div>
            )}
          </div>
        </div>

        {/* Footer */}
        <p className="text-center text-xs text-muted-foreground mt-6">
          By continuing, you agree to our Terms of Service and Privacy Policy.
        </p>
      </div>
    </div>
  );
}
