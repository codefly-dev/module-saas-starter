import type { AdminPlugin } from "@/lib/admin-core";

export const platformAdminPlugin: AdminPlugin = {
  name: "platform-admin",
  navItems: [
    {
      label: "Platform Users",
      href: "/platform",
      icon: "UserSearch",
      group: "Platform",
      requiredRole: "support",
    },
    {
      label: "Sessions",
      href: "/platform/sessions",
      icon: "Activity",
      group: "Platform",
      requiredRole: "support",
    },
    {
      label: "Admins",
      href: "/platform/admins",
      icon: "ShieldCheck",
      group: "Platform",
      requiredRole: "super_admin",
    },
    {
      label: "Feature Flags",
      href: "/platform/feature-flags",
      icon: "Flag",
      group: "Platform",
      requiredRole: "super_admin",
    },
    {
      label: "Entitlements",
      href: "/platform/entitlements",
      icon: "CreditCard",
      group: "Platform",
      requiredRole: "billing",
    },
  ],
};
