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
	Field,
	Input,
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
	if (!organization)
		return <Spinner label="Loading the organization" size="sm" />;
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
			<OrgSettingsForm
				key={orgId}
				orgId={orgId}
				canEdit={isAdministrator || platformRole === "super_admin"}
			/>
			<OrganizationLifecycleCard
				organization={identity}
				canLeave
				canDelete={orgRole === "owner" || platformRole === "super_admin"}
			/>
		</>
	);
}

function OrgSettingsForm({
	orgId,
	canEdit,
}: {
	orgId: string;
	canEdit: boolean;
}) {
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
		<form
			noValidate
			onSubmit={form.handleSubmit((values) => {
				if (canEdit && !updateMutation.isPending) onSubmit(values);
			})}
		>
			<Card>
				<CardHeader>
					<CardTitle>Branding</CardTitle>
					<CardDescription>
						Configure your organization&apos;s visual identity.
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-4">
					<Field
						label="Logo URL"
						error={form.formState.errors.logoUrl?.message}
					>
						{(control) => (
							<Input
								{...control}
								placeholder="https://example.com/logo.png"
								{...form.register("logoUrl")}
								disabled={!canEdit || updateMutation.isPending}
							/>
						)}
					</Field>
					<Field
						label="Primary color"
						error={form.formState.errors.primaryColor?.message}
					>
						{(control) => (
							<div className="flex items-center gap-3">
								<Input
									{...control}
									placeholder={defaultOrgSettings.primaryColor}
									{...form.register("primaryColor")}
									disabled={!canEdit || updateMutation.isPending}
								/>
								<Input
									type="color"
									aria-label="Pick the primary color"
									value={
										form.watch("primaryColor") ||
										defaultOrgSettings.primaryColor
									}
									onChange={(e) =>
										form.setValue("primaryColor", e.target.value, {
											shouldDirty: true,
											shouldValidate: true,
										})
									}
									disabled={!canEdit || updateMutation.isPending}
									className="h-10 w-10 shrink-0 cursor-pointer p-0.5"
								/>
							</div>
						)}
					</Field>
					<Field
						label="Browser icon URL"
						error={form.formState.errors.faviconUrl?.message}
					>
						{(control) => (
							<Input
								{...control}
								placeholder="https://example.com/favicon.ico"
								{...form.register("faviconUrl")}
								disabled={!canEdit || updateMutation.isPending}
							/>
						)}
					</Field>
				</CardContent>

				<CardHeader>
					<CardTitle>Domain</CardTitle>
					<CardDescription>
						Set up a custom domain for your organization.
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-4">
					<Field
						label="Custom domain"
						error={form.formState.errors.customDomain?.message}
					>
						{(control) => (
							<Input
								{...control}
								placeholder="app.example.com"
								{...form.register("customDomain")}
								disabled={!canEdit || updateMutation.isPending}
							/>
						)}
					</Field>
					{updateMutation.isError && (
						<ErrorState
							title="Couldn't save the settings"
							detail={failureText("save the settings", updateMutation.error)}
						/>
					)}
				</CardContent>

				<CardFooter className="flex flex-wrap gap-3">
					{!canEdit && (
						<p className="text-sm text-muted-foreground">
							An organization administrator can update these settings.
						</p>
					)}
					<Button type="submit" disabled={!canEdit || updateMutation.isPending}>
						{updateMutation.isPending ? "Saving..." : "Save settings"}
					</Button>
				</CardFooter>
			</Card>
		</form>
	);
}
