/**
 * The host procedures a browser reaches, and the only ones that are forwarded
 * to the gateway.
 *
 * Deny by default, and in two lists, because there are two browsers. A host
 * procedure is forwarded because something that runs in a browser calls it, and
 * for no other reason: the gateway's edge exposure says whether a procedure may
 * be called with a person's bearer at all, which is a weaker statement —
 * `InstallationService/Install` and `AuthorityService/Issue` are both
 * edge-authenticated and neither is a browser's call.
 */

/**
 * The solution Setup tab's host installation procedures: the installation and
 * role-grant reads and changes, and the offer reads.
 *
 * Both this app's own pages and a solution's mounted `<SolutionSetup binding>`
 * (from `@codefly-dev/saas-ui/solution`) call these, the second over the
 * solution proxy's base, so this is the list that proxy forwards.
 *
 * `InstallationService/Install` is deliberately not among them: installing stays
 * behind the host's own consent form. Neither is
 * `InstallationService/Current` — that is a module's call, and a module reaches
 * the host through the hop.
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

/**
 * The session interface's browser edge, which this app's own chrome calls and
 * nothing else does: what the host says this session is, and the two ways to
 * open a narrower one.
 *
 * `Current` is the one that decides whether administrator actions are offered:
 * the auth provider reads it to learn whether the bearer is a cast, and an
 * unconfirmed session is never presented as the person — so withholding this
 * route withholds every administrator action from everyone.
 *
 * Not on this list, and not reachable from a browser: `SessionService/SignIn`
 * and `SessionService/ListFixturePeople`, which no page of this app calls — a
 * person signs in through `/v1/auth/*`.
 *
 * Kept off the solution proxy on purpose. A solution's frontend is a different
 * browser from the host's chrome: it mounts the Setup tab, and opening a
 * narrowed session for the person is the host's own act, not a solution's.
 */
export const HOST_CHROME_PROCEDURES = [
	"/host.session.v1.SessionService/Current",
	"/host.session.v1.SessionService/OpenRoleView",
	"/host.session.v1.SessionService/OpenTeamCast",
] as const;

/**
 * Every host procedure this app forwards from its own origin: the Setup tab's
 * and its chrome's.
 *
 * `host-browser-procedures.test.ts` holds it to the call sites — it reads the
 * host clients out of this app's source, so a page that calls a host procedure
 * with no route here fails the suite instead of answering 404 in a browser.
 */
export const HOST_BROWSER_PROCEDURES = [
	...HOST_SETUP_PROCEDURES,
	...HOST_CHROME_PROCEDURES,
] as const;

/** Whether a path (with its leading slash) is one of the Setup tab's procedures. */
export function isHostSetupProcedure(path: string): boolean {
	return (HOST_SETUP_PROCEDURES as readonly string[]).includes(path);
}

/** Whether a path (with its leading slash) is forwarded from this app's origin. */
export function isHostBrowserProcedure(path: string): boolean {
	return (HOST_BROWSER_PROCEDURES as readonly string[]).includes(path);
}
