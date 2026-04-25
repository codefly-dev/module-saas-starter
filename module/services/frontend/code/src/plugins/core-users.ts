import type { AdminPlugin } from "@/lib/admin-core";

export const coreUsersPlugin: AdminPlugin = {
  name: "core-users",
  navItems: [
    { label: "Users", href: "/admin/users", icon: "Users" },
    { label: "Organizations", href: "/admin/organizations", icon: "Building2" },
    { label: "Teams", href: "/admin/teams", icon: "UsersRound", requiredRole: "member" },
    { label: "Invitations", href: "/admin/invitations", icon: "Mail", requiredRole: "admin" },
    { label: "API Keys", href: "/admin/api-keys", icon: "Key", requiredRole: "admin" },
    { label: "Roles", href: "/admin/roles", icon: "Shield", requiredRole: "admin" },
    { label: "Org Settings", href: "/admin/organizations/settings", icon: "Building2", group: "Organizations" },
    { label: "Webhooks", href: "/admin/webhooks", icon: "Globe", group: "Integrations" },
    { label: "MFA", href: "/settings/mfa", icon: "Shield", group: "Settings" },
    { label: "Notifications", href: "/settings/notifications", icon: "Bell", group: "Settings" },
    { label: "Data Privacy", href: "/settings/data", icon: "FileText", group: "Settings" },
  ],
  resources: [
    {
      name: "users",
      label: { singular: "User", plural: "Users" },
      columns: [
        { key: "primaryEmail", label: "Email", sortable: true },
        { key: "status", label: "Status" },
        { key: "createdAt", label: "Created", sortable: true },
      ],
      searchable: true,
    },
    {
      name: "organizations",
      label: { singular: "Organization", plural: "Organizations" },
      columns: [
        { key: "name", label: "Name", sortable: true },
        { key: "slug", label: "Slug" },
      ],
    },
  ],
};
