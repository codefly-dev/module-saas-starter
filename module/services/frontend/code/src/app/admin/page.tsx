"use client";

/**
 * Admin overview — the landing page for the admin surface. Guarded by
 * the (admin) layout so only admin-tier users reach this page.
 *
 * Organized as tiles grouped into "Access" (everyday admin tasks) and
 * "Platform" (super-admin-only). Platform tiles use <RoleGate require=
 * "super_admin"> so org admins don't see features they can't access.
 */

import Link from "next/link";

import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { RoleGate } from "@/components/auth/role-gate";
import {
  Users,
  Building2,
  UsersRound,
  Shield,
  KeyRound,
  ScrollText,
  MailCheck,
  Flag,
  ShieldCheck,
  Activity,
} from "lucide-react";

interface Tile {
  href: string;
  title: string;
  description: string;
  icon: React.ComponentType<{ className?: string }>;
}

const accessTiles: Tile[] = [
  { href: "/admin/users", title: "Users", description: "Search users, suspend, impersonate", icon: Users },
  { href: "/admin/organizations", title: "Organizations", description: "Organizations, members, settings", icon: Building2 },
  { href: "/admin/teams", title: "Teams", description: "Team membership + roles", icon: UsersRound },
  { href: "/admin/roles", title: "Roles", description: "Built-in and custom roles", icon: Shield },
  { href: "/admin/api-keys", title: "API keys", description: "Issue and revoke keys", icon: KeyRound },
  { href: "/admin/audit-log", title: "Audit log", description: "Every mutation, searchable", icon: ScrollText },
  { href: "/admin/invitations", title: "Invitations", description: "Pending invites, resends", icon: MailCheck },
];

const platformTiles: Tile[] = [
  { href: "/admin/platform/admins", title: "Platform admins", description: "Super-admin role holders", icon: ShieldCheck },
  { href: "/admin/platform/feature-flags", title: "Feature flags", description: "Rollout controls across orgs", icon: Flag },
  { href: "/admin/sessions", title: "Sessions", description: "Active sessions, force-logout", icon: Activity },
];

function TileGrid({ tiles }: { tiles: Tile[] }) {
  return (
    <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
      {tiles.map((t) => {
        const Icon = t.icon;
        return (
          <Link key={t.href} href={t.href}>
            <Card className="hover:border-primary/50 transition-colors cursor-pointer h-full">
              <CardHeader className="flex flex-row items-center gap-3 pb-2">
                <Icon className="h-5 w-5 text-muted-foreground" />
                <CardTitle className="text-sm font-medium">{t.title}</CardTitle>
              </CardHeader>
              <CardContent>
                <CardDescription>{t.description}</CardDescription>
              </CardContent>
            </Card>
          </Link>
        );
      })}
    </div>
  );
}

export default function AdminOverviewPage() {
  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-3xl font-bold tracking-tight">Admin</h1>
        <p className="text-muted-foreground mt-1">
          Manage users, organizations, and platform settings.
        </p>
      </div>

      <section className="space-y-3">
        <h2 className="text-sm font-semibold text-muted-foreground uppercase tracking-wider">
          Access
        </h2>
        <TileGrid tiles={accessTiles} />
      </section>

      <RoleGate require="super_admin">
        <section className="space-y-3">
          <h2 className="text-sm font-semibold text-muted-foreground uppercase tracking-wider">
            Platform
          </h2>
          <TileGrid tiles={platformTiles} />
        </section>
      </RoleGate>
    </div>
  );
}
