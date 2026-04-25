"use client";

import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { RoleGate } from "@/components/auth/role-gate";
import { Shield, Bell, FileText, ShieldCheck, ArrowUpRight, Activity } from "lucide-react";
import Link from "next/link";
import { Sparkline } from "@/components/sparkline";
import { useAuth } from "@/lib/auth";

/**
 * Main dashboard — the hosting app's home. Saas-starter is a module,
 * not the main app: everything the module adds (users, orgs, teams,
 * billing, audit) lives under /admin and is only reachable if the
 * user is admin-tier.
 *
 * Layout:
 *   - Hero header with the user's email + a subtle gradient strip
 *     for visual personality.
 *   - Three personal-account utility cards (Security / Notifications
 *     / Data & Privacy) — visible to every authenticated user.
 *   - Admin tile gated by <RoleGate require="admin">. Shown with a
 *     mini sparkline (placeholder until we wire real audit-volume
 *     data) so the surface looks alive instead of static.
 */
export default function DashboardPage() {
  const { user, platformRole, orgRole } = useAuth();
  const greetingName =
    user?.email?.split("@")[0]?.replace(/[._-]/g, " ") ?? "there";

  return (
    <div className="space-y-10">
      {/* ── HERO ─────────────────────────────────────────────── */}
      <header className="relative overflow-hidden rounded-2xl border bg-gradient-to-br from-violet-500/10 via-indigo-500/5 to-fuchsia-500/10 p-8 dark:from-violet-500/15 dark:via-indigo-500/10 dark:to-fuchsia-500/15">
        <div
          aria-hidden
          className="pointer-events-none absolute inset-0 opacity-[0.05]"
          style={{
            backgroundImage:
              "linear-gradient(to right, currentColor 1px, transparent 1px), linear-gradient(to bottom, currentColor 1px, transparent 1px)",
            backgroundSize: "24px 24px",
          }}
        />
        <div className="relative flex flex-col gap-2">
          <h1 className="text-3xl font-bold tracking-tight capitalize">
            Welcome back, {greetingName}
          </h1>
          <p className="text-muted-foreground">
            Here&apos;s an overview of your account.{" "}
            <span className="hidden sm:inline">
              Press <kbd className="px-1.5 py-0.5 rounded border bg-background text-[11px] font-mono">⌘K</kbd> to jump anywhere.
            </span>
          </p>
          {(platformRole || orgRole) && (
            <div className="mt-3 flex items-center gap-2 text-xs">
              {platformRole && (
                <span className="inline-flex items-center gap-1 rounded-full bg-violet-500/10 px-2.5 py-1 font-medium text-violet-700 dark:text-violet-300 ring-1 ring-violet-500/20">
                  <Shield className="h-3 w-3" />
                  {platformRole}
                </span>
              )}
              {orgRole && (
                <span className="inline-flex items-center gap-1 rounded-full bg-indigo-500/10 px-2.5 py-1 font-medium text-indigo-700 dark:text-indigo-300 ring-1 ring-indigo-500/20">
                  org / {orgRole}
                </span>
              )}
            </div>
          )}
        </div>
      </header>

      {/* ── PERSONAL CARDS ─────────────────────────────────── */}
      <div className="grid gap-4 md:grid-cols-3">
        <DashboardCard
          href="/settings/mfa"
          icon={Shield}
          title="Security"
          description="Manage MFA and security settings"
          accent="from-emerald-500/10 to-teal-500/10"
        />
        <DashboardCard
          href="/settings/notifications"
          icon={Bell}
          title="Notifications"
          description="Configure notification preferences"
          accent="from-amber-500/10 to-orange-500/10"
        />
        <DashboardCard
          href="/settings/data"
          icon={FileText}
          title="Data & Privacy"
          description="Export data or manage your account"
          accent="from-sky-500/10 to-cyan-500/10"
        />
      </div>

      {/* ── ADMIN ─────────────────────────────────────────────
          Gated so only admin+ sessions see it. The mini sparkline
          is a placeholder — wire to ListAuditEvents once the
          aggregate-by-day query lands.
          ─────────────────────────────────────────────────────── */}
      <RoleGate require="admin">
        <div>
          <h2 className="text-lg font-semibold tracking-tight mb-3">
            Administration
          </h2>
          <div className="grid gap-4 md:grid-cols-2">
            <Link href="/admin" className="group">
              <Card className="relative overflow-hidden hover:border-primary/50 transition-all hover:shadow-md hover:-translate-y-0.5 cursor-pointer">
                <div
                  aria-hidden
                  className="absolute inset-0 bg-gradient-to-br from-amber-500/5 to-orange-500/5 opacity-0 group-hover:opacity-100 transition-opacity"
                />
                <CardHeader className="flex flex-row items-center gap-3 pb-2">
                  <div className="h-9 w-9 rounded-lg bg-amber-500/10 flex items-center justify-center ring-1 ring-amber-500/20">
                    <ShieldCheck className="h-5 w-5 text-amber-600 dark:text-amber-400" />
                  </div>
                  <div className="flex-1">
                    <CardTitle className="text-sm font-medium">Admin panel</CardTitle>
                  </div>
                  <ArrowUpRight className="h-4 w-4 text-muted-foreground opacity-0 group-hover:opacity-100 transition-opacity" />
                </CardHeader>
                <CardContent className="relative space-y-3">
                  <CardDescription>
                    Users, organizations, teams, permissions, API keys, audit log
                  </CardDescription>
                  <div className="flex items-end justify-between pt-2">
                    <div>
                      <div className="text-xs text-muted-foreground flex items-center gap-1.5">
                        <Activity className="h-3 w-3" />
                        Audit activity (7d)
                      </div>
                    </div>
                    <Sparkline
                      points={[3, 5, 4, 8, 6, 12, 9]}
                      className="text-amber-500/70"
                    />
                  </div>
                </CardContent>
              </Card>
            </Link>
          </div>
        </div>
      </RoleGate>
    </div>
  );
}

interface DashboardCardProps {
  href: string;
  icon: React.ComponentType<{ className?: string }>;
  title: string;
  description: string;
  accent: string;
}

function DashboardCard({ href, icon: Icon, title, description, accent }: DashboardCardProps) {
  return (
    <Link href={href} className="group">
      <Card className="relative overflow-hidden hover:border-primary/50 transition-all hover:shadow-md hover:-translate-y-0.5 cursor-pointer h-full">
        <div
          aria-hidden
          className={`absolute inset-0 bg-gradient-to-br ${accent} opacity-0 group-hover:opacity-100 transition-opacity`}
        />
        <CardHeader className="flex flex-row items-center gap-3 pb-2">
          <div className="h-9 w-9 rounded-lg bg-foreground/[0.04] flex items-center justify-center ring-1 ring-border">
            <Icon className="h-4 w-4 text-muted-foreground" />
          </div>
          <CardTitle className="text-sm font-medium flex-1">{title}</CardTitle>
          <ArrowUpRight className="h-4 w-4 text-muted-foreground opacity-0 group-hover:opacity-100 transition-opacity" />
        </CardHeader>
        <CardContent className="relative">
          <CardDescription>{description}</CardDescription>
        </CardContent>
      </Card>
    </Link>
  );
}
