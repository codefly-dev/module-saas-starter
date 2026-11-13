import type { CastTeam } from "./host.js";

// What this tab last asked the host, kept outside React and in the tab's
// session storage: a way back changes who the session is, and a host remounts
// (or, at the edge, reloads) everything beneath it, so an "Opening…" or a
// refusal held in component state would vanish and leave the person with no
// answer. Per tab, like the cast itself.

/** A refusal: whom it was for, whether the person is back in their own view, and the reason. */
export interface CastRefusal {
	teamId: string;
	name: string;
	backInOwn: boolean;
	reason: string;
}

export interface CastAsk {
	open: boolean;
	asking: string;
	refusal: CastRefusal | null;
	/** Why the last way back failed, said as itself: the person is still in the cast. */
	backFailed: string;
}

const ASK_KEY = "codefly_cast_ask";
const TEAMS_KEY = "codefly_cast_teams";
const IDLE: CastAsk = { open: false, asking: "", refusal: null, backFailed: "" };

function read<T>(key: string): T | null {
	try {
		return JSON.parse(sessionStorage.getItem(key) ?? "null") as T | null;
	} catch {
		return null;
	}
}

function write(key: string, value: unknown) {
	try {
		if (value === null) sessionStorage.removeItem(key);
		else sessionStorage.setItem(key, JSON.stringify(value));
	} catch {
		// A tab that keeps nothing still casts; it only forgets across a reload.
	}
}

let ask: CastAsk | null = null;
const listeners = new Set<() => void>();

export function castAsk(): CastAsk {
	ask ??= { ...IDLE, ...read<Pick<CastAsk, "open" | "refusal">>(ASK_KEY) };
	return ask;
}

export function setCastAsk(next: Partial<CastAsk>) {
	ask = { ...castAsk(), ...next };
	write(ASK_KEY, { open: ask.open, refusal: ask.refusal });
	for (const listener of listeners) listener();
}

export function subscribeCastAsk(listener: () => void) {
	listeners.add(listener);
	return () => {
		listeners.delete(listener);
	};
}

export const idleCastAsk = () => IDLE;

/** Who a kept list was read for: a list never crosses people or organisations. */
const owner = (personId: string, organizationId: string) => `${personId}@${organizationId}`;

/** Keeps the teams the person's own session listed, so a cast (which lists only itself) can still switch. */
export function keepCastTeams(personId: string, organizationId: string, teams: CastTeam[]) {
	write(TEAMS_KEY, { owner: owner(personId, organizationId), teams });
}

export function keptCastTeams(personId: string, organizationId: string): CastTeam[] {
	const kept = read<{ owner: string; teams: CastTeam[] }>(TEAMS_KEY);
	return kept?.owner === owner(personId, organizationId) && Array.isArray(kept.teams) ? kept.teams : [];
}

/** Forgets what this tab kept of casting: call it on sign-out. */
export function forgetCastTeams() {
	write(TEAMS_KEY, null);
	write(ASK_KEY, null);
	ask = { ...IDLE };
	for (const listener of listeners) listener();
}
