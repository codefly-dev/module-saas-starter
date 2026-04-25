"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useAuth } from "@/lib/auth";
import { isAdmin } from "@/lib/permissions";
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
  HardDriveDownload,
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
      { label: "Users", href: "/admin/users", icon: Users },
      { label: "Organizations", href: "/admin/organizations", icon: Building2 },
      { label: "Teams", href: "/admin/teams", icon: UsersRound },
      { label: "Roles", href: "/admin/roles", icon: Shield },
      { label: "Invitations", href: "/admin/invitations", icon: Mail },
      { label: "API Keys", href: "/admin/api-keys", icon: Key },
    ],
  },
  {
    group: "Platform",
    items: [
      { label: "Platform Users", href: "/admin/platform/admins", icon: UserSearch },
      { label: "Feature Flags", href: "/admin/platform/feature-flags", icon: Flag },
      { label: "Sessions", href: "/admin/sessions", icon: Monitor },
      { label: "Entitlements", href: "/admin/entitlements", icon: ShieldCheck },
      { label: "Audit Log", href: "/admin/audit-log", icon: Activity },
    ],
  },
  {
    group: "Integrations",
    items: [
      { label: "Webhooks", href: "/admin/webhooks", icon: Globe },
      { label: "Audit Export", href: "/admin/audit-export", icon: HardDriveDownload },
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

// isAdmin lives in @/lib/permissions and is imported above — the sidebar
// uses the same predicate <RoleGate> uses so role semantics stay unified.

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

          {/* Settings moved to user avatar dropdown */}
        </SidebarContent>

        <SidebarFooter>
          <SidebarMenu>
            <SidebarMenuItem>
              <DropdownMenu>
                <DropdownMenuTrigger render={<SidebarMenuButton size="lg" />}>
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
                </DropdownMenuTrigger>
                <DropdownMenuContent side="top" align="start" className="w-56">
                  <DropdownMenuItem render={<Link href="/settings/mfa" />}>
                    <Shield className="mr-2 h-4 w-4" />
                    Security
                  </DropdownMenuItem>
                  <DropdownMenuItem render={<Link href="/settings/notifications" />}>
                    <Bell className="mr-2 h-4 w-4" />
                    Notifications
                  </DropdownMenuItem>
                  <DropdownMenuItem render={<Link href="/settings/data" />}>
                    <FileText className="mr-2 h-4 w-4" />
                    Data & Privacy
                  </DropdownMenuItem>
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
        {/* Route-content fade-in. The key={pathname} forces React
            to remount the wrapper on every navigation, replaying the
            CSS animation. Snappy (200ms) so it feels like polish, not
            a delay. */}
        <main key={pathname} className="flex-1 p-6 animate-in fade-in slide-in-from-bottom-1 duration-200">
          {children}
        </main>
      </SidebarInset>

      <Toaster />
    </SidebarProvider>
  );
}
