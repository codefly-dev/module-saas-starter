"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import {
	Button,
	Card,
	CardContent,
	CardDescription,
	CardFooter,
	CardHeader,
	CardTitle,
	Input,
	Label,
} from "@/shared/ui";
import {
	defaultOrgSettings,
	type OrgSettingsValues,
	orgSettingsSchema,
} from "../model/settings-schema";
import { orgMutations } from "../service/mutations";
import { orgQueries } from "../service/queries";

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
		onError: (err: Error) => {
			toast.error(err.message || "Failed to save settings");
		},
	});
}

export function OrgSettingsPage({ orgId = "default" }: { orgId?: string }) {
	const { data: settings, isLoading } = useOrgSettings(orgId);
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
		return (
			<div className="space-y-6">
				<div>
					<h1 className="text-2xl font-bold tracking-tight">
						Organization Settings
					</h1>
					<p className="text-muted-foreground">Loading...</p>
				</div>
			</div>
		);
	}

	return (
		<div className="space-y-6">
			<div>
				<h1 className="text-2xl font-bold tracking-tight">
					Organization Settings
				</h1>
				<p className="text-muted-foreground">
					Customize branding and domain settings for your organization.
				</p>
			</div>

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
									placeholder="#6366f1"
									className="flex-1"
									{...form.register("primaryColor")}
								/>
								<input
									type="color"
									value={form.watch("primaryColor") || "#6366f1"}
									onChange={(e) =>
										form.setValue("primaryColor", e.target.value)
									}
									className="h-10 w-10 cursor-pointer rounded border p-0.5"
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
		</div>
	);
}
