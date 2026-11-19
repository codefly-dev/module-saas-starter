import { Code, ConnectError } from "@connectrpc/connect";

/**
 * The document the next revision starts from: the current ceiling with its
 * revision advanced, or an empty ceiling when none has been written. Nothing is
 * added for the administrator — which principals to allow, and how far, is the
 * decision this page exists for.
 */
export function nextRevisionDraft(
	document: string | undefined,
	revision: bigint,
): string {
	const next = Number(revision) + 1;
	if (document) {
		try {
			const parsed: unknown = JSON.parse(document);
			if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
				return JSON.stringify({ ...parsed, revision: next }, null, 2);
			}
		} catch {
			// A stored document the browser cannot read is shown as written.
		}
		return document;
	}
	return JSON.stringify({ revision: next, principals: {} }, null, 2);
}

type Json = Record<string, unknown>;

function asObject(value: unknown): Json | null {
	return value && typeof value === "object" && !Array.isArray(value)
		? (value as Json)
		: null;
}

function parseDocument(document: string | undefined): Json | null {
	if (!document) return null;
	try {
		return asObject(JSON.parse(document));
	} catch {
		return null;
	}
}

/** The modules a permissions document approves, keyed by module name. */
function approvedModules(document: string | undefined): Json | null {
	const parsed = parseDocument(document);
	if (!parsed) return null;
	return asObject(parsed.principals) ?? {};
}

/**
 * What is wrong with a draft before it is sent, or null when it can be sent.
 * Only what the browser can know: whether it reads as a document at all. What
 * the document may contain is the platform's to judge when it is saved.
 */
export function draftProblem(text: string): string | null {
	if (text.trim() === "")
		return "Enter the permissions to approve. This can't be empty.";
	try {
		if (asObject(JSON.parse(text))) return null;
	} catch {
		return "This isn't valid JSON. Check for a missing comma, quote or bracket.";
	}
	return "The permissions must be one JSON object, starting with { and ending with }.";
}

/** Which modules a save would newly approve, revoke, or change. */
export interface DraftChanges {
	approved: string[];
	revoked: string[];
	changed: string[];
}

/**
 * What saving `draft` over `current` does to each module, by name. A document
 * that cannot be read on either side yields no names: the question is then
 * asked without them rather than with a guess.
 */
export function draftChanges(
	current: string | undefined,
	draft: string,
): DraftChanges {
	const before = current ? approvedModules(current) : {};
	const after = approvedModules(draft);
	const changes: DraftChanges = { approved: [], revoked: [], changed: [] };
	if (!before || !after) return changes;
	for (const name of Object.keys(after).sort()) {
		if (!(name in before)) changes.approved.push(name);
		else if (JSON.stringify(before[name]) !== JSON.stringify(after[name]))
			changes.changed.push(name);
	}
	for (const name of Object.keys(before).sort()) {
		if (!(name in after)) changes.revoked.push(name);
	}
	return changes;
}

/**
 * The current document with one module's approval taken out and the revision
 * advanced, or null when there is nothing to take out: no document, one the
 * browser cannot read, or one that never approved that module.
 */
export function withoutModule(
	document: string | undefined,
	revision: bigint,
	module: string,
): string | null {
	const parsed = parseDocument(document);
	const approved = parsed && asObject(parsed.principals);
	if (!parsed || !approved || !(module in approved)) return null;
	const { [module]: _revoked, ...kept } = approved;
	return JSON.stringify(
		{ ...parsed, revision: Number(revision) + 1, principals: kept },
		null,
		2,
	);
}

function count(value: unknown): number {
	if (Array.isArray(value)) return value.length;
	const object = asObject(value);
	return object ? Object.keys(object).length : 0;
}

const plural = (n: number, one: string, many: string) =>
	`${n} ${n === 1 ? one : many}`;

/**
 * What a module asks for, in a line per kind of permission. The request itself
 * is a technical document; an operator deciding whether to approve needs its
 * size and shape, and reads the document itself under "Technical details".
 */
export function requestSummary(authority: string): string[] {
	const request = parseDocument(authority);
	const lines: string[] = [];
	if (request) {
		const operations = count(request.bindings);
		const queues = count(request.queues);
		const areas = count(request.namespaces);
		const kinds = count(request.resource_kinds);
		if (operations > 0)
			lines.push(
				`Act for organizations in ${plural(operations, "way", "ways")}`,
			);
		if (queues > 0)
			lines.push(`Run background work on ${plural(queues, "queue", "queues")}`);
		if (areas > 0)
			lines.push(
				`Keep its own data in ${plural(areas, "storage area", "storage areas")}`,
			);
		if (kinds > 0)
			lines.push(
				`Manage ${plural(kinds, "kind of record", "kinds of record")}`,
			);
	}
	return lines.length > 0
		? lines
		: ["The full request is under Technical details."];
}

/**
 * Why a save was refused, in words a platform operator can act on. Chosen by
 * the kind of refusal alone: the platform's own message names internals and is
 * never shown.
 */
export function writeRefusal(error: unknown): string {
	const refused = ConnectError.from(error);
	switch (refused.code) {
		case Code.FailedPrecondition:
			if (refused.rawMessage === "mfa_required") {
				return "Saving needs your passkey confirmed within the last 15 minutes. Confirm it (the sign-in page brings you back here), then save again.";
			}
			return "The platform can't accept module permissions right now. Nothing was saved. Tell the team that runs this platform.";
		case Code.Aborted:
			return "Another operator saved a newer version first. Nothing was saved: discard any changes you were making, review their version, and decide again.";
		case Code.PermissionDenied:
			return "Only a platform super administrator can change module permissions. Ask one to make this change or to give you that role.";
		case Code.InvalidArgument:
			return "The platform didn't accept these permissions, so nothing was saved. Check the document against the format under Technical details: a module's entry may hold only its permissions.";
		case Code.Unavailable:
		case Code.DeadlineExceeded:
			return "The platform didn't answer in time. Nothing was saved. Try again in a moment.";
		default:
			return "The permissions couldn't be saved, and nothing changed. Try again; if it keeps failing, tell the team that runs this platform.";
	}
}
