"use client";

import { Building2 } from "lucide-react";
import { useRef, useState } from "react";
import { toast } from "sonner";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/components/ui/select";
import { useAuth } from "@/lib/auth";
import { useOrganizations } from "@/lib/hooks";

// OrgSelector changes the authenticated tenant for every admin surface. The
// selected value comes from the signed access token, never page-local state.
//
// Built on the shadcn Select primitive (Radix under the hood) instead
// of a native <select>. Native selects open the OS-level dropdown,
// which:
//   - styles inconsistently with the rest of the design system,
//   - can't be driven by Playwright via getByRole("option") — the test
//     pattern every other admin page already uses,
//   - has no icon or rich content support per option.
export function OrgSelector() {
	const { data: orgs = [], isLoading } = useOrganizations();
	const { organizationId, switchOrganization } = useAuth();
	const [isSwitching, setIsSwitching] = useState(false);
	const switchingRef = useRef(false);

	async function handleChange(nextOrganizationId: string | null) {
		if (
			!nextOrganizationId ||
			nextOrganizationId === organizationId ||
			switchingRef.current
		)
			return;
		switchingRef.current = true;
		setIsSwitching(true);
		try {
			await switchOrganization(nextOrganizationId);
		} catch (error) {
			toast.error("Couldn't switch organization", {
				description: error instanceof Error ? error.message : "Try again.",
			});
		} finally {
			switchingRef.current = false;
			setIsSwitching(false);
		}
	}

	return (
		<Select
			value={organizationId ?? ""}
			onValueChange={handleChange}
			disabled={isLoading || isSwitching}
		>
			<SelectTrigger className="w-[260px]">
				<Building2 className="mr-2 h-4 w-4 shrink-0 text-muted-foreground" />
				<SelectValue
					placeholder={
						isLoading
							? "Loading orgs…"
							: isSwitching
								? "Switching…"
								: "Select organization…"
					}
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
