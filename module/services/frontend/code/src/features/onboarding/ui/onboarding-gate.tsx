"use client";

import { XIcon } from "lucide-react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { type ReactNode, useSyncExternalStore } from "react";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/lib/auth";
import { createBrowserOnboardingReminderStore } from "../application/browser-reminder-store";
import { useOnboardingController } from "../react/use-onboarding-controller";
import { OnboardingWizardView } from "./onboarding-wizard";

// One store for the module: `useSyncExternalStore` compares the subscribe
// function by identity, so building it per render would resubscribe forever.
const reminderStore = createBrowserOnboardingReminderStore(
	typeof window === "undefined" ? null : window.localStorage,
);

export function OnboardingGate({ children }: { children: ReactNode }) {
	const { isAuthenticated, organizationId = "" } = useAuth();
	const pathname = usePathname();
	const { controller, model } = useOnboardingController(false);
	// The server cannot know what this browser dismissed, so it renders the
	// reminder and React swaps it after hydration. `useSyncExternalStore` is what
	// makes that a defined re-render rather than a hydration mismatch.
	const reminderDismissed = useSyncExternalStore(
		reminderStore.subscribe,
		() => reminderStore.isDismissed(organizationId),
		() => false,
	);

	if (isAuthenticated && !organizationId) {
		return (
			<main className="flex min-h-screen items-center justify-center bg-background">
				<OnboardingWizardView controller={controller} model={model} />
			</main>
		);
	}

	if (
		isAuthenticated &&
		model.phase !== "loading" &&
		model.phase !== "error" &&
		model.progress &&
		!model.progress.requiredComplete
	) {
		return (
			<main className="flex min-h-screen items-center justify-center bg-background">
				<OnboardingWizardView controller={controller} model={model} />
			</main>
		);
	}

	return (
		<>
			{children}
			{pathname !== "/onboarding" &&
				model.progress?.requiredComplete &&
				!model.progress.checklistComplete &&
				!reminderDismissed && (
					<div
						role="complementary"
						aria-label="Finish workspace setup"
						className="fixed right-4 bottom-4 z-40 max-w-sm rounded-xl border bg-card p-4 shadow-lg"
					>
						<div className="flex items-start justify-between gap-2">
							<p className="font-medium">Finish workspace setup</p>
							<Button
								variant="ghost"
								size="icon-xs"
								aria-label="Dismiss workspace setup reminder"
								onClick={() => reminderStore.dismiss(organizationId)}
							>
								<XIcon />
							</Button>
						</div>
						<p className="mt-1 text-sm text-muted-foreground">
							Optional setup is saved and never blocks the product.
						</p>
						<Button
							size="sm"
							className="mt-3"
							nativeButton={false}
							render={<Link href="/onboarding" />}
						>
							Resume checklist
						</Button>
					</div>
				)}
		</>
	);
}
