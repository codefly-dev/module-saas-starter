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

export const statusWords: Record<HostInstallationStatus, string> = {
	[HostInstallationStatus.UNSPECIFIED]: "Unknown",
	[HostInstallationStatus.PENDING]: "Pending",
	[HostInstallationStatus.ACTIVE]: "Installed",
	[HostInstallationStatus.REVOKED]: "Uninstalled",
};

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
