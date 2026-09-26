/**
 * What the registration endpoint says about a registrant, at state changes only.
 *
 * A solution re-registers on a heartbeat (every 15s by default, per surface), so
 * a line per beat is a line per solution every few seconds, forever — it buried
 * everything else this server printed. A beat that finds the registration as it
 * left it is not an event: the lease it renews is already recorded by the
 * registry. What an operator needs is the change — a solution registering, its
 * registered record moving to a new revision, a registration starting to be
 * refused (and why), a different refusal, and the recovery — so exactly those
 * are logged, and every beat in between is only counted. The count rides on the
 * next line this module prints for that solution ("after N beats"), which is the
 * per-beat trace kept without a per-beat line.
 *
 * The registrant logs its own side of the same transitions (registered, rejected
 * with a reason, backing off) — see the solution runtime's heartbeat — so the
 * two logs read as one story without either repeating itself per beat.
 *
 * State is per process, like the rest of this server's registration caches: a
 * restarted frontend logs each solution's first beat once, which is the right
 * thing to say after a restart.
 */

/** A registration beat's outcome, as the endpoint answered it. */
export type RegistrationBeat =
	| { ok: true; revision: number; status: string }
	| { ok: false; httpStatus: number; reason: string };

type Held =
	| { ok: true; revision: number; status: string; beats: number }
	| { ok: false; httpStatus: number; reason: string; beats: number };

/**
 * The key a beat whose credential did not verify is held under. Its claimed id
 * is not believed, so every unverified beat shares one state: an unauthenticated
 * caller can neither grow this map nor print a line per request.
 */
export const UNVERIFIED_REGISTRANT = "(unverified credential)";

/** Distinct registrants tracked before the oldest is forgotten. */
const MAX_TRACKED = 256;

const globalForLog = globalThis as {
	__solutionRegistrationLog?: Map<string, Held>;
};

function held(): Map<string, Held> {
	globalForLog.__solutionRegistrationLog ??= new Map();
	return globalForLog.__solutionRegistrationLog;
}

function after(beats: number): string {
	return beats === 1 ? "after 1 beat" : `after ${beats} beats`;
}

/**
 * Record one beat for `solution` and log it only when it changes what is known
 * about that registration. Returns the line it logged, or undefined for a beat
 * that changed nothing.
 */
export function observeRegistrationBeat(
	solution: string,
	beat: RegistrationBeat,
): string | undefined {
	const states = held();
	const previous = states.get(solution);
	const unchanged =
		previous !== undefined &&
		(beat.ok
			? previous.ok &&
				previous.revision === beat.revision &&
				previous.status === beat.status
			: !previous.ok &&
				previous.httpStatus === beat.httpStatus &&
				previous.reason === beat.reason);
	if (unchanged) {
		previous.beats += 1;
		return undefined;
	}

	let line: string;
	if (beat.ok) {
		if (previous === undefined) {
			line = `solution registration: "${solution}" registered (revision ${beat.revision}, ${beat.status})`;
		} else if (!previous.ok) {
			line = `solution registration: "${solution}" registered again (revision ${beat.revision}, ${beat.status}) — recovered from ${previous.httpStatus} ${previous.reason} ${after(previous.beats)}`;
		} else {
			line = `solution registration: "${solution}" now at revision ${beat.revision} (${beat.status}; was revision ${previous.revision}, ${previous.status}, held ${after(previous.beats)})`;
		}
		console.info(line);
	} else {
		const since =
			previous === undefined
				? ""
				: previous.ok
					? ` — was registered at revision ${previous.revision} ${after(previous.beats)}`
					: ` — was refused ${previous.httpStatus} ${previous.reason} ${after(previous.beats)}`;
		line = `solution registration: "${solution}" refused ${beat.httpStatus} ${beat.reason}${since}; the same refusal is not logged again until it changes or the registration succeeds`;
		console.warn(line);
	}

	// Insertion order is recency order: re-insert on every change, drop the
	// oldest past the bound.
	states.delete(solution);
	states.set(solution, { ...beat, beats: 1 });
	if (states.size > MAX_TRACKED) {
		const oldest = states.keys().next().value;
		if (oldest !== undefined) states.delete(oldest);
	}
	return line;
}

/**
 * Record that `solution` removed its own registration. A removal is always a
 * change, and a later beat logs as a fresh registration.
 */
export function observeRegistrationRemoved(
	solution: string,
	revision: number,
): string {
	const states = held();
	const previous = states.get(solution);
	states.delete(solution);
	const line = `solution registration: "${solution}" removed its registration (revision ${revision})${
		previous?.ok ? `, held ${after(previous.beats)}` : ""
	}`;
	console.info(line);
	return line;
}
