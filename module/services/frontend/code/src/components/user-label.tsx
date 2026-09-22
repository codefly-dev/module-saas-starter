"use client";

import { useQuery } from "@tanstack/react-query";
import { useAuth } from "@/lib/auth";
import {
	useOrganizationService,
	useUserService,
} from "@/lib/hooks/use-api-client";

// Platform lookups use the existing authorized endpoint. Tenant viewers only
// receive the narrow directory projection returned with organization members.
export function UserLabel({
	userId,
	fallback = "User unavailable",
}: {
	userId: string;
	fallback?: string;
}) {
	const { user, platformRole, organizationId } = useAuth();
	const users = useUserService();
	const organizations = useOrganizationService();
	const detail = useQuery({
		queryKey: ["user-label", organizationId, userId],
		queryFn: () =>
			users.getUser({ identifier: { case: "uuid", value: userId } }),
		enabled: !!userId && !!platformRole && userId !== user?.id,
		staleTime: 60_000,
		retry: false,
	});
	const members = useQuery({
		queryKey: ["org-members", organizationId],
		queryFn: () => organizations.listMembers({ orgId: organizationId || "" }),
		enabled:
			!!userId && !platformRole && !!organizationId && userId !== user?.id,
		staleTime: 60_000,
		retry: false,
	});
	if (!userId) return <span>{fallback}</span>;
	if (userId === user?.id)
		return <span>{user.name || user.email || "You"}</span>;
	const account = detail.data;
	const name =
		account?.profile.name?.trim() ||
		[account?.profile.first_name, account?.profile.last_name]
			.filter(Boolean)
			.join(" ");
	const email =
		account?.primaryEmail ||
		members.data?.members.find((member) => member.userId === userId)?.userEmail;
	return (
		<span>
			{name
				? `${name}${email ? ` (${email})` : ""}`
				: email ||
					(detail.isLoading || members.isLoading ? "Loading user…" : fallback)}
		</span>
	);
}
