"use client";

import { Code, ConnectError, createClient } from "@connectrpc/connect";
import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { hostRefusal } from "@/features/solution-installations/model/installations";
import { InstallationService } from "@/gen/host/installation/v1/installation_pb";
import { type RoleViewEntry, SessionService } from "@/gen/host/session/v1/session_pb";
import { TeamService } from "@/gen/saas/accounts/v1/teams_pb";
import { useAuth } from "@/lib/auth";
import { apiTransport } from "@/lib/connect/transport";

const installations = createClient(InstallationService, apiTransport);
const sessions = createClient(SessionService, apiTransport);
const teams = createClient(TeamService, apiTransport);

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
}
const whole: Narrowed = { view: [], castTeamId: "" };

/**
 * What the host says this session is narrowed to, read from the host and not
 * kept here: the authority recorded the view or the cast, so the answer follows
 * the token the browser holds. A token that lapsed back to the person's own
 * session says so.
 */
export function useNarrowedSession(): Narrowed {
	const { accessToken, isAuthenticated } = useAuth();
	const [narrowed, setNarrowed] = useState(whole);
	useEffect(() => {
		if (!isAuthenticated || !accessToken) return;
		let current = true;
		sessions
			.current({})
			.then((s) => current && setNarrowed({ view: s.roleView, castTeamId: s.castTeamId }))
			// A failed read says nothing about what the session is: an expired
			// cast's Current fails, and the bar must still say what it was.
			.catch(() => undefined);
		return () => {
			current = false;
		};
	}, [accessToken, isAuthenticated]);
	// Signed out, there is nothing to show; derived here, not set from the effect.
	return isAuthenticated && accessToken ? narrowed : whole;
}

export function useRoleView(): RoleViewEntry[] {
	return useNarrowedSession().view;
}

const back = (exit: () => Promise<void>) => () =>
	void exit().catch((e: unknown) => toast.error(e instanceof Error ? e.message : "Could not restore your session."));

/**
 * "Act as this team": asks the host for a session that is this team and nothing
 * else, so a person can see what a member of it sees without joining it. Whether
 * they may is the authority's decision and is never worked out here: anyone on
 * the page may ask, and a refusal is shown in the host's words with its code.
 */
export function ActAsTeam({ teamId, teamName }: { teamId: string; teamName: string }) {
	const { user, enterRoleView, exitRoleView } = useAuth();
	const { castTeamId } = useNarrowedSession();
	const [asking, setAsking] = useState(false);
	const [refusal, setRefusal] = useState("");
	const ask = async () => {
		setAsking(true);
		setRefusal("");
		try {
			enterRoleView((await sessions.openTeamCast({ teamId })).bearer, `team ${teamName}`);
		} catch (error) {
			setRefusal(`${hostRefusal(error)} (${Code[ConnectError.from(error).code]})`);
		} finally {
			setAsking(false);
		}
	};
	return (
		<section aria-label="Act as this team" className="space-y-2 rounded-lg border p-4">
			<h2 className="font-semibold">Act as this team</h2>
			{castTeamId === teamId ? (
				<>
					<p>
						You are <strong>{user?.email ?? "signed in"}</strong>, acting as team <strong>{teamName}</strong>. This session
						holds what the team holds and nothing of your own, and what you do in it is attributed to the team. (Data
						sources and scopes you hold personally are still read as you.)
					</p>
					<Link href="/" className="text-primary underline">
						See what this team sees
					</Link>{" "}
					<button type="button" className="underline" onClick={back(exitRoleView)}>
						Stop acting as this team
					</button>
				</>
			) : (
				<>
					<p className="text-sm text-muted-foreground">
						Open a session that holds what this team holds and nothing of your own: not your roles, not your other teams,
						not your administrator access. You do not have to be a member. Your own session is untouched and is the way
						back.
					</p>
					<button type="button" className="underline" disabled={asking} onClick={() => void ask()}>
						{asking ? "Asking…" : "Act as this team"}
					</button>
					{refusal && (
						<p role="alert">
							You are not acting as {teamName}. The host answered: {refusal}
						</p>
					)}
				</>
			)}
		</section>
	);
}

/** "View as": what the person holds on each installation, and a way to act as less of it. */
export function RoleViewSelector() {
	const { organizationId, accessToken, enterRoleView, exitRoleView } = useAuth();
	const view = useRoleView();
	const [heldRaw, setHeld] = useState<Held[]>([]);
	const held = organizationId && accessToken ? heldRaw : [];
	useEffect(() => {
		if (!organizationId || !accessToken) return;
		let current = true;
		installations
			.listHeld({ organizationId })
			.then((r) => current && setHeld(r.installations.map((h) => ({ id: h.installation?.id ?? "", target: h.installation?.target ?? "", roles: h.roles }))))
			.catch(() => current && setHeld([]));
		return () => {
			current = false;
		};
	}, [organizationId, accessToken]);

	const open = useCallback(
		async (installationId: string, roles: string[]) => {
			try {
				const opened = await sessions.openRoleView({ installationId, roles });
				const target = heldRaw.find((h) => h.id === installationId)?.target ?? installationId;
				enterRoleView(opened.bearer, `${roles.join(", ")} on ${target}`);
				toast.success(`Viewing as ${roles.join(", ")}`);
			} catch (error) {
				toast.error(error instanceof Error ? error.message : "Could not open the role view.");
			}
		},
		[enterRoleView, heldRaw],
	);

	const choices = held.filter((h) => h.roles.length > 1 || view.some((v) => v.installationId === h.id));
	if (choices.length === 0) return null;
	return (
		<div aria-label="View as" className="flex flex-col gap-2 text-sm">
			{choices.map((h) => (
				<div key={h.id} className="flex flex-wrap items-center gap-2">
					<span className="font-medium">{h.target}</span>
					{h.roles.length > 1 && (
						<button type="button" className="underline" onClick={() => void open(h.id, h.roles)}>
							all of mine
						</button>
					)}
					{h.roles.map((role) => (
						<button key={role} type="button" className="underline" onClick={() => void open(h.id, [role])}>
							{role}
						</button>
					))}
				</div>
			))}
			{view.length > 0 && (
				<button type="button" className="text-left underline" onClick={back(exitRoleView)}>
					Back to all my roles
				</button>
			)}
		</div>
	);
}

/**
 * The bar that says, whatever the page shows, that this session acts as less
 * than the person holds. A narrowed session that ENDED without the person
 * asking is said before anything else, by name, with the one way on: an
 * expired one is left for them to leave (their own session is not installed
 * behind their back), a reloaded one is acknowledged. Otherwise a cast is the
 * stronger statement and is said first: who
 * the person is, which team they act as, and how to stop. The team is named by
 * the host's roster when the cast may read it, and by its id when it may not.
 */
export function RoleViewBanner() {
	const { user, organizationId, exitRoleView, narrowedSessionEnd, dismissNarrowedSessionEnd } = useAuth();
	const { view, castTeamId } = useNarrowedSession();
	const [named, setNamed] = useState({ id: "", name: "" });
	useEffect(() => {
		if (!castTeamId || !organizationId) return;
		let current = true;
		teams
			.listTeams({ orgId: organizationId, memberId: "" })
			.then((r) => current && setNamed({ id: castTeamId, name: r.teams.find((t) => t.id === castTeamId)?.name ?? "" }))
			.catch(() => undefined);
		return () => {
			current = false;
		};
	}, [castTeamId, organizationId]);
	if (narrowedSessionEnd) {
		const expired = narrowedSessionEnd.reason === "expired";
		return (
			<div role="alert" className="fixed bottom-0 left-0 right-0 z-[9999] border-t-2 border-red-700 bg-red-200 px-4 py-2 text-sm text-red-950">
				<span className="font-bold">Your session acting as {narrowedSessionEnd.as} has ended</span> &mdash;{" "}
				{expired
					? "its credential expired. Nothing more is done as them, and you are not back in your own session until you choose to be."
					: "reloading the page ended it. You are back in your own session, with all of your own access."}
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
	if (view.length === 0 && !castTeamId) return null;
	const viewed = view.map((v) => `${v.roles.join(", ")} on ${v.installationId}`).join("; ");
	return (
		<div
			role="status"
			className={`fixed bottom-0 left-0 right-0 z-[9999] border-t-2 px-4 py-2 text-sm ${castTeamId ? "border-fuchsia-600 bg-fuchsia-300 text-fuchsia-950" : "border-sky-500 bg-sky-300 text-sky-950"}`}
		>
			{castTeamId ? (
				<>
					<span className="font-bold">Acting as team {(named.id === castTeamId && named.name) || castTeamId}</span> &mdash; you
					are {user?.email ?? "signed in"}, holding what this team holds and nothing of your own (data sources and
					scopes you hold personally are still read as you). What you do here is attributed to the team.
					{viewed && ` Viewing as ${viewed}.`}
				</>
			) : (
				<>
					<span className="font-bold">Role view</span> &mdash; acting as {viewed}
				</>
			)}
			<button type="button" className="ml-3 rounded bg-black/80 px-3 py-1 text-white" onClick={back(exitRoleView)}>
				{castTeamId ? "Stop acting as this team" : "Back to all my roles"}
			</button>
		</div>
	);
}
