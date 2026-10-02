/**
 * The return leg of a GitHub App installation, shared by every surface that
 * can start one — `<DatasourcesPanel>` and `<DeclaredSourceCard>`.
 *
 * Both halves belong together and to one definition: the state is redeemable
 * exactly once, so a surface that read the parameters but scrubbed them
 * differently (or not at all) would leave a single-use state and an
 * authorization code in the address bar, in history and in same-origin
 * referrers.
 *
 * Only one surface that redeems the return leg may be mounted on a page. Two
 * would race for the same single-use state and one of them would report a
 * rejection for an installation that in fact succeeded.
 */

/**
 * The parameters GitHub appends to the App's configured setup URL when it sends
 * the browser back: the installation it claims was installed, and the state we
 * minted. Both are required — `setup_action` is deliberately not consulted, so
 * an existing installation gaining repositories (`update`) lands here exactly as
 * a first install does.
 *
 * Returns null under SSR, where a surface renders before any address exists.
 */
export function readAppSetupReturn(): {
	state: string;
	installationId: string;
	code: string;
} | null {
	if (typeof window === "undefined") return null;
	const params = new URLSearchParams(window.location.search);
	const state = params.get("state");
	const installationId = params.get("installation_id");
	// `code` is deliberately not part of the trigger. It is absent when the App
	// was registered without "Request user authorization (OAuth) during
	// installation", and the host answers that with the error naming the setting
	// — which an operator can act on, where ignoring the return says nothing.
	return state && installationId
		? { state, installationId, code: params.get("code") ?? "" }
		: null;
}

/** Burns the single-use parameters out of the address bar, history and referrers. */
export function scrubAppSetupReturn(): void {
	const params = new URLSearchParams(window.location.search);
	for (const key of ["state", "installation_id", "setup_action", "code"])
		params.delete(key);
	const query = params.toString();
	window.history.replaceState(
		null,
		"",
		`${window.location.pathname}${query ? `?${query}` : ""}${window.location.hash}`,
	);
}
