import { Code, ConnectError } from "@connectrpc/connect";

/**
 * Why a list has nothing in it.
 *
 * An empty region on an admin surface means one of three different things, and
 * a reader cannot tell them apart from the absence itself:
 *
 * - `empty` — the read succeeded and there is genuinely nothing here.
 * - `forbidden` — the read was refused. Something may well be here; this viewer
 *   is not allowed to see it.
 * - `failed` — the read did not complete. Whether anything is here is unknown.
 *
 * Collapsing the last two into the first is the defect this exists to make
 * unwritable: on a permissions console, "nobody holds this role" shown to an
 * administrator whose read was denied is not a blank state, it is a wrong
 * answer, and it is the one they will act on.
 */
export type ReadOutcome = "empty" | "forbidden" | "failed";

/**
 * Classify a finished list read. `isError` comes from the query; `error` is
 * whatever it rejected with.
 *
 * Only `PermissionDenied` reads as `forbidden`. `Unauthenticated` does not: the
 * transport exchanges the refresh cookie and retries, and a session that is
 * genuinely gone is torn down by the auth provider rather than rendered as a
 * missing grant.
 */
export function readOutcome(isError: boolean, error: unknown): ReadOutcome {
	if (!isError) return "empty";
	return ConnectError.from(error).code === Code.PermissionDenied
		? "forbidden"
		: "failed";
}

/**
 * What to put on screen for a read that produced nothing, given what `subject`
 * is called on that surface ("members of this organization", "roles on this
 * team"). `empty` is the caller's own words, because only the caller knows what
 * the reader could do about it.
 */
export function readOutcomeMessage(
	outcome: ReadOutcome,
	subject: string,
	empty: string,
): string {
	switch (outcome) {
		case "forbidden":
			return `You don't have permission to see ${subject}. There may be some; this view can't show them.`;
		case "failed":
			return `Couldn't load ${subject}, so whether there are any is unknown.`;
		default:
			return empty;
	}
}

/**
 * Whether rows already on screen may survive a read that ended in `outcome`.
 *
 * TanStack Query keeps the last successful data when a refetch rejects, so a
 * surface that tests `rows.length === 0` before it tests `isError` never reaches
 * its denial branch: the refused read leaves the previous answer rendered, and
 * the reader is given no sign that it is no longer authorized.
 *
 * The two failures part company here. A refusal is about the reader's authority
 * over the rows themselves, so the rows go — on a permissions console, grants
 * left on screen after the read was refused are the same wrong answer as
 * "nobody holds this role", arrived at from the other direction. A transient
 * failure says nothing about authority, and blanking a good table because one
 * request did not come back is worse than the stale data: those rows stay, with
 * `staleReadNotice` saying they may be out of date.
 */
export function mayKeepRetainedRows(outcome: ReadOutcome): boolean {
	return outcome !== "forbidden";
}

/**
 * What to say above rows that survived a failed refresh, or null when there is
 * nothing to add — the read succeeded, or it was refused and the rows are gone.
 */
export function staleReadNotice(
	outcome: ReadOutcome,
	subject: string,
): string | null {
	return outcome === "failed"
		? `Couldn't refresh ${subject}, so this may be out of date.`
		: null;
}
