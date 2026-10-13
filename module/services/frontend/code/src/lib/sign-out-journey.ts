import type { SignOutResult } from "@/lib/auth";

/**
 * Where a sign-out control sends the person once `logout()` has answered.
 *
 * Every user-facing sign-out control shares ONE destination rule, because the
 * two outcomes are not interchangeable and deciding per control is how they
 * drifted apart:
 *
 * - complete — the server revoked the session. `/auth/login`, which is what a
 *   finished sign-out looks like.
 * - incomplete — the server answered 503 "logout incomplete, retry", or nothing
 *   answered at all, so an already-issued access token stays valid until it
 *   expires. `/auth/logout`, the page that OWNS this case: it says the session
 *   may still be usable and offers the retry, which the kept refresh cookie
 *   still has the credential for.
 *
 * The defect this replaces: `logout()` reported incompleteness and only
 * `/auth/logout` read it. The Terms banner redirected to `/auth/login`
 * regardless, and the command palette discarded the result entirely — so the two
 * controls a person reaches when the account menu is unavailable were precisely
 * the two that presented an unfinished sign-out as finished. Local state is
 * cleared either way, which is what makes the wrong answer invisible: the app
 * looks signed out while the token is live.
 *
 * It is a plain function, not a hook: a control calls it inside the handler it
 * already has, and nothing about it depends on React.
 */
export const SIGN_OUT_COMPLETE_DESTINATION = "/auth/login";
export const SIGN_OUT_RETRY_DESTINATION = "/auth/logout";

export function signOutDestination(
	result: SignOutResult | undefined,
	completeDestination: string = SIGN_OUT_COMPLETE_DESTINATION,
): string {
	// Only the COMPLETE destination is a control's to choose. A control with its
	// own next step — returning to the invitation it was signed into the wrong
	// account for — still has one, and the incomplete case stays the shared
	// journey's, because that is the outcome a control must not be able to
	// present its own way.
	return result?.complete ? completeDestination : SIGN_OUT_RETRY_DESTINATION;
}

/**
 * Runs a sign-out and leaves for the destination its outcome names.
 *
 * A rejection is the same fact as an explicit incompleteness — nothing
 * confirmed the session ended — so it takes the retry journey rather than
 * propagating, which in a click handler would surface as an unhandled rejection
 * and leave the person on a page that still looks signed in.
 *
 * `navigate` exists so a test can observe the decision instead of the browser
 * performing it; production callers omit it.
 */
export async function signOutAndLeave(
	logout: () => Promise<SignOutResult>,
	options: {
		completeDestination?: string;
		navigate?: (destination: string) => void;
	} = {},
): Promise<void> {
	const navigate =
		options.navigate ??
		((destination: string) => {
			window.location.replace(destination);
		});
	let result: SignOutResult | undefined;
	try {
		result = await logout();
	} catch {
		result = { complete: false };
	}
	navigate(signOutDestination(result, options.completeDestination));
}
