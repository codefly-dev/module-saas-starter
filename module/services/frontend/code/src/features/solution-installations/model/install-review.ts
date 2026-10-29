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
		["namespaces", "Use storage spaces"],
		["external_namespaces", "Use external storage spaces"],
		["resources", "Use resource types"],
	] as const) {
		if (value[key]?.length) lines.push(`${label}: ${value[key].join(", ")}.`);
	}
	for (const bindings of [value.read_audiences, value.operation_audiences]) {
		for (const [binding, access] of Object.entries(bindings ?? {})) {
			if (access.delegation_mode)
				lines.push(
					`${access.audience} (${binding}): delegation mode ${access.delegation_mode.replaceAll("_", " ")}.`,
				);
			for (const [key, label] of Object.entries(operationLabels)) {
				for (const entry of access[key as keyof typeof operationLabels] ?? []) {
					const items = entry.resource_ids?.length
						? `items ${entry.resource_ids.join(", ")}`
						: "all items of this type";
					lines.push(
						`${label} through ${access.audience} (${binding}): ${(entry.actions ?? ["read"]).join(", ")} ${entry.resource_kind}; ${items}.`,
					);
				}
			}
		}
	}
	for (const [name, policy] of Object.entries(value.artifact_policies ?? {})) {
		// Retain the complete policy, including contract restrictions and digests.
		// These are read-only declared terms, never editable authority inputs.
		lines.push(`Run approved artifacts under ${name}: ${policyText(policy)}.`);
	}
	if (lines.length === 1 && value.capabilities)
		lines.push(
			"No additional jobs, storage spaces, resource types or cross-module operations are requested.",
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
