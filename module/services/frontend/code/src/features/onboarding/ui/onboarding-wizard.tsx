"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Building2,
  UsersRound,
  CreditCard,
  Key,
  Check,
  SkipForward,
  PartyPopper,
  type LucideIcon,
} from "lucide-react";
import { toast } from "sonner";
import {
  Button,
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
  Input,
  Label,
} from "@/shared/ui";
import { onboardingQueries } from "../service/queries";
import { onboardingMutations } from "../service/mutations";
import {
  ONBOARDING_STEPS,
  type OnboardingStepId,
  type OnboardingProgress,
} from "../model/types";
import { isWizardComplete } from "../model/transforms";

const stepIcons: Record<OnboardingStepId, LucideIcon> = {
  create_org: Building2,
  invite_team: UsersRound,
  select_plan: CreditCard,
  create_api_key: Key,
};

export function OnboardingWizard() {
  const queryClient = useQueryClient();
  const { data } = useQuery(onboardingQueries.progress());
  const progress = data as OnboardingProgress | undefined;

  const completeMutation = useMutation({
    mutationFn: (stepId: string) => onboardingMutations.completeStep(stepId),
    onSuccess: () => {
      toast.success("Step completed!");
      queryClient.invalidateQueries({ queryKey: ["onboarding"] });
    },
    onError: () => toast.error("Failed to complete step"),
  });

  const skipMutation = useMutation({
    mutationFn: (stepId: string) => onboardingMutations.skipStep(stepId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["onboarding"] });
    },
    onError: () => toast.error("Failed to skip step"),
  });

  if (!progress) return null;

  const complete = isWizardComplete(progress);

  if (complete) {
    return (
      <div className="fixed inset-0 z-50 flex items-center justify-center bg-background/95 backdrop-blur-sm">
        <Card className="w-full max-w-md text-center">
          <CardHeader>
            <div className="mx-auto mb-4 flex h-16 w-16 items-center justify-center rounded-full bg-primary/10">
              <PartyPopper className="h-8 w-8 text-primary" />
            </div>
            <CardTitle className="text-2xl">You are all set!</CardTitle>
            <CardDescription>
              Your workspace is configured and ready to go.
            </CardDescription>
          </CardHeader>
          <CardFooter className="justify-center">
            <Button
              size="lg"
              onClick={() =>
                queryClient.setQueryData(["onboarding", "dismissed"], true)
              }
            >
              Go to Dashboard
            </Button>
          </CardFooter>
        </Card>
      </div>
    );
  }

  const currentStepIndex = ONBOARDING_STEPS.findIndex(
    (s) => s.id === progress.currentStep,
  );
  const currentStep = ONBOARDING_STEPS[currentStepIndex];

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-background/95 backdrop-blur-sm">
      <div className="w-full max-w-lg space-y-6 p-6">
        {/* Step indicator */}
        <div className="flex items-center justify-center gap-3">
          {ONBOARDING_STEPS.map((step, i) => {
            const done =
              progress.completedSteps.includes(step.id) ||
              progress.skippedSteps.includes(step.id);
            const isCurrent = step.id === progress.currentStep;
            return (
              <div key={step.id} className="flex items-center gap-3">
                <div className="flex flex-col items-center gap-1">
                  <div
                    className={`flex h-8 w-8 items-center justify-center rounded-full border-2 text-sm font-medium transition-colors ${
                      done
                        ? "border-primary bg-primary text-primary-foreground"
                        : isCurrent
                          ? "border-primary text-primary"
                          : "border-muted text-muted-foreground"
                    }`}
                  >
                    {done ? <Check className="h-4 w-4" /> : i + 1}
                  </div>
                  <span className="text-xs text-muted-foreground whitespace-nowrap">
                    {step.label}
                  </span>
                </div>
                {i < ONBOARDING_STEPS.length - 1 && (
                  <div
                    className={`h-0.5 w-8 ${
                      done ? "bg-primary" : "bg-muted"
                    }`}
                  />
                )}
              </div>
            );
          })}
        </div>

        {/* Current step card */}
        {currentStep && (
          <Card>
            <CardHeader>
              <div className="flex items-center gap-3">
                {(() => {
                  const Icon = stepIcons[currentStep.id];
                  return Icon ? <Icon className="h-5 w-5 text-primary" /> : null;
                })()}
                <div>
                  <CardTitle>{currentStep.label}</CardTitle>
                  <CardDescription>{currentStep.description}</CardDescription>
                </div>
              </div>
            </CardHeader>
            <CardContent>
              <StepContent stepId={currentStep.id} />
            </CardContent>
            <CardFooter className="flex justify-between">
              {currentStep.optional && (
                <Button
                  variant="ghost"
                  onClick={() => skipMutation.mutate(currentStep.id)}
                  disabled={skipMutation.isPending}
                >
                  <SkipForward className="mr-2 h-4 w-4" />
                  Skip
                </Button>
              )}
              <Button
                onClick={() => completeMutation.mutate(currentStep.id)}
                disabled={completeMutation.isPending}
                className={!currentStep.optional ? "ml-auto" : ""}
              >
                {completeMutation.isPending ? "Saving..." : "Continue"}
              </Button>
            </CardFooter>
          </Card>
        )}
      </div>
    </div>
  );
}

/** Render step-specific form content. */
function StepContent({ stepId }: { stepId: OnboardingStepId }) {
  switch (stepId) {
    case "create_org":
      return (
        <div className="space-y-3">
          <div className="space-y-2">
            <Label htmlFor="org-name">Organization Name</Label>
            <Input id="org-name" placeholder="Acme Inc." />
          </div>
          <div className="space-y-2">
            <Label htmlFor="org-slug">Slug</Label>
            <Input id="org-slug" placeholder="acme-inc" />
          </div>
        </div>
      );
    case "invite_team":
      return (
        <div className="space-y-3">
          <div className="space-y-2">
            <Label htmlFor="invite-emails">Team Member Emails</Label>
            <Input
              id="invite-emails"
              placeholder="alice@example.com, bob@example.com"
            />
            <p className="text-xs text-muted-foreground">
              Separate multiple emails with commas.
            </p>
          </div>
        </div>
      );
    case "select_plan":
      return (
        <div className="grid grid-cols-2 gap-3">
          {["Free", "Pro"].map((plan) => (
            <button
              key={plan}
              className="rounded-lg border-2 p-4 text-left hover:border-primary transition-colors focus:border-primary focus:outline-none"
            >
              <p className="font-semibold">{plan}</p>
              <p className="text-sm text-muted-foreground">
                {plan === "Free"
                  ? "For individuals getting started"
                  : "For teams that need more"}
              </p>
            </button>
          ))}
        </div>
      );
    case "create_api_key":
      return (
        <div className="space-y-3">
          <div className="space-y-2">
            <Label htmlFor="key-name">API Key Name</Label>
            <Input id="key-name" placeholder="My first API key" />
          </div>
          <p className="text-xs text-muted-foreground">
            You can always create more API keys later from Settings.
          </p>
        </div>
      );
    default:
      return null;
  }
}
