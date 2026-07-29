import { describe, expect, it, vi } from "vitest";
import {
	type OnboardingProgress,
	type OnboardingStep,
	OnboardingStepId,
	OnboardingStepStatus,
} from "../model/types";
import {
	normalizeOrganizationSlug,
	OnboardingController,
	type OnboardingControllerBackend,
	type OnboardingDraftStore,
	slugifyOrganizationName,
	validOrganizationSlug,
} from "./controller";

function step(
	id: OnboardingStep["id"],
	options: Partial<OnboardingStep> = {},
): OnboardingStep {
	return {
		id,
		label: `Step ${id}`,
		description: `Description ${id}`,
		required: false,
		status: OnboardingStepStatus.PENDING,
		skipReason: "",
		...options,
	};
}

function progress(
	steps: OnboardingStep[],
	options: Partial<OnboardingProgress> = {},
): OnboardingProgress {
	const pending = steps.find(
		(candidate) => candidate.status === OnboardingStepStatus.PENDING,
	);
	return {
		organizationId: "org-1",
		flowId: "default",
		flowVersion: 1,
		variant: "default",
		steps,
		currentStep: pending?.id ?? OnboardingStepId.UNSPECIFIED,
		nextStep: pending?.id ?? OnboardingStepId.UNSPECIFIED,
		requiredComplete: steps
			.filter((candidate) => candidate.required)
			.every(
				(candidate) => candidate.status === OnboardingStepStatus.COMPLETED,
			),
		checklistComplete: steps.every(
			(candidate) => candidate.status !== OnboardingStepStatus.PENDING,
		),
		activationAchieved: false,
		...options,
	};
}

class RecordingBackend implements OnboardingControllerBackend {
	created: Array<{ name: string; slug: string }> = [];
	switched: string[] = [];
	progressRequests: string[] = [];
	skips: Array<{ organizationId: string; stepId: OnboardingStep["id"] }> = [];
	nextOrganizationId = "org-created";
	nextProgress = progress([
		step(OnboardingStepId.CONFIGURE_ORGANIZATION, {
			required: true,
			status: OnboardingStepStatus.COMPLETED,
		}),
		step(OnboardingStepId.INVITE_TEAM),
	]);
	createFailure?: Error;
	progressFailure?: Error;

	async createOrganization(name: string, slug: string): Promise<string> {
		this.created.push({ name, slug });
		if (this.createFailure) throw this.createFailure;
		return this.nextOrganizationId;
	}

	async switchOrganization(organizationId: string): Promise<void> {
		this.switched.push(organizationId);
	}

	async getProgress(organizationId: string): Promise<OnboardingProgress> {
		this.progressRequests.push(organizationId);
		if (this.progressFailure) throw this.progressFailure;
		return this.nextProgress;
	}

	async skipStep(
		organizationId: string,
		stepId: OnboardingStep["id"],
	): Promise<OnboardingProgress> {
		this.skips.push({ organizationId, stepId });
		this.nextProgress = progress(
			this.nextProgress.steps.map((candidate) =>
				candidate.id === stepId
					? {
							...candidate,
							status: OnboardingStepStatus.SKIPPED,
							skipReason: "not_now",
						}
					: candidate,
			),
		);
		return this.nextProgress;
	}
}

function recordingDraftStore(
	initial = { name: "", slug: "" },
): OnboardingDraftStore & {
	saved: Array<{ name: string; slug: string }>;
	cleared: number;
} {
	return {
		saved: [],
		cleared: 0,
		load: () => initial,
		save(draft) {
			this.saved.push(draft);
		},
		clear() {
			this.cleared += 1;
		},
	};
}

describe("OnboardingController", () => {
	it("starts in organization creation and restores the saved draft", () => {
		const backend = new RecordingBackend();
		const draftStore = recordingDraftStore({
			name: "Recovered Company",
			slug: "recovered-company",
		});
		const controller = new OnboardingController({ backend, draftStore });

		expect(controller.getSnapshot()).toMatchObject({
			phase: "organization",
			organizationId: "",
			draft: {
				name: "Recovered Company",
				slug: "recovered-company",
			},
			pending: false,
		});
	});

	it("normalizes and persists organization input", () => {
		const draftStore = recordingDraftStore();
		const controller = new OnboardingController({
			backend: new RecordingBackend(),
			draftStore,
		});
		const listener = vi.fn();
		const unsubscribe = controller.subscribe(listener);

		controller.setOrganizationName("  ACME & Sons  ");
		controller.setOrganizationSlug("  Better @ Workspace!  ");
		unsubscribe();
		controller.setOrganizationName("No longer observed");

		expect(draftStore.saved).toEqual([
			{ name: "  ACME & Sons  ", slug: "acme-sons" },
			{ name: "  ACME & Sons  ", slug: "better-workspace" },
			{ name: "No longer observed", slug: "better-workspace" },
		]);
		expect(listener).toHaveBeenCalledTimes(2);
	});

	it("rejects invalid organization input without touching the backend", async () => {
		const backend = new RecordingBackend();
		const controller = new OnboardingController({ backend });
		controller.setOrganizationName("A");
		controller.setOrganizationSlug("a");

		await controller.createOrganization();

		expect(controller.getSnapshot()).toMatchObject({
			phase: "organization",
			error: "Enter an organization name and a valid workspace slug.",
			pending: false,
		});
		expect(backend.created).toEqual([]);
	});

	it("creates, switches, clears the draft, and loads persisted progress", async () => {
		const backend = new RecordingBackend();
		const draftStore = recordingDraftStore();
		const controller = new OnboardingController({ backend, draftStore });
		controller.setOrganizationName("  ACME  ");

		await controller.createOrganization();

		expect(backend.created).toEqual([{ name: "ACME", slug: "acme" }]);
		expect(backend.switched).toEqual(["org-created"]);
		expect(backend.progressRequests).toEqual(["org-created"]);
		expect(draftStore.cleared).toBe(1);
		expect(controller.getSnapshot()).toMatchObject({
			phase: "step",
			organizationId: "org-created",
			draft: { name: "", slug: "" },
			currentStep: { id: OnboardingStepId.INVITE_TEAM },
			completedCount: 1,
			pending: false,
		});
	});

	it("loads an existing organization and exposes required steps only", async () => {
		const backend = new RecordingBackend();
		backend.nextProgress = progress([
			step(OnboardingStepId.CONFIGURE_ORGANIZATION, {
				required: true,
				status: OnboardingStepStatus.COMPLETED,
			}),
			step(OnboardingStepId.INVITE_TEAM),
			step(OnboardingStepId.CHOOSE_PLAN, { required: true }),
		]);
		const controller = new OnboardingController({
			backend,
			organizationId: "org-existing",
			requiredOnly: true,
		});

		await controller.start();

		expect(backend.progressRequests).toEqual(["org-existing"]);
		expect(
			controller.getSnapshot().visibleSteps.map((candidate) => candidate.id),
		).toEqual([
			OnboardingStepId.CONFIGURE_ORGANIZATION,
			OnboardingStepId.CHOOSE_PLAN,
		]);
		expect(controller.getSnapshot()).toMatchObject({
			phase: "step",
			currentStep: { id: OnboardingStepId.CHOOSE_PLAN },
			completedCount: 1,
		});
	});

	it("skips an optional step and applies the returned persisted state", async () => {
		const backend = new RecordingBackend();
		const controller = new OnboardingController({
			backend,
			organizationId: "org-1",
		});
		await controller.start();

		await controller.skipCurrentStep();

		expect(backend.skips).toEqual([
			{
				organizationId: "org-1",
				stepId: OnboardingStepId.INVITE_TEAM,
			},
		]);
		expect(controller.getSnapshot()).toMatchObject({
			phase: "complete",
			completedCount: 1,
			pending: false,
		});
	});

	it("does not allow a required step to be skipped", async () => {
		const backend = new RecordingBackend();
		backend.nextProgress = progress([
			step(OnboardingStepId.CONFIGURE_ORGANIZATION, { required: true }),
		]);
		const controller = new OnboardingController({
			backend,
			organizationId: "org-1",
		});
		await controller.start();

		await controller.skipCurrentStep();

		expect(backend.skips).toEqual([]);
		expect(controller.getSnapshot().error).toBe(
			"This onboarding step cannot be skipped.",
		);
	});

	it("surfaces backend failures and recovers on refresh", async () => {
		const backend = new RecordingBackend();
		backend.progressFailure = new Error("accounts unavailable");
		const controller = new OnboardingController({
			backend,
			organizationId: "org-1",
		});

		await controller.start();
		expect(controller.getSnapshot()).toMatchObject({
			phase: "error",
			error: "accounts unavailable",
			pending: false,
		});

		backend.progressFailure = undefined;
		await controller.refresh();
		expect(controller.getSnapshot()).toMatchObject({
			phase: "step",
			error: undefined,
			pending: false,
		});
	});

	it("opens canonical step routes and the dashboard without React", async () => {
		const backend = new RecordingBackend();
		const navigate = vi.fn();
		const controller = new OnboardingController({
			backend,
			organizationId: "org-1",
			navigate,
		});
		await controller.start();

		controller.openCurrentStep();
		controller.goToDashboard();

		expect(navigate.mock.calls).toEqual([["/admin/invitations"], ["/"]]);
	});

	it("ignores a duplicate create submission while one is pending", async () => {
		let release!: (organizationId: string) => void;
		const backend = new RecordingBackend();
		backend.createOrganization = vi.fn(
			() =>
				new Promise<string>((resolve) => {
					release = resolve;
				}),
		);
		const controller = new OnboardingController({ backend });
		controller.setOrganizationName("Acme");

		const first = controller.createOrganization();
		const duplicate = controller.createOrganization();
		expect(controller.getSnapshot().pending).toBe(true);
		expect(backend.createOrganization).toHaveBeenCalledTimes(1);

		release("org-created");
		await Promise.all([first, duplicate]);
		expect(controller.getSnapshot().pending).toBe(false);
	});
});

describe("organization slug rules", () => {
	it("matches the backend normalization and validation contract", () => {
		expect(slugifyOrganizationName(" Hello, Déus! ")).toBe("hello-d-us");
		expect(normalizeOrganizationSlug("--A__B--")).toBe("a-b");
		expect(validOrganizationSlug("ab")).toBe(true);
		expect(validOrganizationSlug("a")).toBe(false);
		expect(validOrganizationSlug("-ab")).toBe(false);
		expect(validOrganizationSlug("ab-")).toBe(false);
		expect(validOrganizationSlug("a".repeat(64))).toBe(false);
	});
});
