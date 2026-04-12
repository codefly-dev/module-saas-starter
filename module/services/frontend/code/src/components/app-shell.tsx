"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { cn } from "@/lib/utils";
import { useAdminConfigContext } from "@/lib/providers";
import { useAuth } from "@/lib/auth";
import type { NavItem } from "@/lib/admin-core";
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
  Users,
  UsersRound,
  Building2,
  Key,
  Mail,
  Shield,
  Activity,
  Flag,
  Monitor,
  Layers,
  FileText,
  CreditCard,
  type LucideIcon,
} from "lucide-react";
import { useTheme } from "next-themes";
import { useState } from "react";

const iconMap: Record<string, LucideIcon> = {
  Home, Settings, ShieldCheck, ShieldAlert, UserSearch,
  Users, UsersRound, Building2, Key, Mail, Shield, Activity, Flag,
  Monitor, Layers, FileText, CreditCard,
};

export function AppShell({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const { theme, setTheme } = useTheme();
  const [userMenuOpen, setUserMenuOpen] = useState(false);
  const config = useAdminConfigContext();
  const { user, platformRole, logout, isAuthenticated } = useAuth();

  const navItems: NavItem[] = [
    { label: "Home", href: "/", icon: "Home" },
    ...config.navItems,
  ];

  return (
    <div className="min-h-screen flex flex-col">
      <header className="sticky top-0 z-50 border-b border-border bg-background/95 backdrop-blur supports-[backdrop-filter]:bg-background/60">
        <div className="mx-auto max-w-7xl px-4 sm:px-6 lg:px-8">
          <div className="flex h-14 items-center justify-between">
            <Link href="/" className="flex items-center gap-2.5 shrink-0">
              <div className="h-7 w-7 rounded-lg bg-foreground flex items-center justify-center">
                <span className="text-background text-xs font-bold">S</span>
              </div>
              <span className="font-semibold text-sm hidden sm:block">SaaS Starter</span>
            </Link>

            <nav className="flex items-center gap-1 overflow-x-auto">
              {navItems.map((item) => {
                const Icon = item.icon ? iconMap[item.icon] : null;
                const active =
                  item.href === "/"
                    ? pathname === "/"
                    : pathname.startsWith(item.href);
                return (
                  <Link
                    key={item.href}
                    href={item.href}
                    className={cn(
                      "flex items-center gap-2 px-3 py-1.5 rounded-md text-sm whitespace-nowrap transition-colors",
                      active
                        ? "bg-accent text-accent-foreground font-medium"
                        : "text-muted-foreground hover:text-foreground hover:bg-accent/50"
                    )}
                  >
                    {Icon ? <Icon className="h-4 w-4" /> : null}
                    {item.label}
                  </Link>
                );
              })}
            </nav>

            <div className="flex items-center gap-2 shrink-0">
              {platformRole && (
                <span className="text-[10px] px-1.5 py-0.5 rounded bg-amber-500/10 text-amber-600 dark:text-amber-400 font-mono">
                  {platformRole}
                </span>
              )}

              <button
                onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
                className="p-2 rounded-md text-muted-foreground hover:text-foreground hover:bg-accent/50 transition-colors"
              >
                <Sun className="h-4 w-4 hidden dark:block" />
                <Moon className="h-4 w-4 block dark:hidden" />
              </button>

              {isAuthenticated && (
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
                          {user?.email || user?.id}
                        </div>
                        <div className="border-t border-border my-1" />
                        <button
                          onClick={() => { setUserMenuOpen(false); logout(); }}
                          className="flex items-center gap-2 px-3 py-2 text-sm text-muted-foreground hover:text-foreground hover:bg-accent/50 w-full"
                        >
                          <LogOut className="h-4 w-4" />
                          Sign out
                        </button>
                      </div>
                    </>
                  )}
                </div>
              )}
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
