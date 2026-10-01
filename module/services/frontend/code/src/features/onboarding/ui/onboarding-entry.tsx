"use client";

import { useQuery } from "@tanstack/react-query";
import { Building2, LogOut } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import { isPersonalOrganization } from "@/features/organizations/model/leave-flow";
import { orgQueries } from "@/features/organizations/service/queries";
import { useAuth } from "@/lib/auth";
import {
	Button,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
} from "@/shared/ui";
import type {
	OnboardingController,
	OnboardingViewModel,
} from "../application/controller";
import { OnboardingWizardView } from "./onboarding-wizard";

interface OnboardingEntryProps {
	controller: OnboardingController;
	model: OnboardingViewModel;
}

/**
 * What a signed-in user sees before they have a workspace. Onboarding's one
 * required step is "configure an organization", and the personal organization
 * registration creates never satisfies it — so without this, everyone who was
 * only ever meant to join someone else's organization was walked into creating
 * a new one. Here they are first offered the organizations they already belong
 * to; the creation wizard appears only when the deployment lets them create;
 * and when it does not, they are told to ask for an invitation instead.
 */
export function OnboardingEntry({ controller, model }: OnboardingEntryProps) {
	const { organizationId, switchOrganization, logout } = useAuth();
	const { data } = useQuery(orgQueries.list());
	const [switching, setSwitching] = useState<string | null>(null);

	const workspaces = (data?.organizations ?? []).filter(
		(org) => org.id !== organizationId && !isPersonalOrganization(org),
	);
	// Until the list answers, keep today's behavior: the wizard. The server
	// refuses a creation its policy forbids either way.
	const mayCreate = data ? data.canCreate : true;

	const continueIn = async (id: string) => {
		setSwitching(id);
		try {
			await switchOrganization(id);
		} catch (error) {
			toast.error("Couldn't switch organization", {
				description: error instanceof Error ? error.message : "Try again.",
			});
		} finally {
			setSwitching(null);
		}
	};

	return (
		<div className="mx-auto w-full max-w-xl space-y-6">
			{workspaces.length > 0 && (
				<Card>
					<CardHeader>
						<CardTitle>Continue in an organization you belong to</CardTitle>
						<CardDescription>
							You were added to these organizations; you don&apos;t need to
							create one.
						</CardDescription>
					</CardHeader>
					<CardContent className="space-y-2">
						{workspaces.map((org) => (
							<Button
								key={org.id}
								variant="outline"
								className="w-full justify-start"
								disabled={switching !== null}
								onClick={() => void continueIn(org.id)}
							>
								<Building2 className="mr-2 h-4 w-4" />
								{switching === org.id
									? "Switching…"
									: `Continue in ${org.name}`}
							</Button>
						))}
					</CardContent>
				</Card>
			)}
			{mayCreate ? (
				<OnboardingWizardView controller={controller} model={model} />
			) : (
				workspaces.length === 0 && (
					<Card>
						<CardHeader>
							<CardTitle>You&apos;re not in an organization yet</CardTitle>
							<CardDescription>
								Ask an administrator to invite you. Open the invitation link
								they send you to join.
							</CardDescription>
						</CardHeader>
						<CardContent>
							<Button variant="outline" onClick={() => void logout()}>
								<LogOut className="mr-2 h-4 w-4" />
								Sign out
							</Button>
						</CardContent>
					</Card>
				)
			)}
		</div>
	);
}
