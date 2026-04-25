import type { AdminPlugin } from "@/lib/admin-core";

export const auditPlugin: AdminPlugin = {
  name: "audit",
  navItems: [
    {
      label: "Audit Log",
      href: "/admin/audit-log",
      icon: "FileText",
      requiredRole: "admin",
    },
    {
      label: "API Docs",
      href: "/docs",
      icon: "FileText",
      group: "Developer",
    },
    {
      label: "SDKs",
      href: "/docs/sdks",
      icon: "FileText",
      group: "Developer",
    },
    {
      label: "Compliance",
      href: "/docs/compliance",
      icon: "ShieldCheck",
      group: "Developer",
    },
  ],
};
