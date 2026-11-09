import { z } from "zod";
import type {
	OfferRole,
	OfferScope,
	ReviewedOffer,
} from "@/gen/host/installation/v1/installation_pb";
import type { ReviewedSolutionInstall } from "@/gen/saas/accounts/v1/installations_pb";

/** Presentation must describe the same signed offer as the combined consent. */
export function assertMatchingOffer(
	review: ReviewedSolutionInstall,
	offer: ReviewedOffer,
) {
	const a = review.approval;
	const b = offer.approval;
	if (
		!a ||
		!b ||
		review.target !== offer.target ||
		review.installationRole !== offer.installationRole ||
		a.targetId !== b.targetId ||
		a.generation !== b.generation ||
		a.digest !== b.digest ||
		a.expectedRevision !== b.expectedRevision
	) {
		throw new Error(
			"The solution changed while preparing this review. Review the current access again.",
		);
	}
	if (
		review.installationRole !== "none" &&
		!offer.roles.some((role) => role.name === review.installationRole)
	) {
		throw new Error(
			"The solution's declared installation access is unavailable. Its owner must correct the declaration.",
		);
	}
}

export function offerScopeText(scope: OfferScope): string {
	const boundary = scope.attachedOnly
		? scope.resourceIds.length
			? `attached items: ${scope.resourceIds.join(", ")}`
			: "attached items only; none are attached yet"
		: "items owned by this installation";
	return `${scope.actions.join(", ")} ${scope.resourceKind} — ${boundary}`;
}

const scope = z
	.object({
		resource_kind: z.string(),
		actions: z.array(z.string()).optional(),
		resource_ids: z.array(z.string()).optional(),
	})
	.strict();
const audience = z
	.object({
		audience: z.string(),
		scopes: z.array(scope).nullish(),
		delegation_mode: z.string().optional(),
		invoke_scopes: z.array(scope).nullish(),
		lookup_scopes: z.array(scope).nullish(),
		headless_scopes: z.array(scope).nullish(),
		source_delegation_scopes: z.array(scope).nullish(),
	})
	.strict();
const consent = z
	.object({
		capabilities: z.boolean(),
		follows: z.boolean().optional(),
		queues: z.array(z.string()).optional(),
		namespaces: z.array(z.string()).optional(),
		external_namespaces: z.array(z.string()).optional(),
		resources: z.array(z.string()).optional(),
		read_audiences: z.record(z.string(), audience).optional(),
		operation_audiences: z.record(z.string(), audience).optional(),
		artifact_policies: z.record(z.string(), z.unknown()).optional(),
	})
	.strict();

const operationLabels = {
	scopes: "Read",
	invoke_scopes: "Perform operations",
	lookup_scopes: "Look up information",
	headless_scopes: "Run background operations",
	source_delegation_scopes: "Delegate source access",
} as const;

/** Render every consent axis; never turn unreadable access into an empty review. */
export function moduleConsentText(raw: string): string[] {
	const parsed = consent.safeParse(JSON.parse(raw));
	if (!parsed.success)
		throw new Error(
			"Supporting module access cannot be displayed safely. Ask its owner to update the access declaration.",
		);
	const value = parsed.data;
	const lines = [
		value.capabilities
			? "Use the host features declared below for this organization."
			: "No host feature access.",
	];
	if (value.follows)
		lines.push(
			"Access follows the existing administrative policy as that policy changes.",
		);
	for (const [key, label] of [
		["queues", "Run jobs in"],
		["namespaces", "Publish events in namespaces"],
		[
			"external_namespaces",
			"Namespaces whose declared external events may be delivered to this organization's configured outbound webhook",
		],
		["resources", "Use resource types"],
	] as const) {
		if (value[key]?.length) lines.push(`${label}: ${value[key].join(", ")}.`);
	}
	// One line per audience, axis, resource kind and items, actions merged:
	// an operation reached on two faces (Connect and gRPC) is one capability,
	// and a binding's own identifier is never shown. An audience whose faces
	// act in one delegation mode says it once; one whose faces differ says
	// each face's mode on its own lines.
	const operations = new Map<
		string,
		{ head: string; tail: string; actions: Set<string> }
	>();
	const faces = [
		...Object.values(value.read_audiences ?? {}),
		...Object.values(value.operation_audiences ?? {}),
	];
	const modesAt = new Map<string, Set<string>>();
	for (const access of faces) {
		const modes = modesAt.get(access.audience) ?? new Set<string>();
		modes.add(access.delegation_mode ?? "");
		modesAt.set(access.audience, modes);
	}
	const modeText = (mode: string) =>
		`delegation mode ${mode.replaceAll("_", " ")}`;
	const modes = new Set<string>();
	for (const [audience, held] of modesAt) {
		const [mode] = held;
		if (held.size === 1 && mode)
			modes.add(`Acts at ${audience}: ${modeText(mode)}.`);
	}
	for (const access of faces) {
		const perFace = (modesAt.get(access.audience)?.size ?? 0) > 1;
		const mode = perFace
			? ` (${access.delegation_mode ? modeText(access.delegation_mode) : "no delegation mode"})`
			: "";
		for (const [key, label] of Object.entries(operationLabels)) {
			for (const entry of access[key as keyof typeof operationLabels] ?? []) {
				const items = entry.resource_ids?.length
					? `items ${entry.resource_ids.join(", ")}`
					: "all items of this type";
				const id = [
					access.audience,
					mode,
					key,
					entry.resource_kind,
					items,
				].join("\u0000");
				const line = operations.get(id) ?? {
					head: `${label} through ${access.audience}${mode}: `,
					tail: ` ${entry.resource_kind}; ${items}.`,
					actions: new Set<string>(),
				};
				for (const action of entry.actions ?? ["read"])
					line.actions.add(action);
				operations.set(id, line);
			}
		}
	}
	lines.push(...[...modes].sort());
	lines.push(
		...[...operations.values()]
			.map(
				({ head, tail, actions }) => `${head}${[...actions].join(", ")}${tail}`,
			)
			.sort(),
	);
	for (const [name, policy] of Object.entries(value.artifact_policies ?? {})) {
		// Retain the complete policy, including contract restrictions and digests.
		// These are read-only declared terms, never editable authority inputs.
		lines.push(`Run approved artifacts under ${name}: ${policyText(policy)}.`);
	}
	if (lines.length === 1 && value.capabilities)
		lines.push(
			"No additional jobs, event publication, external event delivery, resource types or cross-module operations are requested.",
		);
	return lines;
}

function policyText(value: unknown): string {
	if (Array.isArray(value))
		return value.length ? value.map(policyText).join("; ") : "none";
	if (value !== null && typeof value === "object")
		return Object.entries(value)
			.map(([key, item]) => `${key.replaceAll("_", " ")}: ${policyText(item)}`)
			.join("; ");
	return String(value);
}

const kindText = (kind: string) => {
	const name = (kind.split(".").pop() ?? kind).replace(/[_-]+/g, " ");
	return name.endsWith("s") ? name : `${name}s`;
};

const listText = (words: readonly string[]) =>
	words.length < 2
		? (words[0] ?? "")
		: `${words.slice(0, -1).join(", ")} and ${words[words.length - 1]}`;

/**
 * What a role's scopes let its holder do, in plain words: one phrase per
 * resource kind with its actions merged ("read and write pages"), never an
 * operation or a binding.
 */
export function scopesText(scopes: readonly OfferScope[]): string {
	const kinds = new Map<string, { actions: Set<string>; attached: boolean }>();
	for (const scope of scopes) {
		const entry = kinds.get(scope.resourceKind) ?? {
			actions: new Set<string>(),
			attached: true,
		};
		for (const action of scope.actions) entry.actions.add(action);
		entry.attached &&= scope.attachedOnly;
		kinds.set(scope.resourceKind, entry);
	}
	return (
		[...kinds]
			.map(
				([kind, { actions, attached }]) =>
					`${listText([...actions])} ${attached ? "attached " : ""}${kindText(kind)}`,
			)
			.join("; ") || "no data access"
	);
}

/**
 * One operation in plain words: a procedure `/pkg.Service/ListItems` (or its
 * Connect route `POST /pkg.Service/ListItems`) reads "list items"; a route
 * `GET /items/{id}` reads "get items". Never an identifier.
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

type ReachRow = OfferRole["reach"][number];

/** At most this many operations name a row that carries no scopes. */
const OPERATIONS_SHOWN = 5;

/**
 * What one reach row lets its holder do: its scopes in plain words ("read and
 * write pages"), which is what the role grants there. A target whose
 * operations carry no policy of their own lets every role reach every one of
 * them, so its operations say nothing about the role; they are shown only
 * behind the technical disclosure (reachDetail). A row with no scopes names
 * a few of its operations instead.
 */
function rowText(row: ReachRow) {
	const operations = [...new Set(row.operations.map(operationText))];
	const named =
		operations.length > OPERATIONS_SHOWN
			? `${operations.slice(0, OPERATIONS_SHOWN).join(", ")} and ${operations.length - OPERATIONS_SHOWN} more`
			: listText(operations);
	const text = row.scopes.length
		? scopesText(row.scopes)
		: named || "no data access";
	const confinement: Record<string, string> = {
		attached: "attached items",
		installation: "this installation's items",
		owner: "items they own",
	};
	return confinement[row.confinement]
		? `${text} (${confinement[row.confinement]})`
		: text;
}

/**
 * What each declared role lets people do, in plain words, grouped by where:
 * "In <target>" (the role on the target itself), "Through <target>, in
 * <module>" (the target acting for the holder, directly or transitively),
 * and "Uses your own access in <modules>" (edges the target's page calls on
 * the holder's own session, where their own grants decide: one line for
 * every such module). The rows are the core's reach (OfferRole.reach); a
 * role whose offer carries none reads its own scopes on the target. A group
 * where every role reads the same says it once, for every role.
 */
export function roleReach(offer: Pick<ReviewedOffer, "roles" | "target">) {
	const groups = new Map<
		string,
		{ order: number; roles: Map<string, string[]> }
	>();
	const add = (where: string, order: number, role: string, text: string) => {
		const group = groups.get(where) ?? { order, roles: new Map() };
		const texts = group.roles.get(role) ?? [];
		if (!texts.includes(text)) texts.push(text);
		group.roles.set(role, texts);
		groups.set(where, group);
	};
	const ownAccess = [
		...new Set(
			offer.roles.flatMap((role) =>
				role.reach
					.filter((row) => row.via === "person")
					.map((row) => row.module),
			),
		),
	].sort();
	const ownWhere = `Uses your own access in ${listText(ownAccess)}`;
	for (const role of offer.roles) {
		if (!role.reach.length) {
			add(`In ${offer.target}`, 0, role.name, scopesText(role.scopes));
			continue;
		}
		for (const row of role.reach) {
			if (row.via === "person")
				add(ownWhere, 2, role.name, "what you hold there yourself");
			else if (row.via === "binding")
				add(
					`Through ${offer.target}, in ${row.module}`,
					1,
					role.name,
					rowText(row),
				);
			else add(`In ${offer.target}`, 0, role.name, rowText(row));
		}
	}
	return [...groups]
		.sort(([a, x], [b, y]) => x.order - y.order || a.localeCompare(b))
		.map(([where, { roles }]) => {
			const entries = [...roles].map(([role, texts]) => ({
				role,
				text: texts.join("; "),
			}));
			const shared =
				entries.length > 1 &&
				entries.length === offer.roles.length &&
				entries.every((entry) => entry.text === entries[0].text);
			return {
				where,
				roles: shared
					? [{ role: "every role", text: entries[0].text }]
					: entries,
			};
		});
}

/**
 * The reach behind the plain words, for the technical disclosure: per role,
 * each place it reaches with its path and the operations' identifiers.
 */
export function reachDetail(offer: Pick<ReviewedOffer, "roles">) {
	return offer.roles.flatMap((role) =>
		role.reach.map(
			(row) =>
				`${role.name} — ${row.via} ${row.path.join(" → ")}${row.confinement ? ` (${row.confinement})` : ""}: ${row.operations.join(", ") || "no operation of its own"}`,
		),
	);
}

/** The supporting modules of a review, as the one-line summaries it shows. */
export function supportingSummary(
	modules: readonly {
		principal: string;
		target: string;
		required: boolean;
		reuseInstallationId: string;
		installationRole: string;
	}[],
) {
	const name = (m: (typeof modules)[number]) => m.target || m.principal;
	const installs = modules.filter((m) => m.required && !m.reuseInstallationId);
	return {
		installs: installs.map(name),
		/** Each fresh module that holds a standing machine role, with the role. */
		standing: installs
			.filter((m) => m.installationRole && m.installationRole !== "none")
			.map((m) => `${name(m)} (${m.installationRole})`),
		reused: modules
			.filter((m) => m.required && m.reuseInstallationId)
			.map(name),
		optional: modules.filter((m) => !m.required).map(name),
	};
}
