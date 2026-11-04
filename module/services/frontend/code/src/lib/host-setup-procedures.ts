/**
 * The host installation interface a signed-in person's page calls — the
 * solution Setup tab (`<SolutionSetup>` from `@codefly-dev/saas-ui/solution`)
 * — exactly the procedures the local authority's edge serves a browser: the
 * installation and role-grant reads and changes, and the offer reads.
 *
 * Install and Current are not among them: installing stays behind the host's
 * own consent form and Current is a module's call, so neither gains a route
 * from a browser. Each procedure listed is decided by the gateway and accounts
 * exactly as for any other caller; forwarding one grants nothing.
 */
export const HOST_SETUP_PROCEDURES = [
	"/host.installation.v1.InstallationService/List",
	"/host.installation.v1.InstallationService/ListHeld",
	"/host.installation.v1.InstallationService/Review",
	"/host.installation.v1.InstallationService/ListRoleGrants",
	"/host.installation.v1.InstallationService/GrantRole",
	"/host.installation.v1.InstallationService/RevokeRole",
	"/host.installation.v1.OfferService/ListOffers",
	"/host.installation.v1.OfferService/ReviewOffer",
] as const;

/** Whether a path (with its leading slash) is one of the Setup tab's procedures. */
export function isHostSetupProcedure(path: string): boolean {
	return (HOST_SETUP_PROCEDURES as readonly string[]).includes(path);
}
