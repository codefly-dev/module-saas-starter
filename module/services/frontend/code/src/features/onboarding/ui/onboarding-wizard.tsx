"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
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
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { orgMutations } from "@/features/organizations/service/mutations";
import { useAuth } from "@/lib/auth";
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
import {
	ONBOARDING_STEP_CONTENT,
	type OnboardingStep,
	OnboardingStepId,
	OnboardingStepStatus,
} from "../model/types";
import { onboardingMutations } from "../service/mutations";
import { onboardingQueries } from "../service/queries";

const DRAFT_KEY = "saas-starter:onboarding-organization-draft";

const stepIcons = {
	[OnboardingStepId.CONFIGURE_ORGANIZATION]: Building2,
	[OnboardingStepId.INVITE_TEAM]: UsersRound,
	[OnboardingStepId.CHOOSE_PLAN]: CreditCard,
	[OnboardingStepId.SETUP_API_KEY]: Key,
};

function OrganizationAction() {
	const { switchOrganization } = useAuth();
	const queryClient = useQueryClient();
	const [name, setName] = useState("");
	const [slug, setSlug] = useState("");

	useEffect(() => {
		const frame = window.requestAnimationFrame(() => {
			const draft = window.sessionStorage.getItem(DRAFT_KEY);
			if (!draft) return;
			try {
				const parsed = JSON.parse(draft) as { name?: string; slug?: string };
				setName(parsed.name ?? "");
				setSlug(parsed.slug ?? "");
			} catch {
				window.sessionStorage.removeItem(DRAFT_KEY);
			}
		});
		return () => window.cancelAnimationFrame(frame);
	}, []);

	useEffect(() => {
		window.sessionStorage.setItem(DRAFT_KEY, JSON.stringify({ name, slug }));
	}, [name, slug]);

	const createOrganization = useMutation({
		mutationFn: () => orgMutations.create(name.trim(), slug.trim()),
		onSuccess: async (response) => {
			if (!response.organization)
				throw new Error("Organization was not returned");
			window.sessionStorage.removeItem(DRAFT_KEY);
			await switchOrganization(response.organization.id);
			await queryClient.invalidateQueries({ queryKey: ["onboarding"] });
			toast.success("Organization created");
		},
		onError: (error) =>
			toast.error("Organization could not be created", {
				description: error instanceof Error ? error.message : "Try again.",
			}),
	});

	function changeName(value: string) {
		setName(value);
		if (!slug) {
			setSlug(
				value
					.toLowerCase()
					.trim()
					.replace(/[^a-z0-9]+/g, "-")
					.replace(/^-|-$/g, ""),
			);
		}
	}

	return (
		<form
			className="space-y-4"
			onSubmit={(event) => {
				event.preventDefault();
				createOrganization.mutate();
			}}
		>
			<div className="space-y-2">
				<Label htmlFor="org-name">Organization name</Label>
				<Input
					id="org-name"
					autoComplete="organization"
					value={name}
					onChange={(event) => changeName(event.target.value)}
					placeholder="Acme Inc."
					required
				/>
			</div>
			<div className="space-y-2">
				<Label htmlFor="org-slug">Workspace slug</Label>
				<Input
					id="org-slug"
					value={slug}
					onChange={(event) => setSlug(event.target.value.toLowerCase())}
					pattern="[a-z0-9]+(?:-[a-z0-9]+)*"
					placeholder="acme-inc"
					required
				/>
			</div>
			<Button
				type="submit"
				disabled={!name.trim() || !slug.trim() || createOrganization.isPending}
				className="w-full"
			>
				{createOrganization.isPending && (
					<Loader2 className="mr-2 h-4 w-4 animate-spin" />
				)}
				Create organization
			</Button>
		</form>
	);
}

function StepAction({ step }: { step: OnboardingStep }) {
	if (step.id === OnboardingStepId.CONFIGURE_ORGANIZATION) {
		return <OrganizationAction />;
	}
	const href = ONBOARDING_STEP_CONTENT[step.id]?.href ?? "/";
	return (
		<div className="space-y-3">
			<p className="text-sm text-muted-foreground">
				This checklist item is completed from its canonical admin surface and
				updates automatically when you return.
			</p>
			<Button
				nativeButton={false}
				render={<Link href={href} />}
				className="w-full"
			>
				Open {step.label}
			</Button>
		</div>
	);
}

export function OnboardingWizard({
	requiredOnly = false,
}: {
	requiredOnly?: boolean;
}) {
	const { organizationId = "" } = useAuth();
	const queryClient = useQueryClient();
	const router = useRouter();
	const query = useQuery({
		...onboardingQueries.progress(organizationId),
		enabled: Boolean(organizationId),
	});

	const skipMutation = useMutation({
		mutationFn: (stepId: OnboardingStepId) =>
			onboardingMutations.skipStep(organizationId, stepId, "not_now"),
		onSuccess: () =>
			queryClient.invalidateQueries({ queryKey: ["onboarding"] }),
		onError: () => toast.error("This step could not be skipped"),
	});

	const visibleSteps = useMemo(
		() =>
			(query.data?.steps ?? []).filter(
				(step) => !requiredOnly || step.required,
			),
		[query.data, requiredOnly],
	);
	const currentStep =
		visibleSteps.find((step) => step.status === OnboardingStepStatus.PENDING) ??
		visibleSteps[visibleSteps.length - 1];

	if (!organizationId) {
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
					<OrganizationAction />
				</CardContent>
			</Card>
		);
	}

	if (query.isLoading) {
		return (
			<div className="mx-auto w-full max-w-xl space-y-4 p-6">
				<Skeleton className="h-7 w-52" />
				<Skeleton className="h-80 w-full" />
			</div>
		);
	}

	if (query.isError) {
		return (
			<Card className="mx-auto w-full max-w-lg">
				<CardHeader>
					<CardTitle>Setup is temporarily unavailable</CardTitle>
					<CardDescription>
						Your progress is still stored on the server. Retry when the
						connection is available.
					</CardDescription>
				</CardHeader>
				<CardFooter>
					<Button onClick={() => query.refetch()}>
						<RotateCcw className="mr-2 h-4 w-4" />
						Retry
					</Button>
				</CardFooter>
			</Card>
		);
	}

	const progress = query.data;
	const complete = requiredOnly
		? progress?.requiredComplete
		: progress?.checklistComplete;

	if (complete) {
		return (
			<Card className="mx-auto w-full max-w-md text-center">
				<CardHeader>
					<PartyPopper className="mx-auto mb-3 h-10 w-10 text-primary" />
					<CardTitle>
						{requiredOnly ? "Workspace ready" : "Checklist complete"}
					</CardTitle>
					<CardDescription>
						{progress?.activationAchieved
							? "Your organization has reached its configured activation milestone."
							: "Setup is saved. Product activation is tracked separately."}
					</CardDescription>
				</CardHeader>
				<CardFooter className="justify-center">
					<Button onClick={() => router.replace("/")}>Go to dashboard</Button>
				</CardFooter>
			</Card>
		);
	}

	if (!currentStep) return null;
	const Icon = stepIcons[currentStep.id];

	return (
		<div className="mx-auto w-full max-w-xl space-y-6 p-4 sm:p-6">
			<section aria-label="Onboarding progress" className="space-y-2">
				<div className="flex justify-between text-sm text-muted-foreground">
					<span>
						{
							visibleSteps.filter(
								(step) => step.status === OnboardingStepStatus.COMPLETED,
							).length
						}{" "}
						of {visibleSteps.length} completed
					</span>
					<span>
						Flow {progress?.flowId} v{progress?.flowVersion}
					</span>
				</div>
				<div className="grid grid-cols-4 gap-2">
					{visibleSteps.map((step) => (
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
					<StepAction step={currentStep} />
				</CardContent>
				{!currentStep.required && (
					<CardFooter className="justify-between">
						<Button
							variant="ghost"
							onClick={() => skipMutation.mutate(currentStep.id)}
							disabled={skipMutation.isPending}
						>
							<SkipForward className="mr-2 h-4 w-4" />
							Do this later
						</Button>
						<Button
							variant="outline"
							onClick={() => query.refetch()}
							disabled={query.isFetching}
						>
							{query.isFetching ? (
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
