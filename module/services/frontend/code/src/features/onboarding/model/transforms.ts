import { timestampDate } from "@bufbuild/protobuf/wkt";
import type {
	OnboardingProgress as OnboardingProgressMessage,
	OnboardingStep as OnboardingStepMessage,
} from "@/gen/saas/accounts/v1/onboarding_pb";
import {
	ONBOARDING_STEP_CONTENT,
	type OnboardingProgress,
	type OnboardingStep,
	OnboardingStepId,
} from "./types";

function timestamp(
	value: OnboardingStepMessage["completedAt"],
): string | undefined {
	return value ? timestampDate(value).toISOString() : undefined;
}

export function transformOnboardingStep(
	message: OnboardingStepMessage,
): OnboardingStep {
	if (message.id === OnboardingStepId.UNSPECIFIED) {
		throw new Error("Onboarding response contains an unspecified step");
	}
	const content = ONBOARDING_STEP_CONTENT[message.id];
	if (!content) {
		throw new Error(`Onboarding response contains unknown step ${message.id}`);
	}
	return {
		id: message.id as OnboardingStep["id"],
		label: content.label,
		description: content.description,
		required: message.required,
		status: message.status,
		skipReason: message.skipReason,
		completedAt: timestamp(message.completedAt),
		skippedAt: timestamp(message.skippedAt),
	};
}

export function transformOnboardingProgress(
	message: OnboardingProgressMessage,
): OnboardingProgress {
	return {
		organizationId: message.organizationId,
		flowId: message.flowId,
		flowVersion: message.flowVersion,
		variant: message.variant,
		steps: message.steps.map(transformOnboardingStep),
		currentStep: message.currentStep,
		nextStep: message.nextStep,
		requiredComplete: message.requiredComplete,
		checklistComplete: message.checklistComplete,
		activationAchieved: message.activationAchieved,
		startedAt: timestamp(message.startedAt),
		completedAt: timestamp(message.completedAt),
		activatedAt: timestamp(message.activatedAt),
	};
}
