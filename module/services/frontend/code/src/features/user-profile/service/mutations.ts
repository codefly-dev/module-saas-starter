import { type UserProfilePatch, userProfileClient } from "./client";

/** userProfileMutations update the current user's typed profile metadata. */
export const userProfileMutations = {
	update: (patch: UserProfilePatch) => userProfileClient.update(patch),
};
