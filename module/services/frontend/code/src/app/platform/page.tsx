"use client";

import Link from "next/link";

const sections = [
  {
    title: "Platform Admins",
    description: "Manage platform-level admin roles and permissions.",
    href: "/platform/admins",
  },
  {
    title: "Feature Flags",
    description: "Create and toggle feature flags for gradual rollouts.",
    href: "/platform/feature-flags",
  },
  {
    title: "Sessions",
    description: "View and manage active user sessions.",
    href: "/sessions",
  },
  {
    title: "Entitlements",
    description: "View and override organization entitlements and plan limits.",
    href: "/entitlements",
  },
];

export default function PlatformPage() {
  return (
    <div>
      <h2 className="text-2xl font-bold mb-6">Platform Management</h2>
      <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
        {sections.map((s) => (
          <Link
            key={s.href}
            href={s.href}
            className="block p-6 border border-gray-200 dark:border-gray-800 rounded-lg hover:bg-gray-50 dark:hover:bg-gray-900/50 transition-colors"
          >
            <h3 className="text-lg font-semibold mb-1">{s.title}</h3>
            <p className="text-sm text-gray-500">{s.description}</p>
          </Link>
        ))}
      </div>
    </div>
  );
}
