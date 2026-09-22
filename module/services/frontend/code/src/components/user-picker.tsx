"use client";

import { useQuery } from "@tanstack/react-query";
import { useEffect, useId, useState } from "react";
import {
	useOrganizationService,
	usePlatformAdminService,
} from "@/lib/hooks/use-api-client";
import { Button, Input } from "@/shared/ui";

// orgId restricts the picker to existing tenant members (for team assignment).
// Omit it only on platform-admin surfaces that can search the global directory.
export function UserPicker({
	value,
	onChange,
	orgId,
	exclude = [],
}: {
	value: string;
	onChange: (id: string) => void;
	orgId?: string;
	exclude?: string[];
}) {
	const id = useId();
	const [search, setSearch] = useState("");
	const [query, setQuery] = useState("");
	useEffect(() => {
		const timer = setTimeout(() => setQuery(search.trim()), 200);
		return () => clearTimeout(timer);
	}, [search]);
	const platform = usePlatformAdminService();
	const organizations = useOrganizationService();
	const result = useQuery({
		queryKey: ["user-picker", orgId, query],
		queryFn: async () => {
			if (orgId) {
				const response = await organizations.listMembers({ orgId });
				return response.members
					.filter((member) => member.userEmail)
					.map((member) => ({ id: member.userId, label: member.userEmail }));
			}
			const response = await platform.searchUsers({ query, pageSize: 20 });
			return response.users.map((user) => ({
				id: user.uuid,
				label: user.profile.name
					? `${user.profile.name} (${user.primaryEmail})`
					: user.primaryEmail,
			}));
		},
		enabled: !value && query.length >= 2 && query === search.trim(),
		retry: false,
	});
	const options = (query === search.trim() ? (result.data ?? []) : []).filter(
		(user) =>
			!exclude.includes(user.id) &&
			(!orgId || user.label.toLowerCase().includes(search.toLowerCase())),
	);
	return (
		<div className="w-full max-w-sm space-y-2">
			<label htmlFor={id} className="text-sm font-medium">
				User
			</label>
			<Input
				id={id}
				placeholder={
					orgId ? "Search members by email…" : "Search by name or email…"
				}
				value={search}
				onChange={(event) => {
					setSearch(event.target.value);
					onChange("");
				}}
			/>
			{!value && search.trim().length >= 2 && (
				<div className="rounded-md border p-2" aria-live="polite">
					{query !== search.trim() || result.isLoading ? (
						"Searching…"
					) : result.isError ? (
						<span>
							Couldn&apos;t load users.{" "}
							<Button variant="link" onClick={() => result.refetch()}>
								Try again
							</Button>
						</span>
					) : options.length === 0 ? (
						"No matching users."
					) : (
						options.map((user) => (
							<Button
								key={user.id}
								type="button"
								variant="ghost"
								className="w-full justify-start"
								onClick={() => {
									setSearch(user.label);
									onChange(user.id);
								}}
							>
								{user.label}
							</Button>
						))
					)}
				</div>
			)}
			{value && <p className="text-xs text-muted-foreground">User selected</p>}
		</div>
	);
}
