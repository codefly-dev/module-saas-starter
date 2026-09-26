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
 * A standing refusal still says so again at {@link isStandingConditionMilestone} beats. Printing
 * a refusal once and never again is a log that goes quiet while the thing is
 * still broken, which is the failure the per-beat line at least did not have;
 * the milestones grow by ten, so an hour of refusals costs three lines.
 *
 * A beat whose credential never verified belongs to no registrant — the id it
 * claims is not believed — so it is not held here at all. It is an observation
 * about the credential check itself, which
 * {@link observeAuthorityRefusal} holds separately: keyed by the two things
 * that can go wrong rather than by a stand-in registrant, so a key set outage
 * and a refused credential happening at once do not evict each other's state
 * and print on every beat, and so the key set coming back is a recovery this
 * module can actually see — the first beat that verifies proves it.
 *
 * The registrant logs its own side of the same transitions (registered, rejected
 * with a reason, backing off) — see the solution runtime's heartbeat — so the
 * two logs read as one story without either repeating itself per beat.
 *
 * State is per process, like the rest of this server's registration caches: a
 * restarted frontend logs each solution's first beat once, which is the right
 * thing to say after a restart.
 */

/**
 * Why a registration was refused.
 *
 * A closed set on purpose. This value is the *identity* of the refusal, and a
 * state that carries caller-supplied text is a state that differs on every
 * beat — so a registrant that varied one field per beat would get a line per
 * beat, which is what this module exists to stop. Everything variable travels
 * as `detail`, which is printed but never compared.
 */
export type RegistrationRefusal =
	| "invalid_json"
	| "invalid_manifest"
	| "solution_not_authorized"
	| "incompatible_runtime"
	| `registry ${"unavailable" | "conflict" | "forbidden" | "rejected"}`;

/** A registration beat's outcome, as the endpoint answered it. */
export type RegistrationBeat =
	| { ok: true; revision: number; status: string }
	| {
			ok: false;
			httpStatus: number;
			reason: RegistrationRefusal;
			detail?: string;
	  };

/**
 * What the credential check answered for a beat that never reached a
 * registrant: the key set could not be reached to judge the credential, or it
 * was judged and refused. The two are tracked apart because only the first has
 * a recovery — a later beat that verifies proves the key set is reachable, and
 * proves nothing about whoever presented a credential this host rejected.
 */
export type AuthorityRefusal = "unreachable" | "refused";

const AUTHORITY_TEXT: Record<AuthorityRefusal, string> = {
	unreachable:
		"the registration key set is unreachable; beats are answered 503 rather than having their credential refused",
	refused: "a beat presented a credential this host does not accept (401)",
};

type Held =
	| { ok: true; revision: number; status: string; beats: number }
	| {
			ok: false;
			httpStatus: number;
			reason: RegistrationRefusal;
			detail?: string;
			beats: number;
	  };

/** Distinct registrants tracked before the oldest is forgotten. */
const MAX_TRACKED = 256;

/** How much caller-supplied text a line may carry. */
const MAX_DETAIL = 200;

const globalForLog = globalThis as {
	__solutionRegistrationLog?: Map<string, Held>;
	__solutionRegistrationAuthority?: Map<AuthorityRefusal, number>;
};

function held(): Map<string, Held> {
	globalForLog.__solutionRegistrationLog ??= new Map();
	return globalForLog.__solutionRegistrationLog;
}

function authority(): Map<AuthorityRefusal, number> {
	globalForLog.__solutionRegistrationAuthority ??= new Map();
	return globalForLog.__solutionRegistrationAuthority;
}

/**
 * Render a caller-supplied value as one bounded line.
 *
 * A manifest id, a compatibility reason and a registry status all reach a log
 * line from the request body or from another service. A newline in one of them
 * writes a second record that reads exactly like a real one, so the module that
 * owns the record boundary is the one that has to hold it: control characters
 * are escaped here rather than trusted to every producer upstream.
 */
function oneLine(value: string): string {
	const escaped = value.replace(
		// biome-ignore lint/suspicious/noControlCharactersInRegex: escaping them is the point
		/[\u0000-\u001f\u007f-\u009f\u2028\u2029]/g,
		(character) =>
			`\\x${character.charCodeAt(0).toString(16).padStart(2, "0")}`,
	);
	return escaped.length > MAX_DETAIL
		? `${escaped.slice(0, MAX_DETAIL)}…`
		: escaped;
}

function after(beats: number): string {
	return beats === 1 ? "after 1 beat" : `after ${beats} beats`;
}

/**
 * Whether a standing condition that has now lasted `occurrences` should say so
 * again. True at 10, 100, 1000 … so the line count grows with the logarithm of
 * the outage rather than with the outage.
 *
 * Exported because the snapshot read in registry.ts reports its own standing
 * failure the same way, and two copies of this rule would drift into two
 * cadences for one subsystem's logs.
 */
export function isStandingConditionMilestone(occurrences: number): boolean {
	if (occurrences < 10) {
		return false;
	}
	let threshold = 10;
	while (threshold < occurrences) {
		threshold *= 10;
	}
	return threshold === occurrences;
}

/** The refusal as a line reads it: its reason, and its detail when it has one. */
function refusalText(beat: {
	reason: RegistrationRefusal;
	detail?: string;
}): string {
	return beat.detail === undefined
		? beat.reason
		: `${beat.reason} (${oneLine(beat.detail)})`;
}

/**
 * Record one beat for `solution` and log it only when it changes what is known
 * about that registration, or when a standing refusal reaches a milestone.
 * Returns the line it logged, or undefined for a beat that changed nothing.
 */
export function observeRegistrationBeat(
	solution: string,
	beat: RegistrationBeat,
): string | undefined {
	const states = held();
	const name = oneLine(solution);
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
	if (unchanged && previous !== undefined) {
		previous.beats += 1;
		// Recency is what the bound below evicts on, and a registration that
		// beats steadily without changing is the most recently seen of all. Left
		// where its last *change* put it, it is the entry a churning neighbour
		// evicts first — and its next beat would then log as a fresh
		// registration that never happened.
		states.delete(solution);
		states.set(solution, previous);
		if (!previous.ok && isStandingConditionMilestone(previous.beats)) {
			const line = `solution registration: "${name}" is still refused ${previous.httpStatus} ${refusalText(previous)} ${after(previous.beats)}`;
			console.warn(line);
			return line;
		}
		return undefined;
	}

	let line: string;
	if (beat.ok) {
		const status = oneLine(beat.status);
		if (previous === undefined) {
			line = `solution registration: "${name}" registered (revision ${beat.revision}, ${status})`;
		} else if (!previous.ok) {
			line = `solution registration: "${name}" registered again (revision ${beat.revision}, ${status}) — recovered from ${previous.httpStatus} ${refusalText(previous)} ${after(previous.beats)}`;
		} else {
			line = `solution registration: "${name}" now at revision ${beat.revision} (${status}; was revision ${previous.revision}, ${oneLine(previous.status)}, held ${after(previous.beats)})`;
		}
		console.info(line);
	} else {
		const since =
			previous === undefined
				? ""
				: previous.ok
					? ` — was registered at revision ${previous.revision} ${after(previous.beats)}`
					: ` — was refused ${previous.httpStatus} ${refusalText(previous)} ${after(previous.beats)}`;
		line = `solution registration: "${name}" refused ${beat.httpStatus} ${refusalText(beat)}${since}; the same refusal is reported again only at 10, 100, 1000 … beats, or when it changes`;
		console.warn(line);
	}

	// Insertion order is recency order: every beat re-inserts, so the oldest key
	// is the one nothing has been heard from for longest.
	states.delete(solution);
	states.set(solution, { ...beat, beats: 1 });
	if (states.size > MAX_TRACKED) {
		const oldest = states.keys().next().value;
		if (oldest !== undefined) states.delete(oldest);
	}
	return line;
}

/**
 * Record that a beat never got past the credential check. Logged at the first
 * beat of each distinct failure, and again at milestones, so an outage that
 * lasts an hour is neither a line per beat nor a single line an hour ago.
 */
export function observeAuthorityRefusal(
	refusal: AuthorityRefusal,
): string | undefined {
	const modes = authority();
	const beats = (modes.get(refusal) ?? 0) + 1;
	modes.set(refusal, beats);
	if (beats !== 1 && !isStandingConditionMilestone(beats)) {
		return undefined;
	}
	const line = `solution registration: ${AUTHORITY_TEXT[refusal]} — ${after(beats)}`;
	console.warn(line);
	return line;
}

/**
 * Record that a beat's credential verified, which is the only evidence this
 * host has that the key set is reachable again. Closes an open `unreachable`
 * run with the recovery line the refusal promised.
 *
 * A refused credential is deliberately NOT closed by this: someone else's beat
 * verifying says nothing about the caller whose credential this host rejected,
 * and clearing it would report a recovery that did not happen.
 */
export function observeAuthorityReachable(): string | undefined {
	const modes = authority();
	const beats = modes.get("unreachable");
	if (beats === undefined) {
		return undefined;
	}
	modes.delete("unreachable");
	const line = `solution registration: the registration key set is reachable again; it answered 503 for ${beats === 1 ? "1 beat" : `${beats} beats`}`;
	console.info(line);
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
	const line = `solution registration: "${oneLine(solution)}" removed its registration (revision ${revision})${
		previous?.ok ? `, held ${after(previous.beats)}` : ""
	}`;
	console.info(line);
	return line;
}
