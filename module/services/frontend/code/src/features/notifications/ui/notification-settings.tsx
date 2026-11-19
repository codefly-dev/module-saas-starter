"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { toast } from "sonner";
import { Settings } from "@/features/user-settings/model/settings";
import { userSettingsMutations } from "@/features/user-settings/service/mutations";
import { userSettingsQueries } from "@/features/user-settings/service/queries";
import { failureText } from "@/shared/lib/failure-text";
import {
	Button,
	Card,
	CardContent,
	CardDescription,
	CardFooter,
	CardHeader,
	CardTitle,
	ErrorState,
	Label,
	Page as PageBody,
	PageHeader,
	Skeleton,
	Switch,
	useLoadingPhase,
} from "@/shared/ui";

export function NotificationSettings() {
	const queryClient = useQueryClient();
	const { data, isLoading, isError, error } = useQuery(
		userSettingsQueries.current(),
	);

	const { quiet, indicator } = useLoadingPhase(isLoading);
	const saving = useRef(false);
	const [draft, setDraft] = useState<boolean>();
	const inApp = draft ?? Settings.notifications.inApp.get(data);

	const save = useMutation({
		mutationFn: () =>
			userSettingsMutations.update({
				patch: {
					notifications: {
						inApp,
					},
				},
			}),
		onSuccess: async () => {
			toast.success("Notification preferences saved");
			await queryClient.invalidateQueries({ queryKey: ["user-settings"] });
		},
		onSettled: () => {
			saving.current = false;
		},
	});

	return (
		<PageBody>
			<PageHeader
				title="Notification preferences"
				description="Choose whether optional updates appear in your notification center."
			/>

			<Card>
				<CardHeader>
					<CardTitle role="heading" aria-level={2}>
						In-app notifications
					</CardTitle>
					<CardDescription>
						Push notifications and notification sounds are not available.
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-4">
					{indicator && <Skeleton className="h-16 w-full" />}
					{!quiet && !indicator && isError && (
						<ErrorState
							title="Notification preferences unavailable"
							detail={failureText("load your notification preferences", error)}
						/>
					)}
					{!quiet && !indicator && !isError && (
						<div className="flex items-center justify-between gap-4">
							<div className="space-y-0.5">
								<Label htmlFor="notif-in-app">Optional updates</Label>
								<p
									id="notif-in-app-description"
									className="text-sm text-muted-foreground"
								>
									Show product activity and invitations in the notification
									center. Essential security and billing messages still appear.
								</p>
							</div>
							<Switch
								id="notif-in-app"
								checked={inApp}
								aria-describedby="notif-in-app-description"
								disabled={save.isPending}
								onCheckedChange={setDraft}
							/>
						</div>
					)}
					{save.isError && (
						<ErrorState
							title="Preferences weren’t saved"
							detail={failureText(
								"save your notification preferences",
								save.error,
							)}
						/>
					)}
				</CardContent>
				<CardFooter>
					<Button
						onClick={() => {
							if (saving.current || isLoading || isError) return;
							saving.current = true;
							save.mutate();
						}}
						disabled={quiet || indicator || isError || save.isPending}
					>
						{save.isPending ? "Saving…" : "Save preferences"}
					</Button>
				</CardFooter>
			</Card>
		</PageBody>
	);
}
