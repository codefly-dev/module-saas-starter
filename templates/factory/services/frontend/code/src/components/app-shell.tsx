"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { cn } from "@/lib/utils";
import { useAdminConfigContext } from "@/lib/providers";
import { hasRole, type Role, type AppTab } from "@/lib/admin-core";
import { isDevMode, getDevRole, getDevEmail } from "@/lib/dev-mode";
import {
  Home,
  Settings,
  ShieldCheck,
  ShieldAlert,
  LogOut,
  Moon,
  Sun,
  ChevronDown,
  UserSearch,
  type LucideIcon,
} from "lucide-react";
import { useTheme } from "next-themes";
import { useState } from "react";

// Icon registry — plugins reference icons by name
const iconMap: Record<string, LucideIcon> = {
  Home, Settings, ShieldCheck, ShieldAlert, UserSearch,
};

// Built-in tabs (always present)
const builtinTabs: AppTab[] = [
  { label: "Home", href: "/", icon: "Home", order: 0 },
  { label: "Settings", href: "/settings", icon: "Settings", order: 100 },
];

export function AppShell({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const { theme, setTheme } = useTheme();
  const [userMenuOpen, setUserMenuOpen] = useState(false);
  const config = useAdminConfigContext();

  // In dev mode, role comes from cookie (set by dev panel).
  // In production, this comes from the auth context / JWT.
  const userRole: Role = isDevMode() ? getDevRole() : "user";
  const userEmail = isDevMode() ? getDevEmail() : "user@example.com";

  // Merge built-in tabs + plugin tabs, filter by role, sort by order
  const allTabs = [...builtinTabs, ...config.tabs]
    .filter((tab) => !tab.minRole || hasRole(userRole, tab.minRole))
    .sort((a, b) => (a.order ?? 50) - (b.order ?? 50));

  return (
    <div className="min-h-screen flex flex-col">
      <header className="sticky top-0 z-50 border-b border-border bg-background/95 backdrop-blur supports-[backdrop-filter]:bg-background/60">
        <div className="mx-auto max-w-7xl px-4 sm:px-6 lg:px-8">
          <div className="flex h-14 items-center justify-between">
            {/* Logo */}
            <Link href="/" className="flex items-center gap-2.5 shrink-0">
              <div className="h-7 w-7 rounded-lg bg-foreground flex items-center justify-center">
                <span className="text-background text-xs font-bold">S</span>
              </div>
              <span className="font-semibold text-sm hidden sm:block">SaaS Starter</span>
            </Link>

            {/* Tabs from plugins */}
            <nav className="flex items-center gap-1">
              {allTabs.map((tab) => {
                const Icon = iconMap[tab.icon] ?? null;
                const active =
                  tab.href === "/"
                    ? pathname === "/"
                    : pathname.startsWith(tab.href);
                return (
                  <Link
                    key={tab.href}
                    href={tab.href}
                    className={cn(
                      "flex items-center gap-2 px-3 py-1.5 rounded-md text-sm transition-colors",
                      active
                        ? "bg-accent text-accent-foreground font-medium"
                        : "text-muted-foreground hover:text-foreground hover:bg-accent/50"
                    )}
                  >
                    {Icon ? <Icon className="h-4 w-4" /> : null}
                    {tab.label}
                  </Link>
                );
              })}
            </nav>

            {/* Right: Theme + User */}
            <div className="flex items-center gap-2 shrink-0">
              {/* Dev mode indicator */}
              {isDevMode() && (
                <span className="text-[10px] px-1.5 py-0.5 rounded bg-amber-500/10 text-amber-600 dark:text-amber-400 font-mono">
                  {userRole}
                </span>
              )}

              <button
                onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
                className="p-2 rounded-md text-muted-foreground hover:text-foreground hover:bg-accent/50 transition-colors"
              >
                <Sun className="h-4 w-4 hidden dark:block" />
                <Moon className="h-4 w-4 block dark:hidden" />
              </button>

              <div className="relative">
                <button
                  onClick={() => setUserMenuOpen(!userMenuOpen)}
                  className="flex items-center gap-2 px-2 py-1.5 rounded-md text-sm text-muted-foreground hover:text-foreground hover:bg-accent/50 transition-colors"
                >
                  <div className="h-6 w-6 rounded-full bg-gradient-to-br from-violet-500 to-fuchsia-500" />
                  <ChevronDown className="h-3 w-3" />
                </button>
                {userMenuOpen && (
                  <>
                    <div className="fixed inset-0" onClick={() => setUserMenuOpen(false)} />
                    <div className="absolute right-0 mt-1 w-48 rounded-lg border border-border bg-card shadow-lg py-1 z-50">
                      <div className="px-3 py-2 text-xs text-muted-foreground border-b border-border">
                        {userEmail}
                        <span className="ml-1 text-[10px] px-1 py-0.5 rounded bg-secondary font-mono">
                          {userRole}
                        </span>
                      </div>
                      <Link
                        href="/settings"
                        onClick={() => setUserMenuOpen(false)}
                        className="flex items-center gap-2 px-3 py-2 text-sm text-muted-foreground hover:text-foreground hover:bg-accent/50"
                      >
                        <Settings className="h-4 w-4" />
                        Settings
                      </Link>
                      <div className="border-t border-border my-1" />
                      <button className="flex items-center gap-2 px-3 py-2 text-sm text-muted-foreground hover:text-foreground hover:bg-accent/50 w-full">
                        <LogOut className="h-4 w-4" />
                        Sign out
                      </button>
                    </div>
                  </>
                )}
              </div>
            </div>
          </div>
        </div>
      </header>

      <main className="flex-1">
        <div className="mx-auto max-w-7xl px-4 sm:px-6 lg:px-8 py-8">
          {children}
        </div>
      </main>
    </div>
  );
}
