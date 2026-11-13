import { decodeJWTPayload } from "./auth-session";

/**
 * A narrowed session (a team cast or a role view) is access-only and the host
 * caps it short, so a tab left open cannot go on acting as less than its
 * person: that guard stays as it is. A person who is USING the session must
 * not be cut off mid-task, though, so an active tab asks the host for the SAME
 * narrowed session again, from the person's own session, shortly before the
 * one it holds lapses. The host decides that ask exactly as it decided the
 * first, and a refusal leaves the session to lapse. A tab nobody touched since
 * its session was installed asks for nothing, and lapses.
 *
 * Activity is the person's input in this tab: a pointer press, a key, a wheel,
 * a touch, a move through history. A request is not: pages poll on their own,
 * and a forgotten tab that polls would otherwise keep itself open forever.
 */

/** How long before a narrowed bearer lapses an active tab reopens it. */
export const RENEW_LEAD_MS = 60_000;
/** After a failed reopen, the next input asks again no sooner than this. */
const RETRY_AFTER_MS = 5_000;
const ACTIVITY = [
	"pointerdown",
	"keydown",
	"wheel",
	"touchstart",
	"popstate",
] as const;

/** What the host recorded a narrowed session as, in the words that open it again. */
export type Reopen =
	| { teamId: string }
	| { installationId: string; roles: string[] };

/**
 * The open that gives the same narrowed session again, from the host's own
 * answer about it (Session.Current). A cast reopens its team; a role view on
 * one installation reopens its roles there. A view held inside a cast, or one
 * across installations, is not one open from the person's session, so it is
 * not reopened: it lapses as it always did.
 */
export function reopenOf(session: {
	castTeamId: string;
	roleView: { installationId: string; roles: string[] }[];
}): Reopen | null {
	if (session.castTeamId)
		return session.roleView.length === 0
			? { teamId: session.castTeamId }
			: null;
	if (session.roleView.length !== 1) return null;
	const [view] = session.roleView;
	return { installationId: view.installationId, roles: [...view.roles] };
}

/**
 * Calls `reopen` once, in the last `RENEW_LEAD_MS` (or quarter of its life,
 * if shorter) of `bearer`'s life, if and only if the person gave input in
 * this tab since it was installed. The life is timed on this tab's own
 * clocks from `installed` for the bearer's lifetime (`exp - iat`), so the
 * host's clock and this one need not agree; it is live only while BOTH say
 * so: the monotonic one (`performance.now()`), which a changed wall clock
 * cannot stretch, and the wall one (`Date.now()`), which keeps running while
 * the machine sleeps when the monotonic one may not. Never once it has
 * lapsed: an ended session is never brought back, so `reopen` is handed
 * `live` to ask again before it installs anything.
 *
 * Input is counted once per bearer, so an idle view can outlast its last
 * input by up to about two lifetimes: input just after one is installed earns
 * the next, which then lapses if nobody touches it.
 * `reopen` resolving is final (renewed, refused, or left); rejecting is a
 * transient failure the next input after `RETRY_AFTER_MS` asks again for.
 * Returns the stop; the caller stops it when the bearer is replaced or the
 * session ends.
 */
export function renewWhileActive(
	bearer: string,
	installed: { at: number; since: number },
	reopen: (live: () => boolean) => Promise<void>,
): () => void {
	const { exp, iat } = decodeJWTPayload(bearer);
	if (typeof exp !== "number" || typeof iat !== "number" || exp <= iat)
		return () => {};
	const life = (exp - iat) * 1000;
	const lapsesAt = installed.at + life;
	const dueAt = lapsesAt - Math.min(RENEW_LEAD_MS, life / 4);
	let active = false;
	let asking = false;
	let stopped = false;
	let notBefore = 0;
	const live = () =>
		!stopped &&
		performance.now() < lapsesAt &&
		Date.now() < installed.since + life;

	const attempt = () => {
		const now = performance.now();
		if (!live() || asking || !active || now < dueAt || now < notBefore) return;
		asking = true;
		reopen(live).then(
			() => stop(),
			() => {
				asking = false;
				notBefore = performance.now() + RETRY_AFTER_MS;
			},
		);
	};
	// Only the person's own input: a script's synthetic event is not them.
	const onActivity = (event: Event) => {
		if (!event.isTrusted) return;
		active = true;
		attempt();
	};
	const timer = setTimeout(attempt, Math.max(0, dueAt - performance.now()));
	const options = { capture: true, passive: true } as const;
	for (const event of ACTIVITY)
		window.addEventListener(event, onActivity, options);

	function stop() {
		if (stopped) return;
		stopped = true;
		clearTimeout(timer);
		for (const event of ACTIVITY)
			window.removeEventListener(event, onActivity, options);
	}
	return stop;
}
