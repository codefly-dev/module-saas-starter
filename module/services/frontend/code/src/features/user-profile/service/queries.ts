import { queryOptions } from "@tanstack/react-query";
import { USER_PROFILE_QUERY_KEY, userProfileClient } from "./client";

/** userProfileQueries load the authenticated user's durable profile. */
export const userProfileQueries = {
	current: () =>
		queryOptions({
			queryKey: USER_PROFILE_QUERY_KEY,
			queryFn: () => userProfileClient.getSelf(),
			staleTime: 5 * 60 * 1000,
		}),
};
