"use client";

import { SidebarProvider as KitSidebarProvider } from "@codefly-dev/ui/layout";
import { type ComponentProps, useState, useSyncExternalStore } from "react";

export {
	Sidebar,
	SidebarContent,
	SidebarFooter,
	SidebarGroup,
	SidebarGroupAction,
	SidebarGroupContent,
	SidebarGroupLabel,
	SidebarHeader,
	SidebarInput,
	SidebarInset,
	SidebarMenu,
	SidebarMenuAction,
	SidebarMenuBadge,
	SidebarMenuButton,
	SidebarMenuItem,
	SidebarMenuSkeleton,
	SidebarMenuSub,
	SidebarMenuSubButton,
	SidebarMenuSubItem,
	SidebarRail,
	SidebarSeparator,
	SidebarTrigger,
	useSidebar,
} from "@codefly-dev/ui/layout";

const subscribeToHydration = () => () => {};

export function SidebarProvider({
	open: controlledOpen,
	defaultOpen,
	onOpenChange,
	...props
}: ComponentProps<typeof KitSidebarProvider>) {
	const hydrated = useSyncExternalStore(
		subscribeToHydration,
		() => true,
		() => false,
	);
	const [open, setOpen] = useState(() => {
		// The host owns browser persistence; the shared kit remains cookie-free.
		// Like defaultOpen, the saved choice initializes this mount only.
		if (controlledOpen !== undefined || defaultOpen !== undefined)
			return defaultOpen ?? true;
		if (typeof document === "undefined") return true;
		const saved = document.cookie
			.split(";")
			.map((cookie) => cookie.trim())
			.find((cookie) => cookie.startsWith("sidebar_state="))
			?.slice("sidebar_state=".length);
		return saved !== "false";
	});

	return (
		<KitSidebarProvider
			{...props}
			open={controlledOpen ?? (hydrated ? open : (defaultOpen ?? true))}
			onOpenChange={(open) => {
				if (controlledOpen === undefined) setOpen(open);
				document.cookie = `sidebar_state=${open}; path=/; max-age=${60 * 60 * 24 * 7}`;
				onOpenChange?.(open);
			}}
		/>
	);
}
