"use client";

/**
 * CommandPalette — global cmd+K / ctrl+K dialog. Routes to admin
 * destinations and exposes a searchable user list (super_admin only)
 * via Connect-ES. Inserted once at the (dashboard) layout root so any
 * authenticated page picks it up.
 *
 * Design choices:
 *   - cmdk + shadcn primitives we already have. Zero new deps.
 *   - Static command list = navigation. Dynamic = users (debounced
 *     async query). Future: orgs, audit events, settings — same shape.
 *   - Visibility gating mirrors the sidebar's RoleGate. We don't show
 *     "Platform Users" search to non-super-admins; they'd hit an
 *     authz error anyway, but better not to dangle the link.
 */

import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import {
  Dialog,
  DialogContent,
  DialogTitle,
} from "@/components/ui/dialog";
import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { useAuth } from "@/lib/auth";
import { isAdmin, isSuperAdmin } from "@/lib/permissions";
import { Users, Building2, Shield, FileText, Key, Bell, LogOut, Webhook, ScrollText, ListChecks, Activity, CreditCard } from "lucide-react";
import { useUsersSearch } from "@/lib/hooks/use-users-search";

interface NavCommand {
  label: string;
  href: string;
  icon: React.ComponentType<{ className?: string }>;
  // requireRole: hide the entry from users who lack the role. Server
  // is still authoritative — this is just UX (don't dangle dead links).
  requireRole?: "admin" | "super_admin";
}

const NAV_COMMANDS: NavCommand[] = [
  { label: "Users", href: "/admin/users", icon: Users, requireRole: "admin" },
  { label: "Organizations", href: "/admin/organizations", icon: Building2, requireRole: "admin" },
  { label: "Teams", href: "/admin/teams", icon: Users, requireRole: "admin" },
  { label: "Roles", href: "/admin/roles", icon: Shield, requireRole: "admin" },
  { label: "Invitations", href: "/admin/invitations", icon: Bell, requireRole: "admin" },
  { label: "API Keys", href: "/admin/api-keys", icon: Key, requireRole: "admin" },
  { label: "Webhooks", href: "/admin/webhooks", icon: Webhook, requireRole: "admin" },
  { label: "Audit Log", href: "/admin/audit-log", icon: ScrollText, requireRole: "admin" },
  { label: "Entitlements", href: "/admin/entitlements", icon: ListChecks, requireRole: "admin" },
  { label: "Platform Users", href: "/admin/platform/admins", icon: Activity, requireRole: "super_admin" },
  { label: "Feature Flags", href: "/admin/platform/feature-flags", icon: ListChecks, requireRole: "super_admin" },
  { label: "Sessions", href: "/admin/sessions", icon: Activity, requireRole: "super_admin" },
  // Personal — visible to every authenticated user.
  { label: "Security (MFA)", href: "/settings/mfa", icon: Shield },
  { label: "Notifications", href: "/settings/notifications", icon: Bell },
  { label: "Data & Privacy", href: "/settings/data", icon: FileText },
  { label: "Pricing", href: "/pricing", icon: CreditCard },
];

export function CommandPalette() {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const { isAuthenticated, platformRole, orgRole, logout } = useAuth();
  const router = useRouter();

  // Hotkey: cmd+K (mac) / ctrl+K (other). Standard for command palettes
  // since Linear / GitHub / Slack made it the de-facto convention.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setOpen((s) => !s);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const admin = isAdmin(platformRole, orgRole);
  const superAdmin = isSuperAdmin(platformRole);
  const visibleNav = NAV_COMMANDS.filter((c) => {
    if (c.requireRole === "admin") return admin;
    if (c.requireRole === "super_admin") return superAdmin;
    return true;
  });

  // Async user search — only when we have a query AND the caller is
  // platform-admin enough to use it. Saves a wasted RPC for everyone
  // else and matches the server-side gate on SearchUsers.
  const userSearchEnabled = open && superAdmin && query.length >= 2;
  const userResults = useUsersSearch(query, userSearchEnabled);

  function navigate(href: string) {
    setOpen(false);
    setQuery("");
    router.push(href);
  }

  if (!isAuthenticated) return null;

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent className="overflow-hidden p-0 max-w-xl">
        <DialogTitle className="sr-only">Command palette</DialogTitle>
        <Command shouldFilter={true}>
          <CommandInput
            placeholder="Search or jump to..."
            value={query}
            onValueChange={setQuery}
          />
          <CommandList>
            <CommandEmpty>No results.</CommandEmpty>

            <CommandGroup heading="Navigate">
              {visibleNav.map((c) => (
                <CommandItem
                  key={c.href}
                  value={`${c.label} ${c.href}`}
                  onSelect={() => navigate(c.href)}
                >
                  <c.icon className="mr-2 h-4 w-4" />
                  {c.label}
                </CommandItem>
              ))}
            </CommandGroup>

            {superAdmin && userResults.length > 0 && (
              <CommandGroup heading="Users">
                {userResults.map((u) => (
                  <CommandItem
                    key={u.uuid}
                    value={`user ${u.email} ${u.name ?? ""}`}
                    onSelect={() => navigate(`/admin/users/${u.uuid}`)}
                  >
                    <Users className="mr-2 h-4 w-4" />
                    <span className="truncate">
                      {u.email}
                      {u.name ? ` — ${u.name}` : ""}
                    </span>
                  </CommandItem>
                ))}
              </CommandGroup>
            )}

            <CommandGroup heading="Actions">
              <CommandItem
                value="sign out logout"
                onSelect={async () => {
                  setOpen(false);
                  await logout();
                }}
              >
                <LogOut className="mr-2 h-4 w-4" />
                Sign out
              </CommandItem>
            </CommandGroup>
          </CommandList>
        </Command>
      </DialogContent>
    </Dialog>
  );
}
