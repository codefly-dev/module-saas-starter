// Removing a membership revokes every session the departing user holds bound to
// that organization (a database trigger on organization_members). Leaving or
// deleting the organization the browser is signed into would therefore sign
// the user out mid-action. The UI moves the session to another organization
// first, when there is one to move to.

export interface DepartureTarget {
	id: string;
	name: string;
}

/** The organization to move the session to before leaving `leavingId`. */
export function fallbackOrganization(
	organizations: readonly DepartureTarget[],
	leavingId: string,
): DepartureTarget | undefined {
	return organizations.find((organization) => organization.id !== leavingId);
}

// Registration gives every identity an organization named "Personal" with a
// "personal-" slug; onboarding treats it as not yet a workspace. The rule is the
// backend's (accounts onboarding detection), mirrored so the onboarding gate
// can offer a real organization the user already belongs to.
export function isPersonalOrganization(organization: {
	name: string;
	slug: string;
}): boolean {
	return (
		organization.name === "Personal" &&
		organization.slug.startsWith("personal-")
	);
}
