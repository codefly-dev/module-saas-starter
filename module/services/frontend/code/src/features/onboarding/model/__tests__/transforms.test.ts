import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { describe, expect, it } from "vitest";
import {
	OnboardingProgressSchema,
	OnboardingStepId,
	OnboardingStepSchema,
	OnboardingStepStatus,
} from "@/gen/saas/accounts/v1/onboarding_pb";
import { transformOnboardingProgress } from "../transforms";

describe("transformOnboardingProgress", () => {
	it("maps the generated canonical contract without changing step ids", () => {
		const started = new Date("2026-07-28T10:00:00.000Z");
		const message = create(OnboardingProgressSchema, {
			organizationId: "00000000-0000-4000-8000-000000000001",
			flowId: "starter_activation",
			flowVersion: 1,
			variant: "default",
			currentStep: OnboardingStepId.INVITE_TEAM,
			nextStep: OnboardingStepId.CHOOSE_PLAN,
			requiredComplete: true,
			checklistComplete: false,
			startedAt: timestampFromDate(started),
			steps: [
				create(OnboardingStepSchema, {
					id: OnboardingStepId.CONFIGURE_ORGANIZATION,
					status: OnboardingStepStatus.COMPLETED,
					required: true,
					completedAt: timestampFromDate(started),
				}),
				create(OnboardingStepSchema, {
					id: OnboardingStepId.INVITE_TEAM,
					status: OnboardingStepStatus.PENDING,
				}),
			],
		});

		expect(transformOnboardingProgress(message)).toMatchObject({
			flowId: "starter_activation",
			flowVersion: 1,
			currentStep: OnboardingStepId.INVITE_TEAM,
			requiredComplete: true,
			checklistComplete: false,
			startedAt: started.toISOString(),
			steps: [
				{
					id: OnboardingStepId.CONFIGURE_ORGANIZATION,
					status: OnboardingStepStatus.COMPLETED,
					required: true,
				},
				{
					id: OnboardingStepId.INVITE_TEAM,
					status: OnboardingStepStatus.PENDING,
					required: false,
				},
			],
		});
	});

	it("fails closed on an unspecified server step", () => {
		const step = create(OnboardingStepSchema, {
			id: OnboardingStepId.UNSPECIFIED,
			status: OnboardingStepStatus.PENDING,
		});
		expect(() =>
			transformOnboardingProgress(
				create(OnboardingProgressSchema, { steps: [step] }),
			),
		).toThrow("unspecified step");
	});
});
