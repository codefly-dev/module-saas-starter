"use client";

import type { FrontendThemePreference } from "@codefly/saas-plugin-contract";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { toast } from "sonner";
import type { UserSettings } from "@/gen/saas/accounts/v1/user_settings_pb";
import {
	Button,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
	Checkbox,
	Input,
	Label,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
	Skeleton,
} from "@/shared/ui";
import { themePreferenceFromProto } from "../model/theme-preference";
import { userSettingsMutations } from "../service/mutations";
import { userSettingsQueries } from "../service/queries";
import { useThemePreference } from "./theme-preference-provider";

/**
 * GeneralSettingsPage — the top-level /settings hub. Surfaces the
 * preferences blob (theme / locale / timezone / formats / email
 * opt-ins) in three cards, each with an inline Save so the operator
 * can update one section without committing the others.
 *
 * Theme syncs both ways: applying the theme via next-themes also
 * persists it to the api on save, so a fresh device login restores
 * the user's preference.
 */
export function GeneralSettingsPage() {
	const { data: settings, isLoading } = useQuery(userSettingsQueries.current());
	if (isLoading) {
		return (
			<div className="space-y-4 max-w-3xl">
				<Skeleton className="h-48 w-full" />
				<Skeleton className="h-48 w-full" />
				<Skeleton className="h-48 w-full" />
			</div>
		);
	}
	const formIdentity = JSON.stringify([
		settings?.theme,
		settings?.locale,
		settings?.timezone,
		settings?.dateFormat,
		settings?.timeFormat,
		settings?.email?.product,
		settings?.email?.marketing,
		settings?.email?.weeklyDigest,
	]);
	return <GeneralSettingsForm key={formIdentity} settings={settings} />;
}

function GeneralSettingsForm({
	settings,
}: {
	settings: UserSettings | undefined;
}) {
	const queryClient = useQueryClient();
	const { setPreference, isSaving: isSavingTheme } = useThemePreference();

	// Local form state. Initialized from the api on load; saves below
	// submit a partial patch with only the keys this card owns.
	const [theme, setLocalTheme] = useState<FrontendThemePreference>(
		themePreferenceFromProto(settings?.theme) ?? "system",
	);
	const [locale, setLocale] = useState(settings?.locale ?? "en");
	const [timezone, setTimezone] = useState(settings?.timezone ?? "UTC");
	const [dateFormat, setDateFormat] = useState(settings?.dateFormat ?? "iso");
	const [timeFormat, setTimeFormat] = useState(settings?.timeFormat ?? "24h");

	const [emailProduct, setEmailProduct] = useState(
		settings?.email?.product ?? true,
	);
	const [emailMarketing, setEmailMarketing] = useState(
		settings?.email?.marketing ?? false,
	);
	const [emailWeeklyDigest, setEmailWeeklyDigest] = useState(
		settings?.email?.weeklyDigest ?? true,
	);

	const save = useMutation({ mutationFn: userSettingsMutations.update });

	async function persist(
		operation: () => Promise<unknown>,
		successMessage = "Settings saved",
	) {
		try {
			await operation();
			toast.success(successMessage);
			await queryClient.invalidateQueries({ queryKey: ["user-settings"] });
		} catch (error) {
			toast.error("Save failed", {
				description: error instanceof Error ? error.message : "Try again.",
			});
		}
	}

	function saveAppearance() {
		void persist(async () => {
			await setPreference(theme);
			await save.mutateAsync({ locale });
		});
	}

	function saveTimezone() {
		void persist(() => save.mutateAsync({ timezone, dateFormat, timeFormat }));
	}

	function saveEmail() {
		void persist(() =>
			save.mutateAsync({
				email: {
					$typeName: "saas.accounts.v1.UserEmailSettings",
					product: emailProduct,
					marketing: emailMarketing,
					weeklyDigest: emailWeeklyDigest,
					// security forced-on server-side; we don't send it.
				},
			}),
		);
	}

	return (
		<div className="space-y-6 max-w-3xl">
			<div>
				<h1 className="text-2xl font-bold tracking-tight">Preferences</h1>
				<p className="text-muted-foreground">
					Theme, locale, and email preferences. Synced across all your devices.
				</p>
			</div>

			{/* ── Appearance ── */}
			<Card>
				<CardHeader>
					<CardTitle>Appearance</CardTitle>
					<CardDescription>
						Theme follows your OS by default. Pick a fixed theme to override.
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-4">
					<div className="grid grid-cols-2 gap-4 max-w-md">
						<div className="space-y-2">
							<Label htmlFor="theme">Theme</Label>
							<Select
								value={theme}
								onValueChange={(value) => {
									if (
										value === "system" ||
										value === "light" ||
										value === "dark"
									)
										setLocalTheme(value);
								}}
							>
								<SelectTrigger id="theme">
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									<SelectItem value="system">System</SelectItem>
									<SelectItem value="light">Light</SelectItem>
									<SelectItem value="dark">Dark</SelectItem>
								</SelectContent>
							</Select>
						</div>
						<div className="space-y-2">
							<Label htmlFor="locale">Language</Label>
							<Select value={locale} onValueChange={(v) => v && setLocale(v)}>
								<SelectTrigger id="locale">
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									<SelectItem value="en">English</SelectItem>
									<SelectItem value="es">Español</SelectItem>
									<SelectItem value="fr">Français</SelectItem>
									<SelectItem value="de">Deutsch</SelectItem>
									<SelectItem value="ja">日本語</SelectItem>
								</SelectContent>
							</Select>
						</div>
					</div>
					<div className="flex justify-end">
						<Button
							onClick={saveAppearance}
							disabled={save.isPending || isSavingTheme}
						>
							{save.isPending || isSavingTheme ? "Saving…" : "Save appearance"}
						</Button>
					</div>
				</CardContent>
			</Card>

			{/* ── Time & date ── */}
			<Card>
				<CardHeader>
					<CardTitle>Time & date</CardTitle>
					<CardDescription>
						How dates render across the product. Affects audit log, billing
						cycles, webhook delivery timestamps.
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-4">
					<div className="grid grid-cols-3 gap-4">
						<div className="space-y-2 col-span-2">
							<Label htmlFor="tz">Time zone (IANA)</Label>
							<Input
								id="tz"
								placeholder="America/New_York"
								value={timezone}
								onChange={(e) => setTimezone(e.target.value)}
							/>
						</div>
						<div className="space-y-2">
							<Label htmlFor="time-format">Time format</Label>
							<Select
								value={timeFormat}
								onValueChange={(v) => v && setTimeFormat(v)}
							>
								<SelectTrigger id="time-format">
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									<SelectItem value="24h">24-hour</SelectItem>
									<SelectItem value="12h">12-hour</SelectItem>
								</SelectContent>
							</Select>
						</div>
					</div>
					<div className="space-y-2 max-w-xs">
						<Label htmlFor="date-format">Date format</Label>
						<Select
							value={dateFormat}
							onValueChange={(v) => v && setDateFormat(v)}
						>
							<SelectTrigger id="date-format">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								<SelectItem value="iso">ISO (2026-04-25)</SelectItem>
								<SelectItem value="us">US (04/25/2026)</SelectItem>
								<SelectItem value="eu">EU (25/04/2026)</SelectItem>
							</SelectContent>
						</Select>
					</div>
					<div className="flex justify-end">
						<Button onClick={saveTimezone} disabled={save.isPending}>
							{save.isPending ? "Saving…" : "Save time & date"}
						</Button>
					</div>
				</CardContent>
			</Card>

			{/* ── Email ── */}
			<Card>
				<CardHeader>
					<CardTitle>Email preferences</CardTitle>
					<CardDescription>
						Macro categories for transactional email. Per-event overrides live
						in <code>/settings/notifications</code>. Security alerts are always
						on.
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-3">
					<label
						htmlFor="email-product"
						className="flex items-start gap-3 cursor-pointer"
					>
						<Checkbox
							id="email-product"
							checked={emailProduct}
							onCheckedChange={(v) => setEmailProduct(v === true)}
						/>
						<div className="space-y-0.5">
							<div className="font-medium text-sm">Product updates</div>
							<div className="text-xs text-muted-foreground">
								New features, important changes, planned maintenance.
							</div>
						</div>
					</label>
					<label
						htmlFor="email-weekly-digest"
						className="flex items-start gap-3 cursor-pointer"
					>
						<Checkbox
							id="email-weekly-digest"
							checked={emailWeeklyDigest}
							onCheckedChange={(v) => setEmailWeeklyDigest(v === true)}
						/>
						<div className="space-y-0.5">
							<div className="font-medium text-sm">Weekly digest</div>
							<div className="text-xs text-muted-foreground">
								Quiet rollup of activity across your orgs once a week.
							</div>
						</div>
					</label>
					<label
						htmlFor="email-marketing"
						className="flex items-start gap-3 cursor-pointer"
					>
						<Checkbox
							id="email-marketing"
							checked={emailMarketing}
							onCheckedChange={(v) => setEmailMarketing(v === true)}
						/>
						<div className="space-y-0.5">
							<div className="font-medium text-sm">Marketing</div>
							<div className="text-xs text-muted-foreground">
								Newsletters and promotions. Off by default.
							</div>
						</div>
					</label>
					<label
						htmlFor="email-security"
						className="flex items-start gap-3 opacity-60 cursor-not-allowed"
					>
						<Checkbox id="email-security" checked disabled />
						<div className="space-y-0.5">
							<div className="font-medium text-sm">Security alerts</div>
							<div className="text-xs text-muted-foreground">
								Always on. Sign-in from a new device, password / MFA changes.
							</div>
						</div>
					</label>
					<div className="flex justify-end pt-2">
						<Button onClick={saveEmail} disabled={save.isPending}>
							{save.isPending ? "Saving…" : "Save email preferences"}
						</Button>
					</div>
				</CardContent>
			</Card>
		</div>
	);
}
