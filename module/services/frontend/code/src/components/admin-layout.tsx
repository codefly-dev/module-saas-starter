"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
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
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { Toaster } from "@/components/ui/sonner";
import { Separator } from "@/components/ui/separator";
import {
  LayoutDashboard,
  CreditCard,
  Settings,
  Users,
  Building2,
  UsersRound,
  Mail,
  Key,
  Shield,
  Activity,
  Flag,
  FileText,
  UserSearch,
  ShieldCheck,
  Globe,
  Bell,
  LogOut,
  ChevronUp,
  BookOpen,
  Monitor,
} from "lucide-react";
import { NotificationBell } from "@/features/notifications/ui/notification-bell";

// Regular user nav — visible to all authenticated users
const userNav = [
  { label: "Dashboard", href: "/", icon: LayoutDashboard },
  { label: "Pricing", href: "/pricing", icon: CreditCard },
];

// Admin nav — only visible to admin and super_admin
const adminNav = [
  {
    group: "Users & Access",
    items: [
      { label: "Users", href: "/users", icon: Users },
      { label: "Organizations", href: "/organizations", icon: Building2 },
      { label: "Teams", href: "/teams", icon: UsersRound },
      { label: "Roles", href: "/roles", icon: Shield },
      { label: "Invitations", href: "/invitations", icon: Mail },
      { label: "API Keys", href: "/api-keys", icon: Key },
    ],
  },
  {
    group: "Platform",
    items: [
      { label: "Platform Users", href: "/platform/admins", icon: UserSearch },
      { label: "Feature Flags", href: "/platform/feature-flags", icon: Flag },
      { label: "Sessions", href: "/sessions", icon: Monitor },
      { label: "Entitlements", href: "/entitlements", icon: ShieldCheck },
      { label: "Audit Log", href: "/audit-log", icon: Activity },
    ],
  },
  {
    group: "Integrations",
    items: [
      { label: "Webhooks", href: "/webhooks", icon: Globe },
    ],
  },
  {
    group: "Developer",
    items: [
      { label: "API Docs", href: "/docs", icon: BookOpen },
      { label: "SDKs", href: "/docs/sdks", icon: FileText },
      { label: "Compliance", href: "/docs/compliance", icon: ShieldCheck },
    ],
  },
];

function isAdmin(platformRole?: string, orgRole?: string): boolean {
  if (platformRole === "super_admin" || platformRole === "billing" || platformRole === "support") return true;
  if (orgRole === "admin" || orgRole === "owner") return true;
  return false;
}

export function AdminLayout({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const { user, platformRole, orgRole, logout } = useAuth();

  const showAdmin = isAdmin(platformRole, orgRole);

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
                    {showAdmin ? "Admin" : "Dashboard"}
                  </span>
                </div>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarHeader>

        <SidebarContent>
          {/* User nav — always visible */}
          <SidebarGroup>
            <SidebarGroupContent>
              <SidebarMenu>
                {userNav.map((item) => (
                  <SidebarMenuItem key={item.href}>
                    <SidebarMenuButton
                      render={<Link href={item.href} />}
                      isActive={item.href === "/" ? pathname === "/" : pathname.startsWith(item.href)}
                    >
                      <item.icon className="h-4 w-4" />
                      <span>{item.label}</span>
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                ))}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>

          {/* Admin nav — only for admin/super_admin */}
          {showAdmin && (
            <>
              <Separator />
              {adminNav.map((section) => (
                <SidebarGroup key={section.group}>
                  <SidebarGroupLabel>{section.group}</SidebarGroupLabel>
                  <SidebarGroupContent>
                    <SidebarMenu>
                      {section.items.map((item) => (
                        <SidebarMenuItem key={item.href}>
                          <SidebarMenuButton
                            render={<Link href={item.href} />}
                            isActive={pathname.startsWith(item.href)}
                          >
                            <item.icon className="h-4 w-4" />
                            <span>{item.label}</span>
                          </SidebarMenuButton>
                        </SidebarMenuItem>
                      ))}
                    </SidebarMenu>
                  </SidebarGroupContent>
                </SidebarGroup>
              ))}
            </>
          )}

          {/* Settings — always visible */}
          <Separator />
          <SidebarGroup>
            <SidebarGroupLabel>Settings</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu>
                <SidebarMenuItem>
                  <SidebarMenuButton
                    render={<Link href="/settings/mfa" />}
                    isActive={pathname.startsWith("/settings/mfa")}
                  >
                    <Shield className="h-4 w-4" />
                    <span>Security (MFA)</span>
                  </SidebarMenuButton>
                </SidebarMenuItem>
                <SidebarMenuItem>
                  <SidebarMenuButton
                    render={<Link href="/settings/notifications" />}
                    isActive={pathname.startsWith("/settings/notifications")}
                  >
                    <Bell className="h-4 w-4" />
                    <span>Notifications</span>
                  </SidebarMenuButton>
                </SidebarMenuItem>
                <SidebarMenuItem>
                  <SidebarMenuButton
                    render={<Link href="/settings/data" />}
                    isActive={pathname.startsWith("/settings/data")}
                  >
                    <FileText className="h-4 w-4" />
                    <span>Data & Privacy</span>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        </SidebarContent>

        <SidebarFooter>
          <SidebarMenu>
            <SidebarMenuItem>
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <SidebarMenuButton size="lg">
                    <Avatar className="h-8 w-8">
                      <AvatarFallback>{userInitials}</AvatarFallback>
                    </Avatar>
                    <div className="flex flex-col gap-0.5 leading-none">
                      <span className="text-sm font-medium">
                        {user?.email || user?.id}
                      </span>
                      {platformRole && (
                        <span className="text-xs text-muted-foreground">
                          {platformRole}
                        </span>
                      )}
                    </div>
                    <ChevronUp className="ml-auto h-4 w-4" />
                  </SidebarMenuButton>
                </DropdownMenuTrigger>
                <DropdownMenuContent side="top" align="start" className="w-56">
                  <DropdownMenuSeparator />
                  <DropdownMenuItem onClick={logout}>
                    <LogOut className="mr-2 h-4 w-4" />
                    Sign out
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarFooter>
      </Sidebar>

      <SidebarInset>
        <header className="flex h-14 items-center gap-2 border-b px-4">
          <SidebarTrigger />
          <Separator orientation="vertical" className="h-6" />
          <div className="flex-1" />
          <NotificationBell />
        </header>
        <main className="flex-1 p-6">{children}</main>
      </SidebarInset>

      <Toaster />
    </SidebarProvider>
  );
}
