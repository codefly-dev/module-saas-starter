import type { AdminPlugin } from "@/lib/admin-core";

export const platformAdminPlugin: AdminPlugin = {
  name: "platform-admin",
  navItems: [
    {
      label: "Platform Users",
      href: "/admin/platform",
      icon: "UserSearch",
      group: "Platform",
      requiredRole: "support",
    },
    {
      label: "Sessions",
      href: "/admin/sessions",
      icon: "Activity",
      group: "Platform",
      requiredRole: "support",
    },
    {
      label: "Admins",
      href: "/admin/platform/admins",
      icon: "ShieldCheck",
      group: "Platform",
      requiredRole: "super_admin",
    },
    {
      label: "Feature Flags",
      href: "/admin/platform/feature-flags",
      icon: "Flag",
      group: "Platform",
      requiredRole: "super_admin",
    },
    {
      label: "Entitlements",
      href: "/admin/entitlements",
      icon: "CreditCard",
      group: "Platform",
      requiredRole: "billing",
    },
  ],
};
