"use client";

import { useQuery } from "@tanstack/react-query";
import Link from "next/link";
import { usePathname } from "next/navigation";
import type { ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/lib/auth";
import { onboardingQueries } from "../service/queries";
import { OnboardingWizard } from "./onboarding-wizard";

export function OnboardingGate({ children }: { children: ReactNode }) {
	const { isAuthenticated, organizationId = "" } = useAuth();
	const pathname = usePathname();
	const query = useQuery({
		...onboardingQueries.progress(organizationId),
		enabled: isAuthenticated && Boolean(organizationId),
	});

	if (
		isAuthenticated &&
		!query.isLoading &&
		!query.isError &&
		query.data &&
		!query.data.requiredComplete
	) {
		return (
			<main className="flex min-h-screen items-center justify-center bg-background">
				<OnboardingWizard requiredOnly />
			</main>
		);
	}

	return (
		<>
			{children}
			{pathname !== "/onboarding" &&
				query.data?.requiredComplete &&
				!query.data.checklistComplete && (
					<div className="fixed bottom-4 right-4 z-40 max-w-sm rounded-xl border bg-card p-4 shadow-lg">
						<p className="font-medium">Finish workspace setup</p>
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
