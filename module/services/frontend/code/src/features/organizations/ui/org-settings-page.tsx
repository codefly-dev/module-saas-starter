"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Building2 } from "lucide-react";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { EmptyState } from "@/components/empty-state";
import { OrgSelector } from "@/components/org-selector";
import { useAuth } from "@/lib/auth";
import { failureText } from "@/shared/lib/failure-text";
import { readOutcome } from "@/shared/lib/read-outcome";
import {
	Button,
	Card,
	CardContent,
	CardDescription,
	CardFooter,
	CardHeader,
	CardTitle,
	ErrorState,
	Input,
	Label,
	Page as PageBody,
	PageHeader,
	Panel,
	Spinner,
	Stack,
} from "@/shared/ui";
import {
	defaultOrgSettings,
	type OrgSettingsValues,
	orgSettingsSchema,
} from "../model/settings-schema";
import { orgMutations } from "../service/mutations";
import { orgQueries } from "../service/queries";
import { OrganizationLifecycleCard } from "./organization-lifecycle-card";
import { OrganizationProfileForm } from "./organization-profile-form";

function useOrgSettings(orgId: string) {
	return useQuery({
		...orgQueries.settings(orgId),
		select: (data): OrgSettingsValues => ({
			logoUrl: data.logoUrl ?? "",
			primaryColor: data.primaryColor ?? defaultOrgSettings.primaryColor,
			customDomain: data.customDomain ?? "",
			faviconUrl: data.faviconUrl ?? "",
		}),
	});
}

function useUpdateOrgSettings(orgId: string) {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: (values: OrgSettingsValues) =>
			orgMutations.updateSettings(orgId, values),
		onSuccess: () => {
			toast.success("Organization settings saved");
			queryClient.invalidateQueries({ queryKey: ["org-settings", orgId] });
		},
		onError: (error) => {
			toast.error(failureText("save the settings", error));
		},
	});
}

export function OrgSettingsPage() {
	const { organizationId } = useAuth();
	return (
		<PageBody>
			<PageHeader
				title="Organization settings"
				description="The organization's name, its branding and its domain."
				actions={<OrgSelector />}
			/>
			{organizationId ? (
				<>
					<OrganizationIdentity
						key={`identity-${organizationId}`}
						orgId={organizationId}
					/>
				</>
			) : (
				<EmptyState
					icon={Building2}
					title="Select an organization to edit its settings"
					description="Pick an organization from the selector above."
				/>
			)}
		</PageBody>
	);
}

/**
 * The current organization's name and slug, and the danger zone: leaving it,
 * and — for its owner or a platform super administrator — deleting it.
 */
function OrganizationIdentity({ orgId }: { orgId: string }) {
	const { orgRole, platformRole } = useAuth();
	const {
		data: organization,
		isError,
		error,
	} = useQuery(orgQueries.detail(orgId));
	if (isError) {
		return (
			<Panel>
				<ErrorState
					title="Couldn't load this organization"
					detail={
						readOutcome(true, error) === "forbidden"
							? "You don't have permission to see this organization's name and membership. Ask one of its administrators for access."
							: "Its name and the options to leave or delete it can't be shown right now. Reload the page; if it keeps failing, tell your administrator."
					}
				/>
			</Panel>
		);
	}
	if (!organization) return null;
	const identity = {
		id: organization.id,
		name: organization.name,
		slug: organization.slug,
	};
	const isAdministrator = orgRole === "owner" || orgRole === "admin";
	return (
		<>
			{(isAdministrator || platformRole === "super_admin") && (
				<OrganizationProfileForm organization={identity} />
			)}
			<OrgSettingsForm key={orgId} orgId={orgId} />
			<OrganizationLifecycleCard
				organization={identity}
				canLeave
				canDelete={orgRole === "owner" || platformRole === "super_admin"}
			/>
		</>
	);
}

function OrgSettingsForm({ orgId }: { orgId: string }) {
	const {
		data: settings,
		isLoading,
		isError,
		error,
		refetch,
	} = useOrgSettings(orgId);
	const updateMutation = useUpdateOrgSettings(orgId);

	const form = useForm<OrgSettingsValues>({
		resolver: zodResolver(orgSettingsSchema),
		defaultValues: defaultOrgSettings,
	});

	useEffect(() => {
		if (settings) {
			form.reset(settings);
		}
	}, [settings, form]);

	const onSubmit = (values: OrgSettingsValues) => {
		updateMutation.mutate(values);
	};

	if (isLoading) {
		return <Spinner label="Loading the organization settings" size="sm" />;
	}
	if (isError) {
		// A refusal is not cured by asking again, so only a read that did not
		// come back offers the retry.
		const refused = readOutcome(true, error) === "forbidden";
		return (
			<Panel>
				<Stack gap={3} align="start">
					<ErrorState
						title="Couldn't load the organization settings"
						detail={
							refused
								? "You don't have permission to see this organization's branding and domain. Ask one of its administrators for access."
								: "The branding and domain can't be edited until they load. Try again; if it keeps failing, tell your administrator."
						}
					/>
					{!refused && (
						<Button variant="outline" size="sm" onClick={() => refetch()}>
							Try again
						</Button>
					)}
				</Stack>
			</Panel>
		);
	}

	return (
		<form onSubmit={form.handleSubmit(onSubmit)}>
			<Card>
				<CardHeader>
					<CardTitle>Branding</CardTitle>
					<CardDescription>
						Configure your organization&apos;s visual identity.
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-4">
					<div className="space-y-2">
						<Label htmlFor="logoUrl">Logo URL</Label>
						<Input
							id="logoUrl"
							placeholder="https://example.com/logo.png"
							{...form.register("logoUrl")}
						/>
						{form.formState.errors.logoUrl && (
							<p className="text-sm text-destructive">
								{form.formState.errors.logoUrl.message}
							</p>
						)}
					</div>

					<div className="space-y-2">
						<Label htmlFor="primaryColor">Primary Color</Label>
						<div className="flex items-center gap-3">
							<Input
								id="primaryColor"
								placeholder={defaultOrgSettings.primaryColor}
								className="flex-1"
								{...form.register("primaryColor")}
							/>
							<Input
								type="color"
								aria-label="Pick the primary color"
								value={
									form.watch("primaryColor") || defaultOrgSettings.primaryColor
								}
								onChange={(e) => form.setValue("primaryColor", e.target.value)}
								className="h-10 w-10 shrink-0 cursor-pointer p-0.5"
							/>
						</div>
						{form.formState.errors.primaryColor && (
							<p className="text-sm text-destructive">
								{form.formState.errors.primaryColor.message}
							</p>
						)}
					</div>

					<div className="space-y-2">
						<Label htmlFor="faviconUrl">Favicon URL</Label>
						<Input
							id="faviconUrl"
							placeholder="https://example.com/favicon.ico"
							{...form.register("faviconUrl")}
						/>
						{form.formState.errors.faviconUrl && (
							<p className="text-sm text-destructive">
								{form.formState.errors.faviconUrl.message}
							</p>
						)}
					</div>
				</CardContent>

				<CardHeader>
					<CardTitle>Domain</CardTitle>
					<CardDescription>
						Set up a custom domain for your organization.
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-4">
					<div className="space-y-2">
						<Label htmlFor="customDomain">Custom Domain</Label>
						<Input
							id="customDomain"
							placeholder="app.yourdomain.com"
							{...form.register("customDomain")}
						/>
						{form.formState.errors.customDomain && (
							<p className="text-sm text-destructive">
								{form.formState.errors.customDomain.message}
							</p>
						)}
					</div>
				</CardContent>

				<CardFooter>
					<Button type="submit" disabled={updateMutation.isPending}>
						{updateMutation.isPending ? "Saving..." : "Save Settings"}
					</Button>
				</CardFooter>
			</Card>
		</form>
	);
}
