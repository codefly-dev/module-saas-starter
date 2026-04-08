"use client";

import { useOrganizations } from "@/lib/hooks";

interface OrgSelectorProps {
  value: string;
  onChange: (orgId: string) => void;
}

export function OrgSelector({ value, onChange }: OrgSelectorProps) {
  const { data: orgs = [] } = useOrganizations();

  return (
    <select
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="px-4 py-2 border border-gray-300 dark:border-gray-700 dark:bg-gray-800 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
    >
      <option value="">Select organization...</option>
      {orgs.map((org) => (
        <option key={org.id} value={org.id}>
          {org.name}
        </option>
      ))}
    </select>
  );
}
