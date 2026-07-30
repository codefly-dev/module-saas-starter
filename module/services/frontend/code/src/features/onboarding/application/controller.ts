import {
	ONBOARDING_STEP_CONTENT,
	type OnboardingProgress,
	type OnboardingStep,
	OnboardingStepStatus,
} from "../model/types";

export interface OrganizationDraft {
	name: string;
	slug: string;
}

export interface OnboardingDraftStore {
	load(): OrganizationDraft | null;
	save(draft: OrganizationDraft): void;
	clear(): void;
}

export type OnboardingPhase =
	| "organization"
	| "loading"
	| "error"
	| "step"
	| "complete";

export interface OnboardingViewModel {
	phase: OnboardingPhase;
	organizationId: string;
	draft: OrganizationDraft;
	progress?: OnboardingProgress;
	visibleSteps: OnboardingStep[];
	currentStep?: OnboardingStep;
	completedCount: number;
	requiredOnly: boolean;
	pending: boolean;
	error?: string;
}

export interface OnboardingControllerOptions {
	backend: OnboardingControllerBackend;
	organizationId?: string;
	requiredOnly?: boolean;
	draftStore?: OnboardingDraftStore;
	navigate?: (href: string) => void;
}

export interface OnboardingControllerBackend {
	createOrganization(name: string, slug: string): Promise<string>;
	switchOrganization(organizationId: string): Promise<unknown>;
	getProgress(organizationId: string): Promise<OnboardingProgress>;
	skipStep(
		organizationId: string,
		stepId: OnboardingStep["id"],
	): Promise<OnboardingProgress>;
}

type Listener = () => void;

/**
 * Framework-free onboarding application controller. React only subscribes to
 * it and renders its view model.
 */
export class OnboardingController {
	private readonly backend: OnboardingControllerOptions["backend"];
	private readonly requiredOnly: boolean;
	private readonly draftStore?: OnboardingDraftStore;
	private readonly navigate: (href: string) => void;
	private readonly listeners = new Set<Listener>();
	private model: OnboardingViewModel;

	constructor(options: OnboardingControllerOptions) {
		this.backend = options.backend;
		this.requiredOnly = options.requiredOnly ?? false;
		this.draftStore = options.draftStore;
		this.navigate = options.navigate ?? (() => undefined);
		const draft = options.draftStore?.load() ?? { name: "", slug: "" };
		this.model = {
			phase: options.organizationId ? "loading" : "organization",
			organizationId: options.organizationId ?? "",
			draft,
			visibleSteps: [],
			completedCount: 0,
			requiredOnly: this.requiredOnly,
			pending: false,
		};
	}

	getSnapshot = (): OnboardingViewModel => this.model;

	subscribe = (listener: Listener): (() => void) => {
		this.listeners.add(listener);
		return () => this.listeners.delete(listener);
	};

	async start(): Promise<void> {
		if (this.model.organizationId) await this.refresh();
	}

	setOrganizationName(name: string): void {
		const current = this.model.draft;
		const draft = {
			name,
			slug: current.slug || slugifyOrganizationName(name),
		};
		this.update({ draft, error: undefined });
		this.draftStore?.save(draft);
	}

	setOrganizationSlug(slug: string): void {
		const draft = {
			...this.model.draft,
			slug: normalizeOrganizationSlug(slug),
		};
		this.update({ draft, error: undefined });
		this.draftStore?.save(draft);
	}

	async createOrganization(): Promise<void> {
		const name = this.model.draft.name.trim();
		const slug = normalizeOrganizationSlug(this.model.draft.slug);
		if (!name || !validOrganizationSlug(slug)) {
			this.update({
				error: "Enter an organization name and a valid workspace slug.",
			});
			return;
		}
		await this.run(async () => {
			const organizationId = await this.backend.createOrganization(name, slug);
			await this.backend.switchOrganization(organizationId);
			this.draftStore?.clear();
			this.update({
				organizationId,
				draft: { name: "", slug: "" },
			});
			await this.loadProgress(organizationId);
		});
	}

	async refresh(): Promise<void> {
		const organizationId = this.model.organizationId;
		if (!organizationId) {
			this.update({ phase: "organization" });
			return;
		}
		await this.run(() => this.loadProgress(organizationId));
	}

	async skipCurrentStep(): Promise<void> {
		const { currentStep, organizationId } = this.model;
		if (!currentStep || currentStep.required || !organizationId) {
			this.update({ error: "This onboarding step cannot be skipped." });
			return;
		}
		await this.run(async () => {
			const progress = await this.backend.skipStep(
				organizationId,
				currentStep.id,
			);
			this.applyProgress(progress);
		});
	}

	openCurrentStep(): void {
		const step = this.model.currentStep;
		if (step) this.navigate(ONBOARDING_STEP_CONTENT[step.id]?.href ?? "/");
	}

	goToDashboard(): void {
		this.navigate("/");
	}

	private async run(operation: () => Promise<void>): Promise<void> {
		if (this.model.pending) return;
		this.update({ pending: true, error: undefined });
		try {
			await operation();
		} catch (error) {
			this.update({
				phase: "error",
				error:
					error instanceof Error
						? error.message
						: "Onboarding is temporarily unavailable.",
			});
		} finally {
			this.update({ pending: false });
		}
	}

	private async loadProgress(organizationId: string): Promise<void> {
		this.update({ phase: "loading" });
		this.applyProgress(await this.backend.getProgress(organizationId));
	}

	private applyProgress(progress: OnboardingProgress): void {
		const visibleSteps = progress.steps.filter(
			(step) => !this.requiredOnly || step.required,
		);
		const currentStep =
			visibleSteps.find(
				(step) => step.status === OnboardingStepStatus.PENDING,
			) ?? visibleSteps[visibleSteps.length - 1];
		const complete = this.requiredOnly
			? progress.requiredComplete
			: progress.checklistComplete;
		this.update({
			phase: complete ? "complete" : "step",
			progress,
			visibleSteps,
			currentStep,
			completedCount: visibleSteps.filter(
				(step) => step.status === OnboardingStepStatus.COMPLETED,
			).length,
			error: undefined,
		});
	}

	private update(patch: Partial<OnboardingViewModel>): void {
		this.model = { ...this.model, ...patch };
		for (const listener of this.listeners) listener();
	}
}

export function slugifyOrganizationName(value: string): string {
	return normalizeOrganizationSlug(value);
}

export function normalizeOrganizationSlug(value: string): string {
	return value
		.toLowerCase()
		.trim()
		.replace(/[^a-z0-9]+/g, "-")
		.replace(/^-|-$/g, "")
		.slice(0, 63)
		.replace(/-$/, "");
}

export function validOrganizationSlug(value: string): boolean {
	return /^[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$/.test(value);
}
