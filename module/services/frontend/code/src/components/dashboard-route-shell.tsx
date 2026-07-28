"use client";

import { useRouter } from "next/navigation";
import { type ReactNode, useEffect } from "react";
import { AdminLayout } from "@/components/admin-layout";
import { CommandPalette } from "@/components/command-palette";
import { ImpersonationBanner } from "@/components/impersonation-banner";
import { Skeleton } from "@/components/ui/skeleton";
import { OnboardingGate } from "@/features/onboarding/ui/onboarding-gate";
import { useAuth } from "@/lib/auth";

export function DashboardRouteShell({ children }: { children: ReactNode }) {
	const { isAuthenticated, isLoading } = useAuth();
	const router = useRouter();

	useEffect(() => {
		if (!isLoading && !isAuthenticated) router.replace("/auth/login");
	}, [isLoading, isAuthenticated, router]);

	if (isLoading) {
		return (
			<div className="min-h-screen flex items-center justify-center">
				<Skeleton className="h-8 w-48" />
			</div>
		);
	}
	if (!isAuthenticated) return null;

	return (
		<>
			<ImpersonationBanner />
			<CommandPalette />
			<OnboardingGate>
				<AdminLayout>{children}</AdminLayout>
			</OnboardingGate>
		</>
	);
}
