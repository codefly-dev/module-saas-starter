"use client";

import { useEffect, useMemo, useState } from "react";
import { useAuth, availableProviders } from "@/lib/auth";
import { useSendMagicLink } from "@/features/auth/service/mutations";
import type { FixtureUser } from "@/lib/fixtures/types";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
  Button,
  Input,
  Label,
  Separator,
} from "@/shared/ui";
import { LogIn, Mail, User, AlertCircle, CheckCircle2, Loader2 } from "lucide-react";

interface FixtureResponse {
  name: string;
  users: FixtureUser[];
}

// Provider icon mapping
function ProviderIcon({ id }: { id: string }) {
  // Simple SVG icons for common providers
  switch (id) {
    case "google":
      return (
        <svg className="h-5 w-5" viewBox="0 0 24 24">
          <path
            fill="currentColor"
            d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92a5.06 5.06 0 0 1-2.2 3.32v2.77h3.57c2.08-1.92 3.28-4.74 3.28-8.1z"
          />
          <path
            fill="currentColor"
            d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.77c-.98.66-2.23 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84C3.99 20.53 7.7 23 12 23z"
          />
          <path
            fill="currentColor"
            d="M5.84 14.09c-.22-.66-.35-1.36-.35-2.09s.13-1.43.35-2.09V7.07H2.18C1.43 8.55 1 10.22 1 12s.43 3.45 1.18 4.93l2.85-2.22.81-.62z"
          />
          <path
            fill="currentColor"
            d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.07l3.66 2.84c.87-2.6 3.3-4.53 6.16-4.53z"
          />
        </svg>
      );
    default:
      return <LogIn className="h-5 w-5" />;
  }
}

export function LoginPage() {
  const { signInWith, login } = useAuth();
  const providers = useMemo(() => availableProviders(), []);
  const [error, setError] = useState<string | null>(null);
  const [fixtureUsers, setFixtureUsers] = useState<FixtureUser[]>([]);
  const [fixtureName, setFixtureName] = useState<string | null>(null);

  // Magic link state
  const [magicEmail, setMagicEmail] = useState("");
  const [magicSent, setMagicSent] = useState(false);
  const sendMagicLink = useSendMagicLink();

  // Dev fallback fields
  const [devIdentity, setDevIdentity] = useState("");
  const [devEmail, setDevEmail] = useState("");
  const devMode = providers.length === 0;

  // Fetch fixture users in dev mode
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

  async function handleMagicLink(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    try {
      await sendMagicLink.mutateAsync({ email: magicEmail });
      setMagicSent(true);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to send magic link");
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
    <div className="flex min-h-screen items-center justify-center bg-background px-4">
      <Card className="w-full max-w-md">
        <CardHeader className="text-center">
          <CardTitle className="text-2xl font-bold">Welcome back</CardTitle>
          <CardDescription>Sign in to your account to continue</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          {error && (
            <div className="flex items-center gap-2 rounded-md border border-destructive/50 bg-destructive/10 p-3 text-sm text-destructive">
              <AlertCircle className="h-4 w-4 shrink-0" />
              {error}
            </div>
          )}

          {/* OAuth provider buttons */}
          {providers.length > 0 && (
            <div className="space-y-3">
              {providers.map((p) => (
                <Button
                  key={p.id}
                  variant="outline"
                  className="w-full justify-center gap-3 h-11"
                  onClick={() => {
                    try {
                      signInWith(p.id);
                    } catch (err) {
                      setError(
                        err instanceof Error ? err.message : "Redirect failed",
                      );
                    }
                  }}
                >
                  <ProviderIcon id={p.id} />
                  Continue with {p.displayName}
                </Button>
              ))}
            </div>
          )}

          {/* Magic link sign-in */}
          {providers.length > 0 && <Separator />}
          {magicSent ? (
            <div className="flex items-center gap-2 rounded-md border border-green-500/50 bg-green-500/10 p-3 text-sm text-green-700 dark:text-green-400">
              <CheckCircle2 className="h-4 w-4 shrink-0" />
              Check your email for a sign-in link
            </div>
          ) : (
            <form onSubmit={handleMagicLink} className="space-y-3">
              <p className="text-sm font-medium text-center text-muted-foreground">
                Sign in with email
              </p>
              <div className="space-y-2">
                <Label htmlFor="magic-email">Email address</Label>
                <Input
                  id="magic-email"
                  type="email"
                  placeholder="you@example.com"
                  value={magicEmail}
                  onChange={(e) => setMagicEmail(e.target.value)}
                  required
                />
              </div>
              <Button
                type="submit"
                variant="outline"
                className="w-full gap-2"
                disabled={sendMagicLink.isPending}
              >
                {sendMagicLink.isPending ? (
                  <Loader2 className="h-4 w-4 animate-spin" />
                ) : (
                  <Mail className="h-4 w-4" />
                )}
                Send magic link
              </Button>
            </form>
          )}

          {/* Fixture users (dev mode) */}
          {devMode && fixtureUsers.length > 0 && (
            <>
              <Separator />
              <div className="space-y-3">
                <p className="text-sm font-medium text-center text-muted-foreground">
                  Fixture users{fixtureName ? ` (${fixtureName})` : ""}
                </p>
                {fixtureUsers.map((u) => (
                  <Button
                    key={u.provider_id}
                    variant="outline"
                    className="w-full justify-start gap-3 h-auto py-3"
                    onClick={() => handleFixtureLogin(u)}
                  >
                    <User className="h-4 w-4 shrink-0 text-muted-foreground" />
                    <div className="flex flex-col items-start">
                      <span className="font-medium">{u.name}</span>
                      <span className="text-xs text-muted-foreground">
                        {u.email} &middot; {u.role}
                      </span>
                    </div>
                  </Button>
                ))}
              </div>
            </>
          )}

          {/* Manual dev login */}
          {devMode && (
            <>
              <Separator />
              <form onSubmit={handleDevLogin} className="space-y-3">
                <p className="text-sm font-medium text-center text-muted-foreground">
                  Manual dev login
                </p>
                <div className="space-y-2">
                  <Label htmlFor="dev-id">Provider ID</Label>
                  <Input
                    id="dev-id"
                    placeholder="e.g. dev-admin"
                    value={devIdentity}
                    onChange={(e) => setDevIdentity(e.target.value)}
                    required
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="dev-email">Email</Label>
                  <Input
                    id="dev-email"
                    type="email"
                    placeholder="admin@example.com"
                    value={devEmail}
                    onChange={(e) => setDevEmail(e.target.value)}
                    required
                  />
                </div>
                <Button type="submit" className="w-full gap-2">
                  <Mail className="h-4 w-4" />
                  Sign in (dev)
                </Button>
              </form>
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
