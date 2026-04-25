"use client";

import { useOrganizations } from "@/lib/hooks";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Building2 } from "lucide-react";

interface OrgSelectorProps {
  value: string;
  onChange: (orgId: string) => void;
}

// OrgSelector — picks the active organization for any admin page that
// scopes to a single tenant (webhooks, entitlements, members, etc).
//
// Built on the shadcn Select primitive (Radix under the hood) instead
// of a native <select>. Native selects open the OS-level dropdown,
// which:
//   - styles inconsistently with the rest of the design system,
//   - can't be driven by Playwright via getByRole("option") — the test
//     pattern every other admin page already uses,
//   - has no icon or rich content support per option.
export function OrgSelector({ value, onChange }: OrgSelectorProps) {
  const { data: orgs = [], isLoading } = useOrganizations();

  return (
    <Select
      value={value}
      onValueChange={(v) => onChange(v ?? "")}
      disabled={isLoading}
    >
      <SelectTrigger className="w-[260px]">
        <Building2 className="mr-2 h-4 w-4 shrink-0 text-muted-foreground" />
        <SelectValue
          placeholder={isLoading ? "Loading orgs…" : "Select organization…"}
        />
      </SelectTrigger>
      <SelectContent>
        {orgs.map((org) => (
          <SelectItem key={org.id} value={org.id}>
            {org.name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
