/** Clean domain types for the Onboarding feature. */

export type OnboardingStepId =
  | "create_org"
  | "invite_team"
  | "select_plan"
  | "create_api_key";

export interface OnboardingStep {
  id: OnboardingStepId;
  label: string;
  description: string;
  optional: boolean;
}

export interface OnboardingProgress {
  completedSteps: OnboardingStepId[];
  skippedSteps: OnboardingStepId[];
  currentStep: OnboardingStepId;
  startedAt: string;
}

export const ONBOARDING_STEPS: OnboardingStep[] = [
  {
    id: "create_org",
    label: "Create Organization",
    description: "Set up your organization to get started.",
    optional: false,
  },
  {
    id: "invite_team",
    label: "Invite Your Team",
    description: "Add team members to collaborate together.",
    optional: true,
  },
  {
    id: "select_plan",
    label: "Select a Plan",
    description: "Choose the plan that fits your needs.",
    optional: true,
  },
  {
    id: "create_api_key",
    label: "Create API Key",
    description: "Generate an API key to integrate with your tools.",
    optional: true,
  },
];
