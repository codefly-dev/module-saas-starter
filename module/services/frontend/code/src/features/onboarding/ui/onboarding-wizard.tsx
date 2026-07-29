"use client";

import {
	Building2,
	Check,
	CreditCard,
	Key,
	Loader2,
	PartyPopper,
	RotateCcw,
	SkipForward,
	UsersRound,
} from "lucide-react";
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
	Skeleton,
} from "@/shared/ui";
import type {
	OnboardingController,
	OnboardingViewModel,
} from "../application/controller";
import {
	ONBOARDING_STEP_CONTENT,
	type OnboardingStep,
	OnboardingStepId,
	OnboardingStepStatus,
} from "../model/types";
import { useOnboardingController } from "../react/use-onboarding-controller";

const stepIcons = {
	[OnboardingStepId.CONFIGURE_ORGANIZATION]: Building2,
	[OnboardingStepId.INVITE_TEAM]: UsersRound,
	[OnboardingStepId.CHOOSE_PLAN]: CreditCard,
	[OnboardingStepId.SETUP_API_KEY]: Key,
};

function OrganizationAction({
	controller,
	model,
}: {
	controller: OnboardingController;
	model: OnboardingViewModel;
}) {
	return (
		<form
			className="space-y-4"
			onSubmit={(event) => {
				event.preventDefault();
				void controller.createOrganization();
			}}
		>
			<div className="space-y-2">
				<Label htmlFor="org-name">Organization name</Label>
				<Input
					id="org-name"
					autoComplete="organization"
					value={model.draft.name}
					onChange={(event) =>
						controller.setOrganizationName(event.target.value)
					}
					placeholder="Acme Inc."
					required
				/>
			</div>
			<div className="space-y-2">
				<Label htmlFor="org-slug">Workspace slug</Label>
				<Input
					id="org-slug"
					value={model.draft.slug}
					onChange={(event) =>
						controller.setOrganizationSlug(event.target.value)
					}
					pattern="[a-z0-9]+(?:-[a-z0-9]+)*"
					placeholder="acme-inc"
					required
				/>
			</div>
			{model.error && (
				<p role="alert" className="text-sm text-destructive">
					{model.error}
				</p>
			)}
			<Button
				type="submit"
				disabled={
					!model.draft.name.trim() || !model.draft.slug.trim() || model.pending
				}
				className="w-full"
			>
				{model.pending && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
				Create organization
			</Button>
		</form>
	);
}

function StepAction({
	controller,
	step,
}: {
	controller: OnboardingController;
	step: OnboardingStep;
}) {
	if (step.id === OnboardingStepId.CONFIGURE_ORGANIZATION) return null;
	return (
		<div className="space-y-3">
			<p className="text-sm text-muted-foreground">
				This checklist item is completed from its canonical admin surface and
				updates automatically when you return.
			</p>
			<Button className="w-full" onClick={() => controller.openCurrentStep()}>
				Open {ONBOARDING_STEP_CONTENT[step.id].label}
			</Button>
		</div>
	);
}

export function OnboardingWizard({
	requiredOnly = false,
}: {
	requiredOnly?: boolean;
}) {
	const { controller, model } = useOnboardingController(requiredOnly);
	return <OnboardingWizardView controller={controller} model={model} />;
}

export function OnboardingWizardView({
	controller,
	model,
}: {
	controller: OnboardingController;
	model: OnboardingViewModel;
}) {
	if (model.phase === "organization") {
		return (
			<Card className="mx-auto w-full max-w-lg">
				<CardHeader>
					<CardTitle>Create your workspace</CardTitle>
					<CardDescription>
						Set up the organization that will own your team and product
						resources.
					</CardDescription>
				</CardHeader>
				<CardContent>
					<OrganizationAction controller={controller} model={model} />
				</CardContent>
			</Card>
		);
	}

	if (model.phase === "loading") {
		return (
			<div className="mx-auto w-full max-w-xl space-y-4 p-6">
				<Skeleton className="h-7 w-52" />
				<Skeleton className="h-80 w-full" />
			</div>
		);
	}

	if (model.phase === "error") {
		return (
			<Card className="mx-auto w-full max-w-lg">
				<CardHeader>
					<CardTitle>Setup is temporarily unavailable</CardTitle>
					<CardDescription>
						{model.error ??
							"Your progress is still stored on the server. Retry when the connection is available."}
					</CardDescription>
				</CardHeader>
				<CardFooter>
					<Button onClick={() => void controller.refresh()}>
						<RotateCcw className="mr-2 h-4 w-4" />
						Retry
					</Button>
				</CardFooter>
			</Card>
		);
	}

	if (model.phase === "complete") {
		return (
			<Card className="mx-auto w-full max-w-md text-center">
				<CardHeader>
					<PartyPopper className="mx-auto mb-3 h-10 w-10 text-primary" />
					<CardTitle>
						{model.requiredOnly ? "Workspace ready" : "Checklist complete"}
					</CardTitle>
					<CardDescription>
						{model.progress?.activationAchieved
							? "Your organization has reached its configured activation milestone."
							: "Setup is saved. Product activation is tracked separately."}
					</CardDescription>
				</CardHeader>
				<CardFooter className="justify-center">
					<Button onClick={() => controller.goToDashboard()}>
						Go to dashboard
					</Button>
				</CardFooter>
			</Card>
		);
	}

	const currentStep = model.currentStep;
	if (!currentStep) return null;
	const Icon = stepIcons[currentStep.id];

	return (
		<div className="mx-auto w-full max-w-xl space-y-6 p-4 sm:p-6">
			<section aria-label="Onboarding progress" className="space-y-2">
				<div className="flex justify-between text-sm text-muted-foreground">
					<span>
						{model.completedCount} of {model.visibleSteps.length} completed
					</span>
					<span>
						Flow {model.progress?.flowId} v{model.progress?.flowVersion}
					</span>
				</div>
				<div className="grid grid-cols-4 gap-2">
					{model.visibleSteps.map((step) => (
						<div
							key={step.id}
							className={`h-2 rounded-full ${
								step.status === OnboardingStepStatus.COMPLETED
									? "bg-primary"
									: step.status === OnboardingStepStatus.SKIPPED
										? "bg-muted-foreground/40"
										: "bg-muted"
							}`}
						>
							<span className="sr-only">
								{step.label}: {OnboardingStepStatus[step.status].toLowerCase()}
							</span>
						</div>
					))}
				</div>
			</section>

			<Card>
				<CardHeader>
					<div className="flex items-start gap-3">
						<div className="rounded-lg bg-primary/10 p-2">
							<Icon className="h-5 w-5 text-primary" />
						</div>
						<div>
							<CardTitle>{currentStep.label}</CardTitle>
							<CardDescription>{currentStep.description}</CardDescription>
						</div>
					</div>
				</CardHeader>
				<CardContent>
					<StepAction controller={controller} step={currentStep} />
				</CardContent>
				{!currentStep.required && (
					<CardFooter className="justify-between">
						<Button
							variant="ghost"
							onClick={() => void controller.skipCurrentStep()}
							disabled={model.pending}
						>
							<SkipForward className="mr-2 h-4 w-4" />
							Do this later
						</Button>
						<Button
							variant="outline"
							onClick={() => void controller.refresh()}
							disabled={model.pending}
						>
							{model.pending ? (
								<Loader2 className="mr-2 h-4 w-4 animate-spin" />
							) : (
								<Check className="mr-2 h-4 w-4" />
							)}
							Check progress
						</Button>
					</CardFooter>
				)}
			</Card>
		</div>
	);
}
