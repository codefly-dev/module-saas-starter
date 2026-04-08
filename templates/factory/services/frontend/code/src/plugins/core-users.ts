import type { AdminPlugin } from "@/lib/admin-core";

export const coreUsersPlugin: AdminPlugin = {
  name: "core-users",
  navItems: [
    { label: "Users", href: "/users", icon: "Users" },
    { label: "Organizations", href: "/organizations", icon: "Building2" },
    { label: "Teams", href: "/teams", icon: "UsersRound", requiredRole: "member" },
    { label: "Invitations", href: "/invitations", icon: "Mail", requiredRole: "admin" },
    { label: "API Keys", href: "/api-keys", icon: "Key", requiredRole: "admin" },
    { label: "Roles", href: "/roles", icon: "Shield", requiredRole: "admin" },
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
