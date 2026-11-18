"use client";

import type { FrontendThemePreference } from "@codefly-dev/saas-plugin-contract";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { toast } from "sonner";
import { profileInitials } from "@/features/user-profile/model/profile";
import { USER_PROFILE_QUERY_KEY } from "@/features/user-profile/service/client";
import { userProfileMutations } from "@/features/user-profile/service/mutations";
import { userProfileQueries } from "@/features/user-profile/service/queries";
import type { GetSelfResponse } from "@/gen/saas/accounts/v1/identity_pb";
import type { UserSettings } from "@/gen/saas/accounts/v1/user_settings_pb";
import { failureText } from "@/shared/lib/failure-text";
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
	ErrorState,
	Field,
	Input,
	Label,
	Page as PageBody,
	PageHeader,
	Panel,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
	Skeleton,
	Textarea,
	useLoadingPhase,
} from "@/shared/ui";
import { Settings } from "../model/settings";
import { themePreferenceFromProto } from "../model/theme-preference";
import { userSettingsMutations } from "../service/mutations";
import { userSettingsQueries } from "../service/queries";
import { useThemePreference } from "./theme-preference-provider";

const HEADER = (
	<PageHeader
		title="Preferences"
		description="Your profile, appearance, time and email preferences. They follow you to every device you sign in on."
	/>
);

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
	const {
		data: settings,
		isLoading,
		isError: settingsFailed,
		error: settingsError,
	} = useQuery(userSettingsQueries.current());
	const {
		data: self,
		isLoading: isProfileLoading,
		isError: profileFailed,
		error: profileError,
	} = useQuery(userProfileQueries.current());
	const { indicator, quiet } = useLoadingPhase(isLoading || isProfileLoading);
	if (quiet || indicator) {
		return (
			<PageBody>
				{HEADER}
				{indicator && <Skeleton className="h-64 w-full" />}
				{indicator && <Skeleton className="h-48 w-full" />}
				{indicator && <Skeleton className="h-48 w-full" />}
				{indicator && <Skeleton className="h-48 w-full" />}
			</PageBody>
		);
	}
	// A form filled with defaults because the read failed would look like the
	// person's own choices, and saving it would overwrite them.
	if (settingsFailed || profileFailed) {
		return (
			<PageBody>
				{HEADER}
				<Panel>
					<ErrorState
						title="Couldn't load your preferences"
						detail={failureText(
							"load your preferences",
							settingsFailed ? settingsError : profileError,
						)}
					/>
				</Panel>
			</PageBody>
		);
	}

	return (
		<GeneralSettingsForm
			key={self?.user?.uuid}
			settings={settings}
			self={self}
		/>
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
	// Keep drafts until this person leaves the page. Saving one section must not
	// discard unfinished edits in another when its query refreshes.
	const busy = useRef(false);
	const [savingSection, setSavingSection] = useState<string | null>(null);
	const [failure, setFailure] = useState<{
		section: string;
		message: string;
	} | null>(null);
	const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
	const isSaving = savingSection !== null;
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
		section: string,
		operation: () => Promise<unknown>,
		successMessage = "Settings saved",
	) {
		if (busy.current) return;
		busy.current = true;
		setSavingSection(section);
		setFailure(null);
		try {
			await operation();
			toast.success(successMessage);
			await queryClient.invalidateQueries({ queryKey: ["user-settings"] });
		} catch (error) {
			setFailure({ section, message: failureText("save your changes", error) });
		} finally {
			busy.current = false;
			setSavingSection(null);
		}
	}

	function saveAppearance() {
		void persist("appearance", async () => {
			await setPreference(theme);
			await save.mutateAsync({
				patch: Settings.regional.locale.patch(locale),
			});
		});
	}

	function saveTimezone() {
		try {
			new Intl.DateTimeFormat(undefined, { timeZone: timezone.trim() });
		} catch {
			setFieldErrors({ tz: "Enter a valid time zone, such as Europe/Paris." });
			return;
		}
		setFieldErrors({});
		void persist("regional", () =>
			save.mutateAsync({
				patch: {
					regional: { timezone: timezone.trim(), dateFormat, timeFormat },
				},
			}),
		);
	}

	function saveEmail() {
		void persist("email", () =>
			save.mutateAsync({
				patch: {
					email: {
						product: emailProduct,
						marketing: emailMarketing,
						weeklyDigest: emailWeeklyDigest,
					},
				},
			}),
		);
	}

	function saveUserProfile() {
		const errors: Record<string, string> = {};
		if (profileAvatarURL.trim()) {
			try {
				const url = new URL(profileAvatarURL);
				if (url.protocol !== "https:" && url.protocol !== "http:")
					throw new Error();
			} catch {
				errors["profile-avatar"] =
					"Enter a complete image address beginning with https:// or http://.";
			}
		}
		if (profileTimezone.trim()) {
			try {
				new Intl.DateTimeFormat(undefined, {
					timeZone: profileTimezone.trim(),
				});
			} catch {
				errors["profile-timezone"] =
					"Enter a valid time zone, such as Europe/Paris.";
			}
		}
		setFieldErrors(errors);
		if (Object.keys(errors).length) return;
		void persist(
			"profile",
			async () => {
				await saveProfile.mutateAsync();
				await queryClient.invalidateQueries({
					queryKey: USER_PROFILE_QUERY_KEY,
				});
			},
			"Profile saved",
		);
	}

	return (
		<PageBody>
			{HEADER}

			{/* ── Profile ── */}
			<Card>
				<CardHeader>
					<CardTitle>Profile</CardTitle>
					<CardDescription>
						Your public identity across organizations and team activity.
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-5">
					<form
						aria-label="Profile"
						className="contents"
						onSubmit={(event) => {
							event.preventDefault();
							saveUserProfile();
						}}
					>
						{failure?.section === "profile" && (
							<ErrorState title="Changes not saved" detail={failure.message} />
						)}
						<div className="flex items-center gap-4">
							<Avatar data-size="lg">
								<AvatarImage src={profileAvatarURL || undefined} />
								<AvatarFallback>
									{profileInitials(
										profileDisplayName ||
											profileName ||
											self?.user?.primaryEmail,
									)}
								</AvatarFallback>
							</Avatar>
							<div className="grid flex-1 gap-4 sm:grid-cols-2">
								<Field
									label="Full name"
									className="space-y-2"
									error={fieldErrors["profile-name"]}
								>
									{(control) => (
										<Input
											disabled={isSaving}
											{...control}
											value={profileName}
											onChange={(event) => setProfileName(event.target.value)}
											placeholder="Jane Doe"
										/>
									)}
								</Field>
								<Field
									label="Display name"
									className="space-y-2"
									error={fieldErrors["profile-display-name"]}
								>
									{(control) => (
										<Input
											disabled={isSaving}
											{...control}
											value={profileDisplayName}
											onChange={(event) =>
												setProfileDisplayName(event.target.value)
											}
											placeholder="jane"
										/>
									)}
								</Field>
							</div>
						</div>
						<div className="grid gap-4 sm:grid-cols-2">
							<Field
								label="Profile picture link"
								className="space-y-2 sm:col-span-2"
								error={fieldErrors["profile-avatar"]}
							>
								{(control) => (
									<Input
										disabled={isSaving}
										{...control}
										value={profileAvatarURL}
										onChange={(event) =>
											setProfileAvatarURL(event.target.value)
										}
										placeholder="https://example.com/avatar.png"
									/>
								)}
							</Field>
							<Field
								label="Title"
								className="space-y-2"
								error={fieldErrors["profile-title"]}
							>
								{(control) => (
									<Input
										disabled={isSaving}
										{...control}
										value={profileTitle}
										onChange={(event) => setProfileTitle(event.target.value)}
										placeholder="Engineering Manager"
									/>
								)}
							</Field>
							<Field
								label="Phone"
								className="space-y-2"
								error={fieldErrors["profile-phone"]}
							>
								{(control) => (
									<Input
										disabled={isSaving}
										{...control}
										value={profilePhone}
										onChange={(event) => setProfilePhone(event.target.value)}
										placeholder="+1 555 0100"
									/>
								)}
							</Field>
							<Field
								label="Location"
								className="space-y-2"
								error={fieldErrors["profile-location"]}
							>
								{(control) => (
									<Input
										disabled={isSaving}
										{...control}
										value={profileLocation}
										onChange={(event) => setProfileLocation(event.target.value)}
										placeholder="New York, NY"
									/>
								)}
							</Field>
							<Field
								label="Profile time zone"
								className="space-y-2"
								error={fieldErrors["profile-timezone"]}
							>
								{(control) => (
									<Input
										disabled={isSaving}
										{...control}
										value={profileTimezone}
										onChange={(event) => setProfileTimezone(event.target.value)}
										placeholder="America/New_York"
									/>
								)}
							</Field>
							<Field
								label="Bio"
								className="space-y-2 sm:col-span-2"
								error={fieldErrors["profile-bio"]}
							>
								{(control) => (
									<Textarea
										disabled={isSaving}
										{...control}
										value={profileBio}
										onChange={(event) => setProfileBio(event.target.value)}
										rows={3}
										placeholder="A short note teammates see on your profile."
									/>
								)}
							</Field>
						</div>
						<div className="flex justify-end">
							<Button type="submit" disabled={isSaving}>
								{saveProfile.isPending ? "Saving…" : "Save profile"}
							</Button>
						</div>
					</form>
				</CardContent>
			</Card>

			{/* ── Appearance ── */}
			<Card>
				<CardHeader>
					<CardTitle>Appearance</CardTitle>
					<CardDescription>
						The theme follows your device by default. Pick light or dark to keep
						it fixed.
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-4">
					<form
						aria-label="Appearance"
						className="contents"
						onSubmit={(event) => {
							event.preventDefault();
							saveAppearance();
						}}
					>
						{failure?.section === "appearance" && (
							<ErrorState title="Changes not saved" detail={failure.message} />
						)}
						<div className="grid gap-4 sm:grid-cols-2 max-w-md">
							<Field label="Theme" className="space-y-2">
								{(control) => (
									<Select
										disabled={isSaving}
										value={theme}
										items={{ system: "System", light: "Light", dark: "Dark" }}
										onValueChange={(value) => {
											if (
												value === "system" ||
												value === "light" ||
												value === "dark"
											)
												setLocalTheme(value);
										}}
									>
										<SelectTrigger {...control}>
											<SelectValue />
										</SelectTrigger>
										<SelectContent>
											<SelectItem value="system">System</SelectItem>
											<SelectItem value="light">Light</SelectItem>
											<SelectItem value="dark">Dark</SelectItem>
										</SelectContent>
									</Select>
								)}
							</Field>
							<Field label="Language" className="space-y-2">
								{(control) => (
									<Select
										disabled={isSaving}
										items={{
											en: "English",
											es: "Español",
											fr: "Français",
											de: "Deutsch",
											ja: "日本語",
										}}
										value={locale}
										onValueChange={(v) => v && setLocale(v)}
									>
										<SelectTrigger {...control}>
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
								)}
							</Field>
						</div>
						<div className="flex justify-end">
							<Button
								variant="outline"
								type="submit"
								disabled={isSaving || isSavingTheme}
							>
								{save.isPending || isSavingTheme
									? "Saving…"
									: "Save appearance"}
							</Button>
						</div>
					</form>
				</CardContent>
			</Card>

			{/* ── Time & date ── */}
			<Card>
				<CardHeader>
					<CardTitle>Time & date</CardTitle>
					<CardDescription>
						How dates and times are shown to you across the product.
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-4">
					<form
						aria-label="Time and date"
						className="contents"
						onSubmit={(event) => {
							event.preventDefault();
							saveTimezone();
						}}
					>
						{failure?.section === "regional" && (
							<ErrorState title="Changes not saved" detail={failure.message} />
						)}
						<div className="grid gap-4 sm:grid-cols-3">
							<Field
								label="Time zone"
								className="space-y-2 sm:col-span-2"
								error={fieldErrors["tz"]}
							>
								{(control) => (
									<Input
										disabled={isSaving}
										{...control}
										placeholder="America/New_York"
										value={timezone}
										onChange={(e) => setTimezone(e.target.value)}
									/>
								)}
							</Field>
							<Field label="Time format" className="space-y-2">
								{(control) => (
									<Select
										disabled={isSaving}
										value={timeFormat}
										items={{ "24h": "24-hour", "12h": "12-hour" }}
										onValueChange={(v) => v && setTimeFormat(v)}
									>
										<SelectTrigger {...control}>
											<SelectValue />
										</SelectTrigger>
										<SelectContent>
											<SelectItem value="24h">24-hour</SelectItem>
											<SelectItem value="12h">12-hour</SelectItem>
										</SelectContent>
									</Select>
								)}
							</Field>
						</div>
						<Field label="Date format" className="space-y-2 max-w-xs">
							{(control) => (
								<Select
									disabled={isSaving}
									value={dateFormat}
									items={{
										iso: "ISO (2026-04-25)",
										us: "US (04/25/2026)",
										eu: "EU (25/04/2026)",
									}}
									onValueChange={(v) => v && setDateFormat(v)}
								>
									<SelectTrigger {...control}>
										<SelectValue />
									</SelectTrigger>
									<SelectContent>
										<SelectItem value="iso">ISO (2026-04-25)</SelectItem>
										<SelectItem value="us">US (04/25/2026)</SelectItem>
										<SelectItem value="eu">EU (25/04/2026)</SelectItem>
									</SelectContent>
								</Select>
							)}
						</Field>
						<div className="flex justify-end">
							<Button variant="outline" type="submit" disabled={isSaving}>
								{save.isPending ? "Saving…" : "Save time & date"}
							</Button>
						</div>
					</form>
				</CardContent>
			</Card>

			{/* ── Email ── */}
			<Card>
				<CardHeader>
					<CardTitle>Email preferences</CardTitle>
					<CardDescription>
						Choose which optional emails you receive. Invitations are sent with
						product updates. Billing lifecycle messages are always on.
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-3">
					<form
						aria-label="Email preferences"
						className="contents"
						onSubmit={(event) => {
							event.preventDefault();
							saveEmail();
						}}
					>
						{failure?.section === "email" && (
							<ErrorState title="Changes not saved" detail={failure.message} />
						)}
						<div className="flex items-start gap-3">
							<Checkbox
								disabled={isSaving}
								id="email-product"
								checked={emailProduct}
								onCheckedChange={(v) => setEmailProduct(v === true)}
							/>
							<div className="space-y-0.5">
								<Label htmlFor="email-product">Product updates</Label>
								<p className="text-xs text-muted-foreground">
									Invitations and news about the product.
								</p>
							</div>
						</div>
						<div className="flex items-start gap-3">
							<Checkbox
								disabled={isSaving}
								id="email-weekly-digest"
								checked={emailWeeklyDigest}
								onCheckedChange={(v) => setEmailWeeklyDigest(v === true)}
							/>
							<div className="space-y-0.5">
								<Label htmlFor="email-weekly-digest">Weekly digest</Label>
								<p className="text-xs text-muted-foreground">
									A weekly summary, when your organization has one set up.
								</p>
							</div>
						</div>
						<div className="flex items-start gap-3">
							<Checkbox
								disabled={isSaving}
								id="email-marketing"
								checked={emailMarketing}
								onCheckedChange={(v) => setEmailMarketing(v === true)}
							/>
							<div className="space-y-0.5">
								<Label htmlFor="email-marketing">Marketing</Label>
								<p className="text-xs text-muted-foreground">
									Newsletters and promotions. Off by default.
								</p>
							</div>
						</div>
						<div className="flex justify-end pt-2">
							<Button variant="outline" type="submit" disabled={isSaving}>
								{save.isPending ? "Saving…" : "Save email preferences"}
							</Button>
						</div>
					</form>
				</CardContent>
			</Card>
		</PageBody>
	);
}
