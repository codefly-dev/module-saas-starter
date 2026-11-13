"use client";

import { createClient } from "@connectrpc/connect";
import { Eye } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { InstallationService } from "@/gen/host/installation/v1/installation_pb";
import {
	type RoleViewEntry,
	SessionService,
} from "@/gen/host/session/v1/session_pb";
import { useAuth } from "@/lib/auth";
import { apiTransport } from "@/lib/connect/transport";
import { Button } from "@/shared/ui";
import { HoldingBadges, useCastAsk, useTeamDirectory } from "./team-casting";

const installations = createClient(InstallationService, apiTransport);
const sessions = createClient(SessionService, apiTransport);

/** A role the person can act as on one installation, by the roles they hold now. */
export interface Held {
	id: string;
	target: string;
	roles: string[];
}

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

export function useRoleView(): RoleViewEntry[] {
	return useNarrowedSession().view;
}

const back = (exit: () => Promise<void>) => () =>
	void exit().catch((e: unknown) =>
		toast.error(
			e instanceof Error ? e.message : "Could not restore your session.",
		),
	);

/**
 * "View as this team" on a team's page: what the team holds, read before the
 * person asks, and the one control that asks the host for a session that is
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
	const { exitRoleView } = useAuth();
	const { castTeamId } = useNarrowedSession();
	const { teams } = useTeamDirectory();
	const {
		open: switcherOpen,
		asking: askingFor,
		refusal: lastRefusal,
		ask,
	} = useCastAsk();
	const holds = teams.find((t) => t.id === teamId)?.holds;
	const asking = askingFor === teamId;
	// The switcher says its own refusals; the card says the ones it was asked.
	const refusal =
		!switcherOpen && lastRefusal?.teamId === teamId ? lastRefusal : null;
	const current = castTeamId === teamId;
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
				{current ? (
					<Button variant="outline" onClick={back(exitRoleView)}>
						Back to my view
					</Button>
				) : (
					<Button
						disabled={asking}
						onClick={() => void ask({ id: teamId, name: teamName })}
					>
						<Eye className="mr-2 h-4 w-4" />
						{asking ? "Opening…" : "View as this team"}
					</Button>
				)}
			</div>
			{holds && (
				<div className="flex flex-wrap items-center gap-2 text-sm">
					<span className="text-muted-foreground">It holds</span>
					<HoldingBadges holds={holds} />
				</div>
			)}
			{refusal && (
				<p role="alert" className="text-sm">
					{refusal.backInOwn
						? `You are back in your own view. The host did not open ${teamName}: ${refusal.reason}`
						: `You are not viewing as ${teamName}. The host answered: ${refusal.reason}`}
				</p>
			)}
		</section>
	);
}

/** "View as": what the person holds on each installation, and a way to act as less of it. */
export function RoleViewSelector() {
	const { organizationId, accessToken, enterRoleView, exitRoleView } =
		useAuth();
	const view = useRoleView();
	const [heldRaw, setHeld] = useState<Held[]>([]);
	const held = organizationId && accessToken ? heldRaw : [];
	useEffect(() => {
		if (!organizationId || !accessToken) return;
		let current = true;
		installations
			.listHeld({ organizationId })
			.then(
				(r) =>
					current &&
					setHeld(
						r.installations.map((h) => ({
							id: h.installation?.id ?? "",
							target: h.installation?.target ?? "",
							roles: h.roles,
						})),
					),
			)
			.catch(() => current && setHeld([]));
		return () => {
			current = false;
		};
	}, [organizationId, accessToken]);

	const open = useCallback(
		async (installationId: string, roles: string[]) => {
			try {
				const opened = await sessions.openRoleView({ installationId, roles });
				const target =
					heldRaw.find((h) => h.id === installationId)?.target ??
					installationId;
				enterRoleView(opened.bearer, `${roles.join(", ")} on ${target}`);
				toast.success(`Viewing as ${roles.join(", ")}`);
			} catch (error) {
				toast.error(
					error instanceof Error
						? error.message
						: "Could not open the role view.",
				);
			}
		},
		[enterRoleView, heldRaw],
	);

	const choices = held.filter(
		(h) => h.roles.length > 1 || view.some((v) => v.installationId === h.id),
	);
	if (choices.length === 0) return null;
	return (
		<div aria-label="View as" className="flex flex-col gap-2 text-sm">
			{choices.map((h) => (
				<div key={h.id} className="flex flex-wrap items-center gap-2">
					<span className="font-medium">{h.target}</span>
					{h.roles.length > 1 && (
						<button
							type="button"
							className="underline"
							onClick={() => void open(h.id, h.roles)}
						>
							all of mine
						</button>
					)}
					{h.roles.map((role) => (
						<button
							key={role}
							type="button"
							className="underline"
							onClick={() => void open(h.id, [role])}
						>
							{role}
						</button>
					))}
				</div>
			))}
			{view.length > 0 && (
				<button
					type="button"
					className="text-left underline"
					onClick={back(exitRoleView)}
				>
					Back to all my roles
				</button>
			)}
		</div>
	);
}

/**
 * What a narrowed session needs said over everything else, whatever the page:
 * one that ENDED without the person asking, by name, with the one way on (an
 * expired one is left for them to leave: their own session is not installed
 * behind their back; one that had lapsed by a reload is acknowledged), and one
 * the host has not confirmed yet. A live cast or role view is said by the
 * calmer CastIndicator in the page itself.
 */
export function RoleViewBanner() {
	const { exitRoleView, narrowedSessionEnd, dismissNarrowedSessionEnd } =
		useAuth();
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
