import { z } from "zod";
import type {
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

/**
 * The reach behind the plain words, for the technical disclosure: per role,
 * each place it reaches with its path and the operations' identifiers.
 */
export function reachDetail(offer: Pick<ReviewedOffer, "roles">) {
	return offer.roles.flatMap((role) =>
		role.reach.map(
			(row) =>
				`${role.name} — ${row.via} ${row.path.join(" → ")}${row.confinement ? ` (${row.confinement})` : ""}: ${row.operations.join(", ") || "no operation listed"}`,
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
