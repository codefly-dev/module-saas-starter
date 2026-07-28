export const capabilityStates = [
	"absent",
	"implemented",
	"configured",
	"operationally_verified",
	"externally_attested",
] as const;

export type CapabilityState = (typeof capabilityStates)[number];
export type CapabilityResponsibility =
	| "starter"
	| "provider"
	| "adopter"
	| "shared";
export type EvidenceStatus = "current" | "expired" | "revoked" | "rejected";

export interface CapabilityEvidence {
	id: string;
	capabilityId: string;
	environment: string;
	scope: string;
	owner: string;
	verifier: string;
	source: string;
	performedAt: string;
	expiresAt?: string;
	reviewAt: string;
	status: EvidenceStatus;
	state: "operationally_verified" | "externally_attested";
	visibility: "private" | "public_summary";
	publicSummary?: string;
}

export interface CapabilityDefinition {
	id: string;
	category: string;
	title: string;
	designState: "absent" | "implemented";
	responsibility: CapabilityResponsibility;
	configuration: {
		providers: string[];
		settings: string[];
	};
	public: {
		minimumState: CapabilityState;
		summary: string;
	};
}

export interface CapabilityManifest {
	schemaVersion: number;
	capabilities: CapabilityDefinition[];
}

export interface CapabilityContextDocument {
	schemaVersion: number;
	environment: string;
	scope: string;
	configuredProviders: string[];
	configuredSettings: string[];
	evidence: CapabilityEvidence[];
}

export interface CapabilityContext {
	environment: string;
	scope: string;
	configuredProviders: string[];
	configuredSettings: string[];
	evidence: CapabilityEvidence[];
	now: Date;
}

export interface PublicCapability {
	id: string;
	category: string;
	title: string;
	responsibility: CapabilityResponsibility;
	state: CapabilityState;
	label: string;
	summary?: string;
	evidenceSummary?: string;
}

export function starterDefaultCapabilityContext(now: Date): CapabilityContext {
	return {
		environment: "starter-default",
		scope: "Unconfigured starter distribution",
		configuredProviders: [],
		configuredSettings: [],
		evidence: [],
		now,
	};
}

export function capabilityStateLabel(state: CapabilityState): string {
	switch (state) {
		case "absent":
			return "Adopter action required";
		case "implemented":
			return "Starter implementation";
		case "configured":
			return "Configured, not verified";
		case "operationally_verified":
			return "Operationally verified";
		case "externally_attested":
			return "Externally attested";
	}
}

export function effectiveCapabilityState(
	capability: CapabilityDefinition,
	context: CapabilityContext,
): CapabilityState {
	let state: CapabilityState = capability.designState;
	const hasConfiguration =
		capability.configuration.providers.every((provider) =>
			context.configuredProviders.includes(provider),
		) &&
		capability.configuration.settings.every((setting) =>
			context.configuredSettings.includes(setting),
		);
	const requiresConfiguration =
		capability.configuration.providers.length > 0 ||
		capability.configuration.settings.length > 0;

	if (requiresConfiguration && hasConfiguration) {
		state = "configured";
	}

	const evidence = context.evidence
		.filter(
			(record) =>
				record.capabilityId === capability.id &&
				record.environment === context.environment &&
				record.scope === context.scope &&
				evidenceIsCurrent(record, context.now),
		)
		.sort((left, right) => rank(right.state) - rank(left.state));

	if (
		evidence.length > 0 &&
		(!requiresConfiguration || hasConfiguration) &&
		rank(evidence[0].state) > rank(state)
	) {
		state = evidence[0].state;
	}
	return state;
}

export function publicCapabilities(
	manifest: CapabilityManifest,
	context: CapabilityContext,
): PublicCapability[] {
	return manifest.capabilities.map((capability) => {
		const state = effectiveCapabilityState(capability, context);
		const supportingEvidence = context.evidence.find(
			(record) =>
				record.capabilityId === capability.id &&
				record.environment === context.environment &&
				record.scope === context.scope &&
				record.visibility === "public_summary" &&
				evidenceIsCurrent(record, context.now) &&
				record.state === state,
		);
		return {
			id: capability.id,
			category: capability.category,
			title: capability.title,
			responsibility: capability.responsibility,
			state,
			label: capabilityStateLabel(state),
			summary:
				rank(state) >= rank(capability.public.minimumState)
					? capability.public.summary
					: undefined,
			evidenceSummary: supportingEvidence?.publicSummary,
		};
	});
}

export function capabilityStateAtLeast(
	state: CapabilityState,
	minimum: CapabilityState,
): boolean {
	return rank(state) >= rank(minimum);
}

function rank(state: CapabilityState): number {
	return capabilityStates.indexOf(state);
}

function evidenceIsCurrent(evidence: CapabilityEvidence, now: Date): boolean {
	if (evidence.status !== "current") return false;
	const performedAt = Date.parse(evidence.performedAt);
	const reviewAt = Date.parse(evidence.reviewAt);
	const expiresAt = evidence.expiresAt
		? Date.parse(evidence.expiresAt)
		: Number.POSITIVE_INFINITY;
	return (
		Number.isFinite(performedAt) &&
		performedAt <= now.getTime() &&
		Number.isFinite(reviewAt) &&
		reviewAt > now.getTime() &&
		expiresAt > now.getTime()
	);
}
