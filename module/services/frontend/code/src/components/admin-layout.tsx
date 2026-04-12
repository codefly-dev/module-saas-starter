"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useAdminConfig } from "@/lib/hooks/use-admin-config";
import { useAuth } from "@/lib/auth";
import {
  SidebarProvider,
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarInset,
  SidebarTrigger,
} from "@/components/ui/sidebar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { Toaster } from "@/components/ui/sonner";
import { Separator } from "@/components/ui/separator";
import {
  Users,
  Building2,
  UsersRound,
  Mail,
  Key,
  Shield,
  LayoutDashboard,
  Activity,
  CreditCard,
  Flag,
  FileText,
  UserSearch,
  ShieldCheck,
  LogOut,
  ChevronUp,
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
  Activity,
  CreditCard,
  Flag,
  FileText,
  UserSearch,
  ShieldCheck,
};

export function AdminLayout({ children }: { children: React.ReactNode }) {
  const config = useAdminConfig();
  const pathname = usePathname();
  const { user, platformRole, logout, isAuthenticated } = useAuth();

  // Group nav items by category
  const grouped = new Map<string, typeof config.navItems>();
  for (const item of config.navItems) {
    const group = item.group ?? "Core";
    if (!grouped.has(group)) grouped.set(group, []);
    grouped.get(group)!.push(item);
  }

  const userInitials = user?.email
    ? user.email.slice(0, 2).toUpperCase()
    : user?.id?.slice(0, 2).toUpperCase() ?? "U";

  return (
    <SidebarProvider>
      <Sidebar>
        <SidebarHeader>
          <SidebarMenu>
            <SidebarMenuItem>
              <SidebarMenuButton size="lg" render={<Link href="/" />}>
                <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-primary text-primary-foreground">
                  <span className="text-sm font-bold">S</span>
                </div>
                <div className="flex flex-col gap-0.5 leading-none">
                  <span className="font-semibold">SaaS Starter</span>
                  <span className="text-xs text-muted-foreground">
                    Admin Dashboard
                  </span>
                </div>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarHeader>

        <SidebarContent>
          {/* Dashboard link */}
          <SidebarGroup>
            <SidebarGroupContent>
              <SidebarMenu>
                <SidebarMenuItem>
                  <SidebarMenuButton
                    render={<Link href="/" />}
                    isActive={pathname === "/"}
                  >
                    <LayoutDashboard className="h-4 w-4" />
                    <span>Dashboard</span>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>

          {/* Plugin nav items by group */}
          {[...grouped.entries()].map(([group, items]) => (
            <SidebarGroup key={group}>
              <SidebarGroupLabel>{group}</SidebarGroupLabel>
              <SidebarGroupContent>
                <SidebarMenu>
                  {items.map((item) => {
                    const Icon = item.icon ? iconMap[item.icon] : undefined;
                    const isActive =
                      item.href === "/"
                        ? pathname === "/"
                        : pathname === item.href ||
                          pathname.startsWith(item.href + "/");
                    return (
                      <SidebarMenuItem key={item.href}>
                        <SidebarMenuButton
                          render={<Link href={item.href} />}
                          isActive={isActive}
                        >
                          {Icon && <Icon className="h-4 w-4" />}
                          <span>{item.label}</span>
                        </SidebarMenuButton>
                      </SidebarMenuItem>
                    );
                  })}
                </SidebarMenu>
              </SidebarGroupContent>
            </SidebarGroup>
          ))}
        </SidebarContent>

        <SidebarFooter>
          <SidebarMenu>
            <SidebarMenuItem>
              {isAuthenticated ? (
                <DropdownMenu>
                  <DropdownMenuTrigger
                    render={<SidebarMenuButton size="lg" />}
                  >
                    <Avatar className="h-8 w-8 rounded-lg">
                      <AvatarFallback className="rounded-lg">
                        {userInitials}
                      </AvatarFallback>
                    </Avatar>
                    <div className="grid flex-1 text-left text-sm leading-tight">
                      <span className="truncate font-semibold">
                        {user?.email ?? user?.id ?? "User"}
                      </span>
                      {platformRole && (
                        <span className="truncate text-xs text-muted-foreground">
                          {platformRole}
                        </span>
                      )}
                    </div>
                    <ChevronUp className="ml-auto h-4 w-4" />
                  </DropdownMenuTrigger>
                  <DropdownMenuContent
                    className="w-[--radix-dropdown-menu-trigger-width] min-w-56"
                    side="top"
                    align="end"
                    sideOffset={4}
                  >
                    <DropdownMenuLabel className="p-0 font-normal">
                      <div className="flex items-center gap-2 px-1 py-1.5 text-left text-sm">
                        <Avatar className="h-8 w-8 rounded-lg">
                          <AvatarFallback className="rounded-lg">
                            {userInitials}
                          </AvatarFallback>
                        </Avatar>
                        <div className="grid flex-1 text-left text-sm leading-tight">
                          <span className="truncate font-semibold">
                            {user?.email ?? user?.id ?? "User"}
                          </span>
                          {platformRole && (
                            <span className="truncate text-xs text-muted-foreground">
                              {platformRole}
                            </span>
                          )}
                        </div>
                      </div>
                    </DropdownMenuLabel>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem onClick={() => logout()}>
                      <LogOut className="mr-2 h-4 w-4" />
                      Sign out
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
              ) : (
                <SidebarMenuButton render={<Link href="/auth/login" />}>
                  <LogOut className="h-4 w-4" />
                  <span>Sign in</span>
                </SidebarMenuButton>
              )}
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarFooter>
      </Sidebar>

      <SidebarInset>
        <header className="flex h-12 shrink-0 items-center gap-2 border-b px-4">
          <SidebarTrigger className="-ml-1" />
          <Separator orientation="vertical" className="mr-2 h-4" />
          <span className="text-sm text-muted-foreground">
            {pathname === "/"
              ? "Dashboard"
              : pathname
                  .split("/")
                  .filter(Boolean)
                  .map((s) => s.replace(/-/g, " "))
                  .map((s) => s.charAt(0).toUpperCase() + s.slice(1))
                  .join(" / ")}
          </span>
        </header>
        <main className="flex-1 p-6">{children}</main>
      </SidebarInset>

      <Toaster />
    </SidebarProvider>
  );
}
