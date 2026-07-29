"use client";

import { useRouter } from "next/navigation";
import { useEffect, useMemo, useSyncExternalStore } from "react";
import { orgMutations } from "@/features/organizations/service/mutations";
import { useAuth } from "@/lib/auth";
import { createBrowserOnboardingDraftStore } from "../application/browser-draft-store";
import {
	OnboardingController,
	type OnboardingViewModel,
} from "../application/controller";
import { onboardingClient } from "../service/client";

export interface OnboardingControllerBinding {
	controller: OnboardingController;
	model: OnboardingViewModel;
}

/** The complete React integration boundary for onboarding. */
export function useOnboardingController(
	requiredOnly: boolean,
): OnboardingControllerBinding {
	const { organizationId = "", switchOrganization } = useAuth();
	const router = useRouter();
	const controller = useMemo(
		() =>
			new OnboardingController({
				organizationId,
				requiredOnly,
				backend: {
					async createOrganization(name, slug) {
						const response = await orgMutations.create(name, slug);
						if (!response.organization) {
							throw new Error("Organization was not returned");
						}
						return response.organization.id;
					},
					switchOrganization,
					getProgress: onboardingClient.getProgress,
					skipStep: onboardingClient.skipStep,
				},
				draftStore:
					typeof window === "undefined"
						? undefined
						: createBrowserOnboardingDraftStore(window.sessionStorage),
				navigate: (href) => router.push(href),
			}),
		[organizationId, requiredOnly, router, switchOrganization],
	);
	const model = useSyncExternalStore(
		controller.subscribe,
		controller.getSnapshot,
		controller.getSnapshot,
	);

	useEffect(() => {
		void controller.start();
	}, [controller]);

	return { controller, model };
}
