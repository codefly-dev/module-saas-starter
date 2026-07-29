import {
	OnboardingStepId,
	OnboardingStepStatus,
} from "@/gen/saas/accounts/v1/onboarding_pb";

export { OnboardingStepId, OnboardingStepStatus };

export type KnownOnboardingStepId = Exclude<
	OnboardingStepId,
	OnboardingStepId.UNSPECIFIED
>;

export interface OnboardingStep {
	id: KnownOnboardingStepId;
	label: string;
	description: string;
	required: boolean;
	status: OnboardingStepStatus;
	skipReason: string;
	completedAt?: string;
	skippedAt?: string;
}

export interface OnboardingProgress {
	organizationId: string;
	flowId: string;
	flowVersion: number;
	variant: string;
	steps: OnboardingStep[];
	currentStep: OnboardingStepId;
	nextStep: OnboardingStepId;
	requiredComplete: boolean;
	checklistComplete: boolean;
	activationAchieved: boolean;
	startedAt?: string;
	completedAt?: string;
	activatedAt?: string;
}

export const ONBOARDING_STEP_CONTENT: Record<
	KnownOnboardingStepId,
	{ label: string; description: string; href?: string }
> = {
	[OnboardingStepId.CONFIGURE_ORGANIZATION]: {
		label: "Configure organization",
		description: "Create the workspace where your team and product live.",
	},
	[OnboardingStepId.INVITE_TEAM]: {
		label: "Invite your team",
		description: "Send secure invitations from the organization admin page.",
		href: "/admin/invitations",
	},
	[OnboardingStepId.CHOOSE_PLAN]: {
		label: "Choose a plan",
		description: "Select a free or paid plan through the billing flow.",
		href: "/admin/billing",
	},
	[OnboardingStepId.SETUP_API_KEY]: {
		label: "Create an API key",
		description:
			"Create a key and reveal its secret once on the API keys page.",
		href: "/admin/api-keys",
	},
};
