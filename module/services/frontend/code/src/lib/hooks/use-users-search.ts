import { useEffect, useState } from "react";
import { useUsers } from "./use-users";

interface UserHit {
	uuid: string;
	email: string;
	name?: string;
}

/**
 * useUsersSearch — debounced user-search hook for the command palette.
 *
 * - 200ms debounce: cmdk fires onValueChange on every keystroke; we
 *   don't want to fire one searchUsers RPC per character.
 * - `enabled=false` short-circuits the underlying useQuery, so we
 *   don't waste a network round trip when the palette is closed or
 *   the caller isn't allowed to search.
 * - Maps the proto User shape down to {uuid, email, name} so the
 *   palette UI doesn't depend on the wire format.
 */
export function useUsersSearch(query: string, enabled: boolean): UserHit[] {
	const [debounced, setDebounced] = useState("");
	useEffect(() => {
		if (!enabled) return;
		const t = setTimeout(() => setDebounced(query), 200);
		return () => clearTimeout(t);
	}, [query, enabled]);

	const { data } = useUsers(debounced, enabled && debounced === query);
	if (!enabled || !data) return [];
	return data.map((u) => ({
		uuid: u.uuid,
		email: u.primaryEmail,
		name: u.profile?.name,
	}));
}
