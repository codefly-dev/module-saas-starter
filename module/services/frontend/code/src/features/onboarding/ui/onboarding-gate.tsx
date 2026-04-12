"use client";

import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { useAuth } from "@/lib/auth";
import { onboardingQueries } from "../service/queries";
import type { OnboardingProgress } from "../model/types";
import { isWizardComplete } from "../model/transforms";
import { OnboardingWizard } from "./onboarding-wizard";

interface OnboardingGateProps {
  children: ReactNode;
}

export function OnboardingGate({ children }: OnboardingGateProps) {
  const { isAuthenticated } = useAuth();
  const { data, isLoading } = useQuery({
    ...onboardingQueries.progress(),
    enabled: isAuthenticated,
  });

  // Don't block if not authenticated, still loading, or explicitly dismissed
  const dismissed = useQuery({
    queryKey: ["onboarding", "dismissed"],
    queryFn: () => false,
    staleTime: Infinity,
  });

  if (!isAuthenticated || isLoading || dismissed.data === true) {
    return <>{children}</>;
  }

  const progress = data as OnboardingProgress | undefined;

  if (progress && !isWizardComplete(progress)) {
    return <OnboardingWizard />;
  }

  return <>{children}</>;
}
