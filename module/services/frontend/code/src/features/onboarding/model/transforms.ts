import type { OnboardingProgress, OnboardingStepId } from "./types";
import { ONBOARDING_STEPS } from "./types";

/** Lucide icon name for each onboarding step. */
export function getStepIcon(stepId: OnboardingStepId): string {
	switch (stepId) {
		case "create_org":
			return "Building2";
		case "invite_team":
			return "UsersRound";
		case "select_plan":
			return "CreditCard";
		case "create_api_key":
			return "Key";
		default:
			return "Circle";
	}
}

export function getStepDescription(stepId: OnboardingStepId): string {
	const step = ONBOARDING_STEPS.find((s) => s.id === stepId);
	return step?.description ?? "";
}

export function isWizardComplete(progress: OnboardingProgress | null): boolean {
	if (!progress) return false;
	return ONBOARDING_STEPS.every(
		(step) =>
			progress.completedSteps.includes(step.id) ||
			progress.skippedSteps.includes(step.id),
	);
}
