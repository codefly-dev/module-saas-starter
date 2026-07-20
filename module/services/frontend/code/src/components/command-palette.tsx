"use client";

/**
 * CommandPalette — global cmd+K / ctrl+K dialog. Routes to admin
 * destinations and exposes a searchable user list (super_admin only)
 * via Connect-ES. Inserted once at the (dashboard) layout root so any
 * authenticated page picks it up.
 *
 * Design choices:
 *   - cmdk + shadcn primitives we already have. Zero new deps.
 *   - Static command list = navigation. Dynamic = users (debounced
 *     async query). Future: orgs, audit events, settings — same shape.
 *   - Visibility gating mirrors the sidebar's RoleGate. We don't show
 *     "Platform Users" search to non-super-admins; they'd hit an
 *     authz error anyway, but better not to dangle the link.
 */

import { LogOut, Users } from "lucide-react";
import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import {
	Command,
	CommandEmpty,
	CommandGroup,
	CommandInput,
	CommandItem,
	CommandList,
} from "@/components/ui/command";
import { Dialog, DialogContent, DialogTitle } from "@/components/ui/dialog";
import { useAuth } from "@/lib/auth";
import { useUsersSearch } from "@/lib/hooks/use-users-search";
import { getNavigationIcon } from "@/lib/navigation-icons";
import { isSuperAdmin } from "@/lib/permissions";
import { selectNavigation } from "@/lib/plugins/presentation";
import { useFrontendConfig } from "@/lib/providers";

export function CommandPalette() {
	const [open, setOpen] = useState(false);
	const [query, setQuery] = useState("");
	const { isAuthenticated, platformRole, orgRole, logout } = useAuth();
	const config = useFrontendConfig();
	const router = useRouter();

	// Hotkey: cmd+K (mac) / ctrl+K (other). Standard for command palettes
	// since Linear / GitHub / Slack made it the de-facto convention.
	useEffect(() => {
		const onKey = (e: KeyboardEvent) => {
			if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
				e.preventDefault();
				setOpen((s) => !s);
			}
		};
		window.addEventListener("keydown", onKey);
		return () => window.removeEventListener("keydown", onKey);
	}, []);

	const superAdmin = isSuperAdmin(platformRole);
	const visibleNav = selectNavigation(config, "command_palette", {
		isAuthenticated,
		platformRole,
		orgRole,
	});

	// Async user search — only when we have a query AND the caller is
	// platform-admin enough to use it. Saves a wasted RPC for everyone
	// else and matches the server-side gate on SearchUsers.
	const userSearchEnabled = open && superAdmin && query.length >= 2;
	const userResults = useUsersSearch(query, userSearchEnabled);

	function navigate(href: string) {
		setOpen(false);
		setQuery("");
		router.push(href);
	}

	if (!isAuthenticated) return null;

	return (
		<Dialog open={open} onOpenChange={setOpen}>
			<DialogContent className="overflow-hidden p-0 max-w-xl">
				<DialogTitle className="sr-only">Command palette</DialogTitle>
				<Command shouldFilter={true}>
					<CommandInput
						placeholder="Search or jump to..."
						value={query}
						onValueChange={setQuery}
					/>
					<CommandList>
						<CommandEmpty>No results.</CommandEmpty>

						<CommandGroup heading="Navigate">
							{visibleNav.map((item) => {
								const Icon = getNavigationIcon(item.icon);
								return (
									<CommandItem
										key={item.href}
										value={`${item.label} ${item.href}`}
										onSelect={() => navigate(item.href)}
									>
										<Icon className="mr-2 h-4 w-4" />
										{item.label}
									</CommandItem>
								);
							})}
						</CommandGroup>

						{superAdmin && userResults.length > 0 && (
							<CommandGroup heading="Users">
								{userResults.map((u) => (
									<CommandItem
										key={u.uuid}
										value={`user ${u.email} ${u.name ?? ""}`}
										onSelect={() => navigate(`/admin/users/${u.uuid}`)}
									>
										<Users className="mr-2 h-4 w-4" />
										<span className="truncate">
											{u.email}
											{u.name ? ` — ${u.name}` : ""}
										</span>
									</CommandItem>
								))}
							</CommandGroup>
						)}

						<CommandGroup heading="Actions">
							<CommandItem
								value="sign out logout"
								onSelect={async () => {
									setOpen(false);
									await logout();
								}}
							>
								<LogOut className="mr-2 h-4 w-4" />
								Sign out
							</CommandItem>
						</CommandGroup>
					</CommandList>
				</Command>
			</DialogContent>
		</Dialog>
	);
}
