"use client";

import type { FrontendThemePreference } from "@codefly/saas-plugin-contract";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { toast } from "sonner";
import { profileInitials } from "@/features/user-profile/model/profile";
import { USER_PROFILE_QUERY_KEY } from "@/features/user-profile/service/client";
import { userProfileMutations } from "@/features/user-profile/service/mutations";
import { userProfileQueries } from "@/features/user-profile/service/queries";
import type { GetSelfResponse } from "@/gen/saas/accounts/v1/identity_pb";
import type { UserSettings } from "@/gen/saas/accounts/v1/user_settings_pb";
import {
	Avatar,
	AvatarFallback,
	AvatarImage,
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
	Textarea,
} from "@/shared/ui";
import { Settings } from "../model/settings";
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
 * Theme syncs both ways: applying the theme via the client provider also
 * persists it to the api on save, so a fresh device login restores
 * the user's preference.
 */
export function GeneralSettingsPage() {
	const { data: settings, isLoading } = useQuery(userSettingsQueries.current());
	const { data: self, isLoading: isProfileLoading } = useQuery(
		userProfileQueries.current(),
	);
	if (isLoading || isProfileLoading) {
		return (
			<div className="space-y-4 max-w-3xl">
				<Skeleton className="h-64 w-full" />
				<Skeleton className="h-48 w-full" />
				<Skeleton className="h-48 w-full" />
				<Skeleton className="h-48 w-full" />
			</div>
		);
	}
	const formIdentity = JSON.stringify([
		Settings.appearance.theme.get(settings),
		Settings.regional.locale.get(settings),
		Settings.regional.timezone.get(settings),
		Settings.regional.dateFormat.get(settings),
		Settings.regional.timeFormat.get(settings),
		Settings.email.product.get(settings),
		Settings.email.marketing.get(settings),
		Settings.email.weeklyDigest.get(settings),
		self?.user?.uuid,
		self?.user?.primaryEmail,
		self?.user?.profile,
	]);
	return (
		<GeneralSettingsForm key={formIdentity} settings={settings} self={self} />
	);
}

function GeneralSettingsForm({
	settings,
	self,
}: {
	settings: UserSettings | undefined;
	self: GetSelfResponse | undefined;
}) {
	const queryClient = useQueryClient();
	const { setPreference, isSaving: isSavingTheme } = useThemePreference();

	// Local form state. Initialized from the api on load; saves below
	// submit a partial patch with only the keys this card owns.
	const [theme, setLocalTheme] = useState<FrontendThemePreference>(
		themePreferenceFromProto(Settings.appearance.theme.get(settings)) ??
			"system",
	);
	const [locale, setLocale] = useState(Settings.regional.locale.get(settings));
	const [timezone, setTimezone] = useState(
		Settings.regional.timezone.get(settings),
	);
	const [dateFormat, setDateFormat] = useState(
		Settings.regional.dateFormat.get(settings),
	);
	const [timeFormat, setTimeFormat] = useState(
		Settings.regional.timeFormat.get(settings),
	);

	const [emailProduct, setEmailProduct] = useState(
		Settings.email.product.get(settings),
	);
	const [emailMarketing, setEmailMarketing] = useState(
		Settings.email.marketing.get(settings),
	);
	const [emailWeeklyDigest, setEmailWeeklyDigest] = useState(
		Settings.email.weeklyDigest.get(settings),
	);
	const profile = self?.user?.profile;
	const [profileName, setProfileName] = useState(profile?.name ?? "");
	const [profileDisplayName, setProfileDisplayName] = useState(
		profile?.display_name ?? "",
	);
	const [profileAvatarURL, setProfileAvatarURL] = useState(
		profile?.avatar_url ?? "",
	);
	const [profileTitle, setProfileTitle] = useState(profile?.title ?? "");
	const [profileBio, setProfileBio] = useState(profile?.bio ?? "");
	const [profilePhone, setProfilePhone] = useState(profile?.phone ?? "");
	const [profileLocation, setProfileLocation] = useState(
		profile?.location ?? "",
	);
	const [profileTimezone, setProfileTimezone] = useState(
		profile?.timezone ?? "",
	);

	const save = useMutation({ mutationFn: userSettingsMutations.update });
	const saveProfile = useMutation({
		mutationFn: () =>
			userProfileMutations.update({
				name: profileName.trim(),
				display_name: profileDisplayName.trim(),
				avatar_url: profileAvatarURL.trim(),
				title: profileTitle.trim(),
				bio: profileBio.trim(),
				phone: profilePhone.trim(),
				location: profileLocation.trim(),
				timezone: profileTimezone.trim(),
			}),
	});

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
			await save.mutateAsync({
				patch: Settings.regional.locale.patch(locale),
			});
		});
	}

	function saveTimezone() {
		void persist(() =>
			save.mutateAsync({
				patch: { regional: { timezone, dateFormat, timeFormat } },
			}),
		);
	}

	function saveEmail() {
		void persist(() =>
			save.mutateAsync({
				patch: {
					email: {
						product: emailProduct,
						marketing: emailMarketing,
						weeklyDigest: emailWeeklyDigest,
						// security forced-on server-side; we don't send it.
					},
				},
			}),
		);
	}

	function saveUserProfile() {
		void persist(async () => {
			await saveProfile.mutateAsync();
			await queryClient.invalidateQueries({
				queryKey: USER_PROFILE_QUERY_KEY,
			});
		}, "Profile saved");
	}

	return (
		<div className="space-y-6 max-w-3xl">
			<div>
				<h1 className="text-2xl font-bold tracking-tight">Preferences</h1>
				<p className="text-muted-foreground">
					Theme, locale, and email preferences. Synced across all your devices.
				</p>
			</div>

			{/* ── Profile ── */}
			<Card>
				<CardHeader>
					<CardTitle>Profile</CardTitle>
					<CardDescription>
						Your public identity across organizations and team activity.
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-5">
					<div className="flex items-center gap-4">
						<Avatar data-size="lg">
							<AvatarImage src={profileAvatarURL || undefined} />
							<AvatarFallback>
								{profileInitials(
									profileDisplayName || profileName || self?.user?.primaryEmail,
								)}
							</AvatarFallback>
						</Avatar>
						<div className="grid flex-1 gap-4 sm:grid-cols-2">
							<div className="space-y-2">
								<Label htmlFor="profile-name">Full name</Label>
								<Input
									id="profile-name"
									value={profileName}
									onChange={(event) => setProfileName(event.target.value)}
									placeholder="Ada Lovelace"
								/>
							</div>
							<div className="space-y-2">
								<Label htmlFor="profile-display-name">Display name</Label>
								<Input
									id="profile-display-name"
									value={profileDisplayName}
									onChange={(event) =>
										setProfileDisplayName(event.target.value)
									}
									placeholder="ada"
								/>
							</div>
						</div>
					</div>
					<div className="grid gap-4 sm:grid-cols-2">
						<div className="space-y-2 sm:col-span-2">
							<Label htmlFor="profile-avatar">Avatar URL</Label>
							<Input
								id="profile-avatar"
								value={profileAvatarURL}
								onChange={(event) => setProfileAvatarURL(event.target.value)}
								placeholder="https://example.com/avatar.png"
							/>
						</div>
						<div className="space-y-2">
							<Label htmlFor="profile-title">Title</Label>
							<Input
								id="profile-title"
								value={profileTitle}
								onChange={(event) => setProfileTitle(event.target.value)}
								placeholder="Engineering Manager"
							/>
						</div>
						<div className="space-y-2">
							<Label htmlFor="profile-phone">Phone</Label>
							<Input
								id="profile-phone"
								value={profilePhone}
								onChange={(event) => setProfilePhone(event.target.value)}
								placeholder="+1 555 0100"
							/>
						</div>
						<div className="space-y-2">
							<Label htmlFor="profile-location">Location</Label>
							<Input
								id="profile-location"
								value={profileLocation}
								onChange={(event) => setProfileLocation(event.target.value)}
								placeholder="New York, NY"
							/>
						</div>
						<div className="space-y-2">
							<Label htmlFor="profile-timezone">Profile time zone</Label>
							<Input
								id="profile-timezone"
								value={profileTimezone}
								onChange={(event) => setProfileTimezone(event.target.value)}
								placeholder="America/New_York"
							/>
						</div>
						<div className="space-y-2 sm:col-span-2">
							<Label htmlFor="profile-bio">Bio</Label>
							<Textarea
								id="profile-bio"
								value={profileBio}
								onChange={(event) => setProfileBio(event.target.value)}
								rows={3}
								placeholder="A short note teammates see on your profile."
							/>
						</div>
					</div>
					<div className="flex justify-end">
						<Button onClick={saveUserProfile} disabled={saveProfile.isPending}>
							{saveProfile.isPending ? "Saving…" : "Save profile"}
						</Button>
					</div>
				</CardContent>
			</Card>

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
						Invitation emails use the product setting. Other optional categories
						apply when a matching workflow is configured. Security and billing
						messages are always on.
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
								Invitations and configured product updates.
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
								Applied when a digest workflow is configured.
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
								Applied to configured newsletters and promotions. Off by
								default.
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
