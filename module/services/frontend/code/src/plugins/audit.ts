import type { AdminPlugin } from "@/lib/admin-core";

export const auditPlugin: AdminPlugin = {
  name: "audit",
  navItems: [
    {
      label: "Audit Log",
      href: "/audit-log",
      icon: "FileText",
      requiredRole: "admin",
    },
  ],
};
