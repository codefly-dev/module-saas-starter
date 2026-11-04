"use client";

import { SidebarProvider as KitSidebarProvider } from "@codefly-dev/ui/layout";
import { type ComponentProps, useLayoutEffect, useRef, useState } from "react";

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

export function SidebarProvider({
	open: controlledOpen,
	defaultOpen,
	onOpenChange,
	...props
}: ComponentProps<typeof KitSidebarProvider>) {
	const [open, setOpen] = useState(defaultOpen ?? true);
	const restored = useRef(false);
	useLayoutEffect(() => {
		if (restored.current) return;
		restored.current = true;
		// The host owns browser persistence; the shared kit remains cookie-free.
		// Keep the first client render identical to SSR and honor caller overrides.
		// Like defaultOpen, the saved choice initializes this mount only.
		if (controlledOpen !== undefined || defaultOpen !== undefined) return;
		const saved = document.cookie
			.split(";")
			.map((cookie) => cookie.trim())
			.find((cookie) => cookie.startsWith("sidebar_state="))
			?.slice("sidebar_state=".length);
		if (saved === "true" || saved === "false") setOpen(saved === "true");
	}, [controlledOpen, defaultOpen]);

	return (
		<KitSidebarProvider
			{...props}
			open={controlledOpen ?? open}
			onOpenChange={(open) => {
				if (controlledOpen === undefined) setOpen(open);
				document.cookie = `sidebar_state=${open}; path=/; max-age=${60 * 60 * 24 * 7}`;
				onOpenChange?.(open);
			}}
		/>
	);
}
