import { createClient } from "@connectrpc/connect";
import { UserService } from "@/gen/saas/accounts/v1/identity_pb";
import { apiTransport } from "@/lib/connect/transport";
import { stringProfilePatch, type UserProfileValues } from "../model/profile";

export type UserProfilePatch = Partial<UserProfileValues>;

export const USER_PROFILE_QUERY_KEY = ["user-profile", "self"] as const;

const client = createClient(UserService, apiTransport);

/** userProfileClient preserves unrelated metadata while updating the current user. */
export const userProfileClient = {
	getSelf: () => client.getSelf({}),
	update: updateSelfProfile,
};

async function updateSelfProfile(patch: UserProfilePatch) {
	const self = await client.getSelf({});
	const user = self.user;
	if (!user) {
		throw new Error("Current user is missing");
	}

	return client.updateUser({
		uuid: user.uuid,
		user: {
			uuid: user.uuid,
			primaryEmail: user.primaryEmail,
			profile: {
				...user.profile,
				...stringProfilePatch(patch),
			},
		},
		// Only the profile map is being changed. Declaring the mask makes the
		// intent explicit and keeps this call correct if the server ever honors
		// update_mask instead of the current "update non-empty fields" behavior.
		updateMask: { paths: ["profile"] },
	});
}
