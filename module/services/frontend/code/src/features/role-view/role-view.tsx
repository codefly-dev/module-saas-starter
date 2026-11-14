"use client";

import {
	HoldingBadges,
	useCasting,
	ViewAsTeamButton,
} from "@codefly-dev/saas-ui/casting";
import { toast } from "sonner";
import type { RoleViewEntry } from "@/gen/host/session/v1/session_pb";
import { useAuth } from "@/lib/auth";

/** What a session acts as, when it acts as less than its person. */
export interface Narrowed {
	view: RoleViewEntry[];
	/** The team the session is cast onto: it acts as that team and nothing else. */
	castTeamId: string;
	/** False while the host's answer is on the way or could not be read: the session is then unconfirmed, neither narrowed nor whole. */
	confirmed: boolean;
}

/**
 * What the host says this session is narrowed to, read once by the auth
 * provider and consumed here, not read again: one read, one state. A session
 * the host could not be asked about is `confirmed: false`, never "whole".
 */
export function useNarrowedSession(): Narrowed {
	const { isAuthenticated, accessToken, sessionRead, castTeamId, roleView } =
		useAuth();
	if (!isAuthenticated || !accessToken)
		return { view: [], castTeamId: "", confirmed: true };
	return { view: roleView, castTeamId, confirmed: sessionRead === "confirmed" };
}

const back = (exit: () => Promise<void>) => () =>
	void exit().catch((e: unknown) =>
		toast.error(
			e instanceof Error ? e.message : "Could not restore your session.",
		),
	);

/**
 * "View as this team" on a team's page: what the team holds, as the host
 * lists it, and the kit's one control that asks the host for a session that is
 * this team and nothing else. Whether they may is the authority's decision and
 * is never worked out here: anyone on the page may ask, and a refusal is shown
 * in the host's words with its code.
 */
export function ActAsTeam({
	teamId,
	teamName,
}: {
	teamId: string;
	teamName: string;
}) {
	const { teams, session } = useCasting();
	const listed = teams.find((t) => t.id === teamId);
	const current = session?.castTeamId === teamId;
	return (
		<section
			aria-label="View as this team"
			className="space-y-3 rounded-lg border p-4"
		>
			<div className="flex flex-wrap items-start justify-between gap-3">
				<div className="min-w-0 flex-1 space-y-1">
					<h2 className="font-semibold">View as this team</h2>
					<p className="text-sm text-muted-foreground">
						{current
							? "You are viewing as this team: this session holds what it holds and nothing of your own, and what you do in it is attributed to the team."
							: "See the product as a member of this team sees it: its pages and roles, and none of yours. You do not have to be a member, and your own session is one click back."}
					</p>
				</div>
				<ViewAsTeamButton
					team={listed ?? { id: teamId, name: teamName, holds: [] }}
				/>
			</div>
			{listed && (
				<div className="flex flex-wrap items-center gap-2 text-sm">
					<span className="text-muted-foreground">It holds</span>
					<HoldingBadges holds={listed.holds} />
				</div>
			)}
		</section>
	);
}

/**
 * What a narrowed session needs said over everything else, whatever the page:
 * one that ENDED without the person asking, by name, with the one way on (an
 * expired one is left for them to leave: their own session is not installed
 * behind their back; one that had lapsed by a reload is acknowledged), and one
 * the host has not confirmed yet. A live cast or role view is said by the
 * calmer CastBar (the kit's) in the page itself. One the host refused to
 * reopen is said as soon as that is known, with when it will end.
 */
export function RoleViewBanner() {
	const {
		exitRoleView,
		narrowedSessionEnd,
		narrowedSessionEnding,
		dismissNarrowedSessionEnd,
	} = useAuth();
	const { confirmed } = useNarrowedSession();
	const { sessionRead } = useAuth();
	if (narrowedSessionEnd) {
		const expired = narrowedSessionEnd.reason === "expired";
		return (
			<div
				role="alert"
				className="fixed bottom-0 left-0 right-0 z-[9999] border-t-2 border-red-700 bg-red-200 px-4 py-2 text-sm text-red-950"
			>
				<span className="font-bold">
					Your session acting as {narrowedSessionEnd.as} has ended
				</span>{" "}
				&mdash;{" "}
				{expired
					? "its credential expired. Nothing more is done as them, and you are not back in your own session until you choose to be."
					: "its credential had lapsed when the page reloaded. You are back in your own session, with all of your own access."}
				<button
					type="button"
					className="ml-3 rounded bg-red-900 px-3 py-1 text-red-50"
					onClick={expired ? back(exitRoleView) : dismissNarrowedSessionEnd}
				>
					{expired ? "Return to my own session" : "Understood"}
				</button>
			</div>
		);
	}
	if (narrowedSessionEnding) {
		const at = new Date(narrowedSessionEnding.endsAt).toLocaleTimeString([], {
			hour: "2-digit",
			minute: "2-digit",
		});
		return (
			<div
				role="status"
				className="fixed bottom-0 left-0 right-0 z-[9999] border-t-2 border-amber-600 bg-amber-200 px-4 py-2 text-sm text-amber-950"
			>
				<span className="font-bold">
					Your session acting as {narrowedSessionEnding.as} will end at {at}
				</span>{" "}
				&mdash; it could not be renewed. Nothing is done as them after that, and
				you are not back in your own session until you choose to be.
				<button
					type="button"
					className="ml-3 rounded bg-amber-900 px-3 py-1 text-amber-50"
					onClick={back(exitRoleView)}
				>
					Return to my own session
				</button>
			</div>
		);
	}
	if (!confirmed)
		return (
			<div
				role="status"
				className="fixed bottom-0 left-0 right-0 z-[9999] border-t-2 border-amber-600 bg-amber-200 px-4 py-2 text-sm text-amber-950"
			>
				<span className="font-bold">
					{sessionRead === "reading"
						? "Confirming what this session is"
						: "Could not confirm what this session is"}
				</span>{" "}
				&mdash; it may be acting as less than you hold, so no administrator
				access is offered until the host says. The host decides every action.
			</div>
		);
	return null;
}
