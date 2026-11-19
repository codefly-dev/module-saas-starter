"use client";

import { Progress } from "@codefly-dev/ui/layout";
import {
	Building2,
	Check,
	CreditCard,
	Key,
	PartyPopper,
	RotateCcw,
	SkipForward,
	UsersRound,
} from "lucide-react";
import { useAuth } from "@/lib/auth";
import {
	Button,
	Card,
	CardContent,
	CardDescription,
	CardFooter,
	CardHeader,
	CardTitle,
	DelayedLoading,
	ErrorState,
	Field,
	Input,
	Page,
	PageHeader,
	Skeleton,
	Spinner,
	useLoadingPhase,
} from "@/shared/ui";
import type {
	OnboardingController,
	OnboardingViewModel,
} from "../application/controller";
import {
	ONBOARDING_STEP_CONTENT,
	type OnboardingStep,
	OnboardingStepId,
} from "../model/types";
import { useOnboardingController } from "../react/use-onboarding-controller";

/**
 * Derive workspace placeholders from the signed-in user's email domain
 * (alice@acme.com -> "Acme" / "acme") so the suggestion reflects the operator's
 * org instead of a generic "Acme". Falls back to the generic sample when no
 * email is available.
 */
function orgPlaceholders(email: string | undefined): {
	name: string;
	slug: string;
} {
	const label = email?.split("@")[1]?.split(".")[0]?.toLowerCase();
	if (!label) {
		return { name: "Acme Inc.", slug: "acme-inc" };
	}
	return { name: label.charAt(0).toUpperCase() + label.slice(1), slug: label };
}

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
	const { user } = useAuth();
	const placeholders = orgPlaceholders(user?.email);
	return (
		<form
			className="space-y-4"
			noValidate
			onSubmit={(event) => {
				event.preventDefault();
				void controller.createOrganization();
			}}
		>
			<Field label="Organization name" error={model.fieldErrors?.name}>
				{(control) => (
					<Input
						{...control}
						disabled={model.pending}
						autoComplete="organization"
						value={model.draft.name}
						onChange={(event) =>
							controller.setOrganizationName(event.target.value)
						}
						placeholder={placeholders.name}
						required
					/>
				)}
			</Field>
			<Field
				label="Organization address"
				error={model.fieldErrors?.slug}
				description="Use 2–63 letters, numbers or hyphens."
			>
				{(control) => (
					<Input
						{...control}
						disabled={model.pending}
						value={model.draft.slug}
						onChange={(event) =>
							controller.setOrganizationSlug(event.target.value)
						}
						pattern="[a-z0-9]+(?:-[a-z0-9]+)*"
						placeholder={placeholders.slug}
						required
					/>
				)}
			</Field>
			{model.error && (
				<ErrorState title="Setup wasn’t saved" detail={model.error} />
			)}
			<Button type="submit" disabled={model.pending} className="w-full">
				<DelayedLoading active={model.pending} label="Saving setup">
					<Spinner label="Creating organization" size="sm" />
				</DelayedLoading>
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
				Complete this step on its settings page. Your checklist updates when you
				return.
			</p>
			<Button className="w-full" onClick={() => controller.openCurrentStep()}>
				{ONBOARDING_STEP_CONTENT[step.id].label}
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
	return (
		<Page>
			<PageHeader
				title="Get started"
				description="Set up your workspace and invite the people you work with."
			/>
			<OnboardingWizardView controller={controller} model={model} />
		</Page>
	);
}

export function OnboardingWizardView({
	controller,
	model,
}: {
	controller: OnboardingController;
	model: OnboardingViewModel;
}) {
	const loading = useLoadingPhase(model.phase === "loading");
	if (model.phase === "organization") {
		return (
			<Card className="mx-auto w-full max-w-lg">
				<CardHeader>
					<CardTitle role="heading" aria-level={2}>
						Create your workspace
					</CardTitle>
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

	if (loading.quiet || loading.indicator) {
		return (
			<div className="mx-auto w-full max-w-xl space-y-4 p-6">
				{loading.indicator && (
					<>
						<Skeleton className="h-7 w-52" />
						<Skeleton className="h-80 w-full" />
					</>
				)}
			</div>
		);
	}

	if (model.phase === "error") {
		return (
			<Card className="mx-auto w-full max-w-lg">
				<CardHeader>
					<CardTitle role="heading" aria-level={2}>
						Setup needs attention
					</CardTitle>
					<ErrorState
						title="Couldn’t continue setup"
						detail={
							model.error ??
							"Your progress is saved. Try again when your connection is available."
						}
					/>
				</CardHeader>
				<CardFooter>
					{model.retryable !== false && (
						<Button
							disabled={model.pending}
							onClick={() => void controller.refresh()}
						>
							<RotateCcw className="mr-2 h-4 w-4" />
							Try again
						</Button>
					)}
				</CardFooter>
			</Card>
		);
	}

	if (model.phase === "complete") {
		return (
			<Card className="mx-auto w-full max-w-md text-center">
				<CardHeader>
					<PartyPopper className="mx-auto mb-3 h-10 w-10 text-primary" />
					<CardTitle role="heading" aria-level={2}>
						{model.requiredOnly ? "Workspace ready" : "Checklist complete"}
					</CardTitle>
					<CardDescription>
						Your workspace is ready. You can continue from your dashboard.
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
				</div>
				<Progress
					label="Setup progress"
					value={
						model.visibleSteps.length
							? (model.completedCount / model.visibleSteps.length) * 100
							: 0
					}
				/>
			</section>

			<Card>
				<CardHeader>
					<div className="flex items-start gap-3">
						<div className="rounded-lg bg-primary/10 p-2">
							<Icon className="h-5 w-5 text-primary" />
						</div>
						<div>
							<CardTitle role="heading" aria-level={2}>
								{currentStep.label}
							</CardTitle>
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
							<DelayedLoading active={model.pending} label="Saving setup">
								<Spinner label="Checking progress" size="sm" />
							</DelayedLoading>
							{!model.pending && <Check className="mr-2 h-4 w-4" />}
							Check progress
						</Button>
					</CardFooter>
				)}
			</Card>
		</div>
	);
}
