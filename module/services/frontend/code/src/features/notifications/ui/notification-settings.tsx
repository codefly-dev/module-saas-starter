"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { toast } from "sonner";
import { Settings } from "@/features/user-settings/model/settings";
import { userSettingsMutations } from "@/features/user-settings/service/mutations";
import { userSettingsQueries } from "@/features/user-settings/service/queries";
import {
	Button,
	Card,
	CardContent,
	CardDescription,
	CardFooter,
	CardHeader,
	CardTitle,
	Label,
	Switch,
} from "@/shared/ui";

export function NotificationSettings() {
	const queryClient = useQueryClient();
	const { data, isLoading } = useQuery(userSettingsQueries.current());

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
		onError: () => toast.error("Failed to save preferences"),
	});

	return (
		<div className="space-y-6">
			<div>
				<h1 className="text-2xl font-bold tracking-tight">
					Notification Preferences
				</h1>
				<p className="text-muted-foreground">
					Choose whether optional updates appear in your notification center.
				</p>
			</div>

			<Card>
				<CardHeader>
					<CardTitle>In-app notifications</CardTitle>
					<CardDescription>
						Push notifications and notification sounds are not available.
					</CardDescription>
				</CardHeader>
				<CardContent>
					<div className="flex items-center justify-between gap-4">
						<div className="space-y-0.5">
							<Label htmlFor="notif-in-app" className="text-sm font-medium">
								Optional updates
							</Label>
							<p className="text-sm text-muted-foreground">
								Show product activity and invitations in the notification
								center. Essential security and billing messages still appear.
							</p>
						</div>
						<Switch
							id="notif-in-app"
							checked={inApp}
							disabled={isLoading}
							onCheckedChange={setDraft}
						/>
					</div>
				</CardContent>
				<CardFooter>
					<Button
						onClick={() => save.mutate()}
						disabled={isLoading || save.isPending}
					>
						{save.isPending ? "Saving..." : "Save Preferences"}
					</Button>
				</CardFooter>
			</Card>
		</div>
	);
}
