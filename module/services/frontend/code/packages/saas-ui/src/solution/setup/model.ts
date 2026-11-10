import {
	HostInstallationStatus,
	type OfferScope,
	type ReviewedOffer,
} from "@codefly-dev/saas-sdk";
import { Code, ConnectError } from "@connectrpc/connect";

/**
 * A condition the HOST knows about and the authority does not — an unmet
 * prerequisite of this solution's installation, in the words of whoever owns
 * it. A host that has none passes none; the authority's own answers are
 * rendered whatever the host adds.
 */
export interface SetupCondition {
	/** Stable across refreshes: the host's own key for the condition. */
	key: string;
	heading: string;
	subject?: string;
	detail: string;
	/** Who can clear it, as a sentence ("An organization administrator"). */
	clearedBy?: string;
	/**
	 * What clears it: a link to where it is cleared, an act performed here, or
	 * a note saying plainly that nothing here can.
	 */
	action?:
		| { kind: "link"; label: string; href: string }
		| { kind: "perform"; label: string; onPerform: () => Promise<unknown> }
		| { kind: "note"; label: string };
}

/**
 * A person or a team the host can name, offered when granting a role and used
 * to name a grant's holder. Optional: without it the page grants to an id and
 * names holders by id, which is what the authority itself knows.
 */
export interface SetupSubject {
	kind: "person" | "team";
	id: string;
	label: string;
	/** A short fact beside the name ("3 people"). */
	detail?: string;
	/** Where the host shows this person or team. */
	href?: string;
}

/** One fact on the setup's status line ("Healthy"), with its tone. */
export interface SetupFact {
	text: string;
	tone?: "success" | "warning" | "danger" | "info" | "neutral";
}

/**
 * What the authority said when it refused, as the person reads it: the
 * reason it gave, never a generic failure. A refusal that is not a Connect
 * error (the network, a programming error) says so in its own words.
 */
export function refusalOf(error: unknown): string {
	if (error instanceof ConnectError) {
		const reason = error.rawMessage || "no reason was given";
		return `${codeWords[error.code] ?? Code[error.code]}: ${reason}`;
	}
	return error instanceof Error ? error.message : String(error);
}

const codeWords: Partial<Record<Code, string>> = {
	[Code.PermissionDenied]: "Refused",
	[Code.Unauthenticated]: "Not signed in",
	[Code.NotFound]: "Not found",
	[Code.FailedPrecondition]: "Not now",
	[Code.InvalidArgument]: "Invalid",
	[Code.Unavailable]: "Unavailable",
	[Code.Aborted]: "Changed meanwhile",
};

/** Whether a refusal is the authority denying this person, rather than a failure. */
export function isDenied(error: unknown): boolean {
	return (
		error instanceof ConnectError &&
		(error.code === Code.PermissionDenied ||
			error.code === Code.Unauthenticated)
	);
}

/** Whether the offer carries a live installation (pending or active). */
export function installed(offer: ReviewedOffer | undefined): boolean {
	const status = offer?.installation?.status;
	return (
		status === HostInstallationStatus.ACTIVE ||
		status === HostInstallationStatus.PENDING
	);
}

/**
 * One scope as a sentence. A role's own scopes are confined to what its
 * installation owns, and each says so; an operation's requirement or a
 * binding's ceiling is not confined.
 */
export function scopeText(s: OfferScope, confined: boolean): string {
	const actions = `${s.resourceKind}: ${s.actions.join(", ")}`;
	if (!confined) return actions;
	const reach = s.attachedOnly
		? s.resourceIds.length
			? `only ${s.resourceIds.join(", ")}`
			: "only resources attached to the installation (none yet)"
		: "what this installation owns";
	return `${actions} · ${reach}`;
}

/**
 * The access one operation demands, as one label.
 *
 * `read` is the floor, and the only action that changes nothing — a
 * distinction this contract already draws elsewhere, where a tool carries
 * `write` to say it changes data. So an operation demanding any other action
 * is named by those actions, and only a wholly read-only one is named `read`.
 * Naming an operation by every action it demands instead would file a write
 * that also reads under "read, write" and split the write count over two
 * buckets, hiding the number a reader came for: whether a role's breadth is
 * forty reads or eleven writes.
 *
 * No severity order is invented among the other verbs — `write`, `publish`,
 * `create` and whatever a module declares next are listed as the contract
 * spells them, sorted only so the label is stable.
 */
export function accessLevel(requires: readonly OfferScope[]): string {
	const actions = [...new Set(requires.flatMap((s) => s.actions))].sort();
	const changing = actions.filter((action) => action !== "read");
	if (changing.length > 0) return changing.join(", ");
	if (actions.length > 0) return "read";
	// An operation the authority says needs no scope at all is a real answer,
	// and not a read.
	return "no scope";
}

/** Access labels in reading order: the read floor first, the unscoped last. */
export function byAccess(a: string, b: string): number {
	const rank = (label: string) =>
		label === "read" ? 0 : label === "no scope" ? 2 : 1;
	return rank(a) - rank(b) || a.localeCompare(b);
}

/**
 * How a set of operations divides by the access it demands, counted and
 * ordered: `41 read, 11 write`. This is what a module's summary line says,
 * because "requires documents: read" understated a module whose fifty-two
 * operations include eleven writes.
 */
export function accessMix(
	requires: readonly (readonly OfferScope[])[],
): string {
	const counts = new Map<string, number>();
	for (const operation of requires) {
		const level = accessLevel(operation);
		counts.set(level, (counts.get(level) ?? 0) + 1);
	}
	return [...counts.entries()]
		.sort(([a], [b]) => byAccess(a, b))
		.map(([level, count]) => `${count} ${level}`)
		.join(", ");
}

/** A subject's key as the grant picker spells it. */
export function subjectKey(kind: "person" | "team", id: string): string {
	return `${kind}:${id}`;
}

/** The holder of a grant, named as the host names it, else by id. */
export function holderName(
	grant: { personId: string; teamId: string },
	subjects: readonly SetupSubject[] | undefined,
): string {
	const kind = grant.personId ? "person" : "team";
	const id = grant.personId || grant.teamId;
	const named = subjects?.find((s) => s.kind === kind && s.id === id);
	if (named) return kind === "team" ? `Team ${named.label}` : named.label;
	return kind === "team" ? `Team ${id}` : id;
}
