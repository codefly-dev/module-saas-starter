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

interface Channel {
	id: "inApp" | "push" | "sound";
	label: string;
	description: string;
}

/** The delivery channels the backend actually persists (UserNotificationSettings:
 * in_app / push / sound). Per-event granularity would need a proto extension; these
 * are the global toggles the api stores today. */
const CHANNELS: Channel[] = [
	{
		id: "inApp",
		label: "In-app",
		description: "Show notifications in the app's notification center.",
	},
	{
		id: "push",
		label: "Push",
		description: "Send push notifications to your registered devices.",
	},
	{
		id: "sound",
		label: "Sound",
		description: "Play a sound when a notification arrives.",
	},
];

type Prefs = { inApp: boolean; push: boolean; sound: boolean };

/**
 * Notification preferences — wired to the real UserSettings.notifications
 * (in_app / push / sound). Reads the current settings and persists changes via
 * UserSettingsService.Update. Nested fields merge independently.
 */
export function NotificationSettings() {
	const queryClient = useQueryClient();
	const { data, isLoading } = useQuery(userSettingsQueries.current());

	const [draft, setDraft] = useState<Partial<Prefs>>({});
	const serverPrefs: Prefs = {
		inApp: Settings.notifications.inApp.get(data),
		push: Settings.notifications.push.get(data),
		sound: Settings.notifications.sound.get(data),
	};
	const prefs: Prefs = { ...serverPrefs, ...draft };

	const save = useMutation({
		mutationFn: () =>
			userSettingsMutations.update({
				patch: {
					notifications: {
						inApp: prefs.inApp,
						push: prefs.push,
						sound: prefs.sound,
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
					Choose how you want to be notified.
				</p>
			</div>

			<Card>
				<CardHeader>
					<CardTitle>Delivery channels</CardTitle>
					<CardDescription>
						These apply to the notifications this account receives.
					</CardDescription>
				</CardHeader>
				<CardContent>
					<div className="divide-y">
						{CHANNELS.map((ch) => (
							<div
								key={ch.id}
								className="flex items-center justify-between gap-4 py-4 first:pt-0 last:pb-0"
							>
								<div className="space-y-0.5">
									<Label
										htmlFor={`notif-${ch.id}`}
										className="text-sm font-medium"
									>
										{ch.label}
									</Label>
									<p className="text-sm text-muted-foreground">
										{ch.description}
									</p>
								</div>
								<Switch
									id={`notif-${ch.id}`}
									checked={prefs[ch.id]}
									disabled={isLoading}
									onCheckedChange={(v) =>
										setDraft((current) => ({ ...current, [ch.id]: v }))
									}
								/>
							</div>
						))}
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
