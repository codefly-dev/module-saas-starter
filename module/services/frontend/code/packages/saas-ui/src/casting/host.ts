import { HostSessionService, type HostSession } from "@codefly-dev/saas-sdk";
import { Code, ConnectError, createClient, type Transport } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

/** What the host says this tab's session is (SessionService.Current). */
export interface CastSession {
	personId: string;
	personDisplayName: string;
	organizationId: string;
	/** The team the session is cast onto; empty when it is not a cast. */
	castTeamId: string;
	/** The roles a role view acts as, by installation; empty when it is not one. */
	roleView: { installationId: string; roles: string[] }[];
}

/** One role a team holds on one installation, named as a menu names it. */
export interface Holding {
	installationId: string;
	target: string;
	title: string;
	role: string;
}

/** A team the host says this person may act as, and what it holds. */
export interface CastTeam {
	id: string;
	name: string;
	holds: Holding[];
}

/**
 * Where team casting gets its data and does its acts: the host that serves the
 * session interface. Every answer is the host's; nothing here decides who may
 * act as what.
 */
export interface CastHost {
	/** The session (SessionService.Current). Unauthenticated means signed out. */
	current(): Promise<CastSession>;
	/** The teams the host lists for this session (SessionService.ListCastableTeams). */
	listTeams(): Promise<CastTeam[]>;
	/** Asks the host for a cast (SessionService.OpenTeamCast) and makes it this tab's session. */
	open(team: CastTeam): Promise<void>;
	/** Restores the session the cast or role view was opened from. */
	back(): Promise<void>;
	/** Told after the session changed (a cast opened, or the way back taken). */
	changed?(): void;
}

const castSession = (s: HostSession): CastSession => ({
	personId: s.personId,
	personDisplayName: s.personDisplayName,
	organizationId: s.organizationId,
	castTeamId: s.castTeamId,
	roleView: s.roleView.map((v) => ({ installationId: v.installationId, roles: [...v.roles] })),
});

export interface SessionCastHostOptions {
	/** The transport that reaches the host's session interface as this tab's session. */
	transport: Transport;
	/** Makes the cast the host opened this tab's session (a host whose client holds bearers adopts `cast.bearer`). */
	adopt(cast: HostSession, team: CastTeam): void | Promise<void>;
	back(): Promise<void>;
	changed?(): void;
}

/** A host reached through the generated `host.session.v1` client. */
export function sessionCastHost(o: SessionCastHostOptions): CastHost {
	const sessions = createClient(HostSessionService, o.transport);
	return {
		current: async () => castSession(await sessions.current({})),
		listTeams: async () =>
			(await sessions.listCastableTeams({})).teams.map((t) => ({
				id: t.teamId,
				name: t.displayName || t.teamId,
				holds: t.holds.map((h) => ({
					installationId: h.installationId,
					target: h.target,
					title: h.target,
					role: h.role,
				})),
			})),
		open: async (team) => o.adopt(await sessions.openTeamCast({ teamId: team.id }), team),
		back: o.back,
		changed: o.changed,
	};
}

/**
 * The local authority's edge: the session interface on the page's own origin,
 * with the browser's session in an HttpOnly cookie. A cast the edge opens is
 * already the browser's (it sets the cookie and hands no bearer to a script);
 * the way back is the edge's act-as form. A change reloads the page by default,
 * so everything on it is read again as the session it now is. Make it once
 * (at module scope, or memoized): a new host is a new session to read.
 */
export function edgeCastHost(o: { changed?(): void } = {}): CastHost {
	return sessionCastHost({
		// The page's own origin, resolved when a call is made.
		transport: createConnectTransport({ baseUrl: "/" }),
		adopt: () => undefined,
		back: async () => {
			const answer = await fetch("/auth/act-as", {
				method: "POST",
				redirect: "manual",
				body: new URLSearchParams({ action: "back", return: window.location.pathname }),
			});
			if (answer.type !== "opaqueredirect" && !answer.ok)
				throw new Error((await answer.text()).trim() || `the edge answered ${answer.status}`);
		},
		changed: o.changed ?? (() => window.location.reload()),
	});
}

/** Whether a host's answer says the session is not signed in. */
export const signedOut = (error: unknown) =>
	error instanceof ConnectError && error.code === Code.Unauthenticated;

/**
 * A refusal as the person reads it: the host's words with its code when the
 * host refused, and the plain reason otherwise (a failed way back is not a
 * host's refusal).
 */
export function refusalText(error: unknown): string {
	if (error instanceof ConnectError) return `${error.rawMessage} (${Code[error.code]})`;
	return error instanceof Error ? error.message : String(error);
}
