"use client";

/**
 * ThemeToggle — three-state cycle (light → dark → system) shown as
 * an icon button. Slots into the admin sidebar's user-dropdown
 * footer so it's reachable from every admin page without taking
 * top-level chrome real-estate.
 *
 * SSR caveat: useTheme returns undefined on the first server render
 * (no class on <html> yet), which would otherwise flash a different
 * icon. We render the Monitor icon as a placeholder until mounted.
 */

import { Monitor, Moon, Sun } from "lucide-react";
import { useTheme } from "next-themes";
import { useSyncExternalStore } from "react";
import { toast } from "sonner";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { useThemePreference } from "@/features/user-settings/ui/theme-preference-provider";
import { Button } from "@/shared/ui";

const items = [
	{ value: "light", label: "Light", Icon: Sun },
	{ value: "dark", label: "Dark", Icon: Moon },
	{ value: "system", label: "System", Icon: Monitor },
] as const;

const subscribeToHydration = () => () => {};

export function ThemeToggle() {
	const { resolvedTheme } = useTheme();
	const { preference, isSaving, setPreference } = useThemePreference();
	const mounted = useSyncExternalStore(
		subscribeToHydration,
		() => true,
		() => false,
	);

	// Pick the trigger icon: while the theme is "system", use the
	// resolved value so it visually matches what the page is showing.
	const ActiveIcon = (() => {
		if (!mounted) return Monitor;
		const effective = preference === "system" ? resolvedTheme : preference;
		return effective === "dark" ? Moon : Sun;
	})();

	return (
		<DropdownMenu>
			<DropdownMenuTrigger
				render={
					<Button
						variant="ghost"
						size="sm"
						aria-label="Change theme"
						className="h-8 w-8 p-0"
					/>
				}
			>
				<ActiveIcon className="h-4 w-4" />
			</DropdownMenuTrigger>
			<DropdownMenuContent align="end">
				{items.map(({ value, label, Icon }) => (
					<DropdownMenuItem
						key={value}
						onClick={() => {
							void setPreference(value).catch((error) =>
								toast.error("Theme wasn't saved", {
									description:
										error instanceof Error ? error.message : "Try again.",
								}),
							);
						}}
						disabled={isSaving}
						className={preference === value ? "font-medium" : ""}
					>
						<Icon className="mr-2 h-4 w-4" />
						{label}
						{preference === value && (
							<span
								aria-hidden
								className="ml-auto text-xs text-muted-foreground"
							>
								✓
							</span>
						)}
					</DropdownMenuItem>
				))}
			</DropdownMenuContent>
		</DropdownMenu>
	);
}
