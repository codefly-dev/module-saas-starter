"use client";

import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import {
	useOrganizationService,
	usePlatformAdminService,
} from "@/lib/hooks/use-api-client";
import {
	Chip,
	Command,
	CommandEmpty,
	CommandInput,
	CommandItem,
	CommandList,
	Field,
	useCommandFilter,
} from "@/shared/ui";

// What the platform directory search asks for, and the shortest query it will
// answer: the RPC searches, it does not list, so a one-letter question is a
// scan of every tenant's users and is not asked.
const DIRECTORY_PAGE = 20;
const DIRECTORY_MINIMUM = 2;

type Candidate = { id: string; label: string };

/**
 * Choosing one person.
 *
 * Built on the kit's `Command` (Base UI Autocomplete), so the list is a
 * listbox with combobox semantics — arrow keys, Enter, Home/End and
 * active-descendant — rather than a stack of buttons under a text box.
 *
 * Two directories, because the host holds two:
 *
 *   - `orgId` given: the organization's own roster, which `ListMembers`
 *     returns whole. It is shown on arrival and filtered as you type, so
 *     picking the one member of a three-person tenant takes no guess at how
 *     their email is spelled.
 *   - `orgId` omitted: the platform directory, which only answers a search.
 *     Below the minimum the picker says so instead of showing an empty box,
 *     and a full page says more may exist rather than implying these are all.
 *
 * A confirmed choice becomes a removable chip and the search box goes away:
 * before, the chosen label was left in the input, where it looked like a typed
 * query and any keystroke silently discarded the selection.
 */
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
	const [search, setSearch] = useState("");
	const [query, setQuery] = useState("");
	useEffect(() => {
		const timer = setTimeout(() => setQuery(search.trim()), 200);
		return () => clearTimeout(timer);
	}, [search]);
	const platform = usePlatformAdminService();
	const organizations = useOrganizationService();
	const { contains } = useCommandFilter({ sensitivity: "base" });

	// A roster read is keyed by the organization alone: it is one list, read
	// once and filtered here. A directory read is keyed by the query, because
	// the server is the one doing the filtering.
	const roster = useQuery({
		queryKey: ["user-picker", "roster", orgId],
		queryFn: async (): Promise<Candidate[]> => {
			const response = await organizations.listMembers({ orgId });
			return response.members
				.filter((member) => member.userEmail)
				.map((member) => ({ id: member.userId, label: member.userEmail }));
		},
		enabled: Boolean(orgId) && !value,
		retry: false,
	});
	const directory = useQuery({
		queryKey: ["user-picker", "directory", query],
		queryFn: async (): Promise<Candidate[]> => {
			const response = await platform.searchUsers({
				query,
				pageSize: DIRECTORY_PAGE,
			});
			return response.users.map((user) => ({
				id: user.uuid,
				label: user.profile.name
					? `${user.profile.name} (${user.primaryEmail})`
					: user.primaryEmail,
			}));
		},
		enabled: !orgId && !value && query.length >= DIRECTORY_MINIMUM,
		retry: false,
	});

	const result = orgId ? roster : directory;
	// Only the directory lags the typing: the roster is one read that does not
	// change as the search narrows, so it must not report "Searching…" for the
	// debounce of a filter it never asks the server.
	const settled = Boolean(orgId) || query === search.trim();
	const found = result.data ?? [];
	// The roster is filtered here with the kit's own matcher; the directory
	// arrived already filtered by the query it was read for, and is shown
	// untouched — but only once the query it answers is the one being asked.
	const options = (
		orgId ? found.filter((user) => contains(user.label, search)) : found
	).filter((user) => !exclude.includes(user.id));
	const truncated =
		!orgId && settled && found.length === DIRECTORY_PAGE
			? "Showing the first 20 matches. Narrow the search to see others."
			: undefined;
	const tooShort =
		!orgId && search.trim().length < DIRECTORY_MINIMUM
			? "Type at least two characters to search the directory."
			: undefined;

	if (value) {
		const chosen =
			found.find((user) => user.id === value)?.label ?? (search || value);
		return (
			<div className="w-full max-w-sm space-y-2">
				<p className="text-sm font-medium">User</p>
				<Chip
					onRemove={() => {
						onChange("");
						setSearch("");
					}}
					removeLabel={`Choose someone other than ${chosen}`}
				>
					{chosen}
				</Chip>
			</div>
		);
	}

	return (
		<div className="w-full max-w-sm">
			<Field
				label="User"
				description={tooShort ?? truncated}
				error={
					result.isError
						? "Couldn't read the directory. Edit the search to ask again."
						: undefined
				}
			>
				{(control) => (
					<Command value={search} onValueChange={setSearch}>
						<CommandInput
							{...control}
							placeholder={
								orgId ? "Search members by email…" : "Search by name or email…"
							}
						/>
						{!tooShort && (
							<CommandList>
								{result.isPending || !settled ? (
									<CommandEmpty>Searching…</CommandEmpty>
								) : options.length === 0 ? (
									<CommandEmpty>No matching users.</CommandEmpty>
								) : (
									options.map((user) => (
										<CommandItem
											key={user.id}
											value={user.id}
											onSelect={() => {
												setSearch(user.label);
												onChange(user.id);
											}}
										>
											{user.label}
										</CommandItem>
									))
								)}
							</CommandList>
						)}
					</Command>
				)}
			</Field>
		</div>
	);
}
