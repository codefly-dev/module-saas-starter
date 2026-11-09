/**
 * What a role lets its holder do, in an organisation administrator's words.
 *
 * One vocabulary for every surface that shows a role — the install review,
 * the update review, a solution's Setup and a module page's — so the same
 * declaration never reads two ways. Every fact is the authority's reach
 * (`OfferRole.reach`, the core's `decide.Reach`): where a holder can cause an
 * operation, directly or through the solution acting for them, hop by hop.
 * Nothing here decides access; it only words it. Identifiers (operations,
 * paths, binding ids) stay behind a surface's technical details.
 */

/** A scope as an offer carries it. */
export interface AbilityScope {
	resourceKind: string;
	actions: readonly string[];
	attachedOnly?: boolean;
}

/** One reach row of a role (`OfferReach`). */
export interface AbilityReach {
	path: readonly string[];
	module: string;
	/** "role" on the target, "binding" through it, "person" on their own session. */
	via: string;
	confinement: string;
	scopes: readonly AbilityScope[];
	operations: readonly string[];
}

/** A declared role and its reach (`OfferRole`). */
export interface AbilityRole {
	name: string;
	scopes: readonly AbilityScope[];
	reach: readonly AbilityReach[];
}

/** One place a holder acts, and what they can do there. */
export interface AbilityGroup {
	/** Stable across reads: the place, not its wording. */
	key: string;
	/** "In pages", "Through pages, in documents", "With their own access in …". */
	where: string;
	lines: string[];
}

export interface RoleAbilities {
	role: string;
	groups: AbilityGroup[];
}

/** "A" or "An", for a role named in a sentence. */
export function article(word: string): string {
	return /^[aeiou]/i.test(word) ? "An" : "A";
}

/** "a", "a and b", "a, b and c". */
export function listText(words: readonly string[]): string {
	if (words.length < 2) return words[0] ?? "";
	return `${words.slice(0, -1).join(", ")} and ${words[words.length - 1]}`;
}

const verbs: Record<string, string> = {
	invoke: "use",
	write: "edit",
	admin: "manage",
	lookup: "look up",
};

/** A resource kind as a plural noun: `documents.collection` → "collections". */
export function kindText(kind: string): string {
	const name = (kind.split(".").pop() ?? kind).replace(/[_-]+/g, " ");
	if (name.endsWith("s")) return name;
	if (/[^aeiou]y$/.test(name)) return `${name.slice(0, -1)}ies`;
	return `${name}s`;
}

/**
 * What scopes let a holder do, one phrase per kind with its actions merged:
 * "read and edit pages; create collections". Never an identifier.
 */
export function scopesText(scopes: readonly AbilityScope[]): string {
	const kinds = new Map<string, { actions: string[]; attached: boolean }>();
	for (const scope of scopes) {
		const entry = kinds.get(scope.resourceKind) ?? {
			actions: [],
			attached: true,
		};
		for (const action of scope.actions) {
			const verb = verbs[action] ?? action;
			if (!entry.actions.includes(verb)) entry.actions.push(verb);
		}
		entry.attached &&= scope.attachedOnly === true;
		kinds.set(scope.resourceKind, entry);
	}
	return [...kinds]
		.map(
			([kind, { actions, attached }]) =>
				`${listText(actions)} ${attached ? "attached " : ""}${kindText(kind)}`,
		)
		.join("; ");
}

/**
 * One operation in plain words: `/pkg.Service/ListItems` (or `POST
 * /pkg.Service/ListItems`) reads "list items"; `GET /items/{id}` reads "get
 * items".
 */
export function operationText(operation: string): string {
	const [verb, route] = operation.includes(" ")
		? (operation.split(" ", 2) as [string, string])
		: ["", operation];
	const procedure = /^\/[\w.]+\/(\w+)$/.exec(route);
	if (procedure)
		return procedure[1].replace(/([a-z0-9])([A-Z])/g, "$1 $2").toLowerCase();
	const words = route
		.split("/")
		.filter((part) => part && !part.startsWith("{"))
		.map((part) => part.replace(/[_-]+/g, " "));
	return [verb.toLowerCase(), ...words].filter(Boolean).join(" ");
}

/** At most this many operations name a row that carries no scopes. */
const OPERATIONS_SHOWN = 4;

function operationsText(operations: readonly string[]): string {
	const named = [...new Set(operations.map(operationText))];
	return named.length > OPERATIONS_SHOWN
		? `${named.slice(0, OPERATIONS_SHOWN).join(", ")} and ${named.length - OPERATIONS_SHOWN} more`
		: listText(named);
}

/** What one row lets its holder do: its scopes, or else its operations. */
function rowLine(row: AbilityReach): string {
	const text = row.scopes.length
		? scopesText(row.scopes)
		: operationsText(row.operations) || "nothing yet";
	return row.confinement === "attached"
		? `${text}, only those attached to it`
		: text;
}

/** Where a row acts, as a heading, and the order it is read in. */
function whereOf(target: string, row: AbilityReach) {
	if (row.via === "binding") {
		const between = row.path.slice(1, -1);
		return {
			key: `binding:${row.path.join(">")}`,
			order: 1 + between.length,
			where: between.length
				? `Through ${target} and ${listText(between)}, in ${row.module}`
				: `Through ${target}, in ${row.module}`,
		};
	}
	return { key: "role", order: 0, where: `In ${target}` };
}

const OWN_ACCESS = "nothing more than they already have there themselves";

/**
 * What each role lets its holder do, grouped by where: "In <target>" (the
 * role on the target itself), "Through <target>, in <module>" (the target
 * acting for them; "Through <target> and <module>, in <module>" when it goes
 * on through another module), and "With their own access in <modules>" (edges
 * the target's page calls on the holder's own session, where their own grants
 * decide). A role whose offer carries no reach reads its own scopes on the
 * target.
 */
export function roleAbilities(
	target: string,
	roles: readonly AbilityRole[],
): RoleAbilities[] {
	return roles.map((role) => {
		const groups = new Map<
			string,
			AbilityGroup & { order: number; lines: string[] }
		>();
		const add = (
			place: { key: string; order: number; where: string },
			line: string,
		) => {
			const group = groups.get(place.key) ?? { ...place, lines: [] };
			if (line && !group.lines.includes(line)) group.lines.push(line);
			groups.set(place.key, group);
		};
		if (!role.reach.length && role.scopes.length)
			add(
				{ key: "role", order: 0, where: `In ${target}` },
				scopesText(role.scopes),
			);
		for (const row of role.reach)
			if (row.via !== "person") add(whereOf(target, row), rowLine(row));
		const own = [
			...new Set(
				role.reach.filter((row) => row.via === "person").map((r) => r.module),
			),
		].sort();
		if (own.length)
			add(
				{
					key: "person",
					order: 99,
					where: `With their own access in ${listText(own)}`,
				},
				OWN_ACCESS,
			);
		return {
			role: role.name,
			groups: [...groups.values()]
				.sort((a, b) => a.order - b.order || a.where.localeCompare(b.where))
				.map(({ key, where, lines }) => ({ key, where, lines })),
		};
	});
}

/** The kinds and actions of `now` that `was` does not hold. */
function beyondScopes(
	now: readonly AbilityScope[],
	was: readonly AbilityScope[],
): AbilityScope[] {
	const held = new Set(
		was.flatMap((s) => s.actions.map((a) => `${s.resourceKind}\u0000${a}`)),
	);
	return now
		.map((s) => ({
			...s,
			actions: s.actions.filter(
				(a) => !held.has(`${s.resourceKind}\u0000${a}`),
			),
		}))
		.filter((s) => s.actions.length > 0);
}

/**
 * What an update asks beyond the reach its installation's consent approved,
 * role by role and in the same words and places as `roleAbilities`: a role
 * the approval did not have, a place a role did not reach, what it can now do
 * where it already reached ("create collections"), and an operation newly
 * open to what it already held. Only roles and places that ask something are
 * returned; the authority decides whether an update is offered at all.
 */
export function updateAsks(
	target: string,
	now: readonly AbilityRole[],
	approved: readonly AbilityRole[],
): RoleAbilities[] {
	const same = (a: AbilityReach, b: AbilityReach) =>
		a.module === b.module && a.via === b.via && a.confinement === b.confinement;
	const out: RoleAbilities[] = [];
	for (const role of now) {
		const was = approved.find((r) => r.name === role.name);
		if (!was) {
			out.push(...roleAbilities(target, [role]));
			continue;
		}
		const asked: AbilityReach[] = [];
		for (const row of role.reach) {
			const before = was.reach.find((r) => same(r, row));
			if (!before) {
				asked.push(row);
				continue;
			}
			if (row.via === "person") continue;
			const scopes = beyondScopes(row.scopes, before.scopes);
			const operations = row.operations.filter(
				(op) => !before.operations.includes(op),
			);
			if (scopes.length) asked.push({ ...row, scopes, operations: [] });
			else if (operations.length)
				asked.push({ ...row, scopes: [], operations });
		}
		const [abilities] = roleAbilities(target, [
			{ name: role.name, scopes: [], reach: asked },
		]);
		if (abilities.groups.length) out.push(abilities);
	}
	return out;
}

const UNREACHED = /^role (\S+) reaches nothing of (.+) through (\S+): (.+)$/;

/**
 * One line naming who cannot reach what, from an installation's unreached
 * conditions: "Viewers can't reach model through assistant" (each kind's
 * module, from its prefix), for a status line; `unreachedText` says each in
 * full.
 */
export function unreachedHeadline(conditions: readonly string[]): string {
	const through = new Map<string, Set<string>>();
	const roles = new Set<string>();
	for (const condition of conditions) {
		const parsed = UNREACHED.exec(condition);
		if (!parsed) return "Some access can't be reached right now";
		roles.add(parsed[1]);
		const modules = through.get(parsed[3]) ?? new Set<string>();
		for (const pair of parsed[2].split(", ")) modules.add(pair.split(".")[0]);
		through.set(parsed[3], modules);
	}
	const who = listText([...roles].sort().map((role) => `${role}s`));
	return `${who[0].toUpperCase()}${who.slice(1)} can't reach ${listText(
		[...through].map(
			([binding, modules]) => `${listText([...modules])} through ${binding}`,
		),
	)}`;
}

/**
 * One unreached condition (`Installation.unreached`) in words: the core says
 * "role R reaches nothing of K:a, K:b through B: …"; this reads "A viewer can't
 * use profiles through robin right now". A condition in another form is shown
 * as the core wrote it.
 */
export function unreachedText(condition: string): string {
	const parsed = UNREACHED.exec(condition);
	if (!parsed) return condition;
	const [, role, scopes, binding, reason] = parsed;
	const kinds = scopes.split(", ").map((pair) => {
		const at = pair.lastIndexOf(":");
		return { resourceKind: pair.slice(0, at), actions: [pair.slice(at + 1)] };
	});
	const why = reason.endsWith("not composed")
		? `${reason.replace(/ not composed$/, "")} is not available here`
		: `${binding} no longer passes it on`;
	return `${article(role)} ${role} can't ${scopesText(kinds)} through ${binding} right now: ${why}.`;
}
