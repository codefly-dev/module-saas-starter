"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useAdminConfig } from "@/lib/hooks/use-admin-config";
import { cn } from "@/lib/utils";
import {
  Users,
  Building2,
  UsersRound,
  Mail,
  Key,
  Shield,
  LayoutDashboard,
  ScrollText,
  Settings,
  Activity,
  Clock,
  CreditCard,
  Flag,
  type LucideIcon,
} from "lucide-react";

const iconMap: Record<string, LucideIcon> = {
  Users,
  Building2,
  UsersRound,
  Mail,
  Key,
  Shield,
  LayoutDashboard,
  ScrollText,
  Settings,
  Activity,
  Clock,
  CreditCard,
  Flag,
};

export function AdminLayout({ children }: { children: React.ReactNode }) {
  const config = useAdminConfig();
  const pathname = usePathname();

  // Group nav items
  const grouped = new Map<string, typeof config.navItems>();
  for (const item of config.navItems) {
    const group = item.group ?? "default";
    if (!grouped.has(group)) grouped.set(group, []);
    grouped.get(group)!.push(item);
  }

  return (
    <div className="flex min-h-screen">
      {/* Sidebar */}
      <aside className="w-64 border-r border-gray-200 dark:border-gray-800 bg-white dark:bg-gray-900 flex flex-col">
        <div className="p-4 border-b border-gray-200 dark:border-gray-800">
          <Link href="/" className="text-lg font-semibold">
            Admin
          </Link>
        </div>

        <nav className="flex-1 p-3 space-y-1">
          {/* Dashboard link */}
          <NavLink href="/" icon={LayoutDashboard} label="Dashboard" active={pathname === "/"} />

          {/* Plugin nav items */}
          {[...grouped.entries()].map(([group, items]) => (
            <div key={group}>
              {group !== "default" && (
                <div className="mt-4 mb-1 px-3 text-xs font-medium text-gray-500 uppercase tracking-wider">
                  {group}
                </div>
              )}
              {items.map((item) => {
                const Icon = item.icon ? iconMap[item.icon] : undefined;
                return (
                  <NavLink
                    key={item.href}
                    href={item.href}
                    icon={Icon}
                    label={item.label}
                    active={pathname === item.href}
                  />
                );
              })}
            </div>
          ))}
        </nav>
      </aside>

      {/* Main content */}
      <main className="flex-1 p-8">{children}</main>
    </div>
  );
}

function NavLink({
  href,
  icon: Icon,
  label,
  active,
}: {
  href: string;
  icon?: LucideIcon;
  label: string;
  active: boolean;
}) {
  return (
    <Link
      href={href}
      className={cn(
        "flex items-center gap-3 px-3 py-2 rounded-md text-sm transition-colors",
        active
          ? "bg-gray-100 dark:bg-gray-800 text-gray-900 dark:text-gray-100 font-medium"
          : "text-gray-600 dark:text-gray-400 hover:bg-gray-50 dark:hover:bg-gray-800/50"
      )}
    >
      {Icon && <Icon className="h-4 w-4" />}
      {label}
    </Link>
  );
}
