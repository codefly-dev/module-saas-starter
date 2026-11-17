"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
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
	Switch,
} from "@/shared/ui";

export function NotificationSettings() {
	const queryClient = useQueryClient();
	const { data, isLoading, isError } = useQuery(userSettingsQueries.current());

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
		onSuccess: () => {
			toast.success("Notification preferences saved");
			queryClient.invalidateQueries({ queryKey: ["user-settings"] });
		},
		onError: (error) =>
			toast.error(failureText("save your notification preferences", error)),
	});

	return (
		<PageBody>
			<PageHeader
				title="Notification preferences"
				description="Choose whether optional updates appear in your notification center."
			/>

			<Card>
				<CardHeader>
					<CardTitle>In-app notifications</CardTitle>
					<CardDescription>
						Push notifications and notification sounds are not available.
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-4">
					{/* The switch shows a default when the read fails, so it is locked
					    and the page says the real setting is unknown. */}
					{isError && (
						<ErrorState
							title="Couldn't load your notification preferences"
							detail="Your current choice is unknown, so it can't be changed here yet. Reload the page; if it keeps failing, tell your administrator."
						/>
					)}
					<div className="flex items-center justify-between gap-4">
						<div className="space-y-0.5">
							<Label htmlFor="notif-in-app">Optional updates</Label>
							<p className="text-sm text-muted-foreground">
								Show product activity and invitations in the notification
								center. Essential security and billing messages still appear.
							</p>
						</div>
						<Switch
							id="notif-in-app"
							checked={inApp}
							disabled={isLoading || isError}
							onCheckedChange={setDraft}
						/>
					</div>
				</CardContent>
				<CardFooter>
					<Button
						onClick={() => save.mutate()}
						disabled={isLoading || isError || save.isPending}
					>
						{save.isPending ? "Saving…" : "Save preferences"}
					</Button>
				</CardFooter>
			</Card>
		</PageBody>
	);
}
